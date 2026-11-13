package relay

import (
	"math"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
)

// Standing approvals (ADR 0074). A grant lets one collaborator's requests be submitted without a per-request click, inside narrow
// terms and a budget. The grant lives on the key holder's computer; the group server only sees an offer (no path, host or commit)
// and carries the grant's id on a request. The computer checks every term again for every request.

// Limits on grants.
const (
	MaxGrants         = 20 // live grants one computer offers
	MaxGrantInputs    = 16 // input folders and files of one grant
	MaxGrantJobs      = 1000
	MaxGrantCoreHours = 1000000
	MaxGrantAtOnce    = 16
	MaxGrantDays      = 180
)

// Grant codes: why a request that named a grant waits for the key holder. Only these words cross to the group server, never the
// computer's own sentence (which may name a path).
const (
	GrantUnknown = "unknown" // no such grant here for this person, label and template
	GrantRevoked = "revoked"
	GrantExpired = "expired"
	GrantStale   = "stale" // filed before the grant was made, or the request itself has expired
	GrantOutside = "outside"
	GrantGrid    = "grid"
	GrantBase    = "base"
	GrantInput   = "input"
	GrantCommit  = "commit"
	GrantBudget  = "budget"
	GrantEdited  = "edited"
)

// GrantCodeText is the fixed sentence the group server shows for a code.
var GrantCodeText = map[string]string{
	GrantUnknown: "the standing approval it named is not one the key holder's computer has for you",
	GrantRevoked: "the standing approval was revoked",
	GrantExpired: "the standing approval has expired",
	GrantStale:   "the request was filed before the standing approval was made, or has expired",
	GrantOutside: "it asks for settings or resources outside the standing approval",
	GrantGrid:    "a grid always waits for the key holder",
	GrantBase:    "its base run needs the key holder to choose it",
	GrantInput:   "the base run's input is outside the standing approval",
	GrantCommit:  "the draft would run another code commit than the standing approval allows",
	GrantBudget:  "the standing approval's budget is used up",
	GrantEdited:  "the key holder changed the draft",
}

var (
	grantIDRe   = regexp.MustCompile(`^[0-9a-f]{32}$`)
	accountIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	commitRe    = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

// ValidGrantID reports a grant id (16 random bytes in hex).
func ValidGrantID(s string) bool { return grantIDRe.MatchString(s) }

// ValidAccountID reports the shape of a group server account id.
func ValidAccountID(s string) bool { return accountIDRe.MatchString(s) }

// GrantOffer is what the group server shows of a grant, and only to its collaborator: no path, host, root or commit.
type GrantOffer struct {
	ID            string    `json:"id"`
	Requester     string    `json:"requester"`
	Label         string    `json:"label"`
	Template      string    `json:"template"`
	Keys          []Bound   `json:"keys"`
	PerJob        Limits    `json:"perJob"`
	MaxJobs       int       `json:"maxJobs"`
	JobsUsed      int       `json:"jobsUsed"`
	MaxCoreHours  int       `json:"maxCoreHours"`
	CoreHoursUsed float64   `json:"coreHoursUsed"`
	MaxAtOnce     int       `json:"maxAtOnce"`
	ExpiresAt     time.Time `json:"expiresAt"`
}

// Grant is a standing approval as the key holder's computer keeps it.
type Grant struct {
	ID            string     `json:"id"`
	RequesterID   string     `json:"requesterId"`
	RequesterName string     `json:"requesterName"`
	Label         string     `json:"label"`
	HostID        int64      `json:"hostId"`
	Template      string     `json:"template"`
	Profile       string     `json:"profile"`
	Commit        string     `json:"commit"`
	CommitBasis   string     `json:"commitBasis"` // profile | typed
	Keys          []Bound    `json:"keys"`
	Roots         []string   `json:"roots"`
	Files         []string   `json:"files"`
	MaxJobs       int        `json:"maxJobs"`
	MaxCoreHours  int        `json:"maxCoreHours"`
	PerJob        Limits     `json:"perJob"`
	MaxAtOnce     int        `json:"maxAtOnce"`
	Note          string     `json:"note"`
	CreatedAt     time.Time  `json:"createdAt"`
	ExpiresAt     time.Time  `json:"expiresAt"`
	RevokedAt     *time.Time `json:"revokedAt,omitempty"`
	// usage, read with the grant
	JobsUsed        int `json:"jobsUsed"`
	CoreMinutesUsed int `json:"coreMinutesUsed"`
	AtOnce          int `json:"atOnce"`
	origin          string
}

// Live reports a grant that is neither revoked nor expired at now.
func (g Grant) Live(now time.Time) bool { return g.RevokedAt == nil && g.ExpiresAt.After(now) }

// Offer is the grant as the group server may show it.
func (g Grant) Offer() GrantOffer {
	keys := g.Keys
	if keys == nil {
		keys = []Bound{}
	}
	return GrantOffer{ID: g.ID, Requester: g.RequesterID, Label: g.Label, Template: g.Template, Keys: keys, PerJob: g.PerJob,
		MaxJobs: g.MaxJobs, JobsUsed: g.JobsUsed, MaxCoreHours: g.MaxCoreHours, CoreHoursUsed: math.Round(float64(g.CoreMinutesUsed)/6) / 10,
		MaxAtOnce: g.MaxAtOnce, ExpiresAt: g.ExpiresAt}
}

// checkGrantKeys checks a grant's keys against the label's bounds for its template: every key settable and listed by the inbox,
// narrowed to a range with both ends or to a choice list (not both), and every end and choice passing the template's own validator
// (which holds the template's declared range) and the inbox's bound. Never wider than either.
func checkGrantKeys(t TemplateBounds, keys []Bound) error {
	if len(keys) > len(t.Keys) {
		return refuse("the standing approval lists more settings than the inbox lets a request change")
	}
	seen := map[string]bool{}
	for _, b := range keys {
		if seen[b.Key] {
			return refuse("%s appears twice", b.Key)
		}
		seen[b.Key] = true
		listed := false
		for _, x := range t.Keys {
			listed = listed || x.Key == b.Key
		}
		if !settable(t.Template, b.Key) || !listed {
			return refuse("%s is not a setting your inbox lets a request change under %s", b.Key, t.Template)
		}
		hasRange := b.Min != nil || b.Max != nil
		switch {
		case hasRange && len(b.Choices) > 0:
			return refuse("%s: give a range or a list of values, not both", b.Key)
		case !hasRange && len(b.Choices) == 0:
			return refuse("%s: narrow it to a range (both ends) or a list of values", b.Key)
		case hasRange && (b.Min == nil || b.Max == nil):
			return refuse("%s: a range needs both ends", b.Key)
		case len(b.Choices) > MaxValuesPerKey:
			return refuse("%s lists at most %d values", b.Key, MaxValuesPerKey)
		}
		if hasRange {
			for _, f := range []*float64{b.Min, b.Max} {
				if math.IsNaN(*f) || math.IsInf(*f, 0) {
					return refuse("%s has an end that is not a number", b.Key)
				}
			}
			if *b.Min > *b.Max {
				return refuse("%s has a minimum above its maximum", b.Key)
			}
			for _, f := range []*float64{b.Min, b.Max} {
				if err := checkValue(t, b.Key, strconv.FormatFloat(*f, 'f', -1, 64)); err != nil {
					return refuse("%s: the range must lie inside what the template and your inbox allow (%s)", b.Key, Sentence(err))
				}
			}
		}
		for _, c := range b.Choices {
			if err := checkValue(t, b.Key, c); err != nil {
				return refuse("%s: every value must be one the template and your inbox allow (%s)", b.Key, Sentence(err))
			}
		}
	}
	return nil
}

// CheckGrantTerms validates a grant's terms against the inbox as it is now, at creation and at each use: the label takes the
// template, the keys are narrowed inside the template and the inbox, the per-job caps lie within the inbox's caps, the budget and
// the expiry are within bounds, the commit is a full SHA and the collaborator is an account id. Inputs are checked by the caller,
// which knows the host's roots (CheckGrantInputs).
func CheckGrantTerms(in Inbox, g Grant, now time.Time) error {
	if !ValidAccountID(g.RequesterID) {
		return refuse("choose the collaborator")
	}
	if err := PlainText(g.RequesterName, "the collaborator's name"); err != nil || len([]rune(g.RequesterName)) > 100 {
		return refuse("the collaborator's name is not plain text of at most 100 characters")
	}
	if err := PlainText(g.Note, "the note"); err != nil {
		return err
	}
	if jobs.ShippedTemplate(g.Template) == nil {
		return refuse("a standing approval is for a tool's template (vgt, rohsa)")
	}
	t, err := in.find(g.Label, g.Template)
	if err != nil {
		return err
	}
	if err := checkGrantKeys(t, g.Keys); err != nil {
		return err
	}
	// per job, never more than the inbox lets a request ask for; where the inbox lets a request choose nothing (0), the job keeps
	// the base run's resources and the cap bounds those, up to the relay's own maximum
	pj, lim := g.PerJob, t.Limits
	within := func(inbox, max int) int {
		if inbox > 0 {
			return inbox
		}
		return max
	}
	for _, c := range []struct {
		v, max int
		what   string
	}{{pj.MaxCPUs, within(lim.MaxCPUs, MaxCPUs), "CPUs"}, {pj.MaxMemGiB, within(lim.MaxMemGiB, MaxMemGiB), "memory (GiB)"},
		{pj.MaxTimeMinutes, within(lim.MaxTimeMinutes, MaxTimeMinutes), "wall time (minutes)"}} {
		if c.v < 1 || c.v > c.max {
			return refuse("per job, %s is 1 to %d", c.what, c.max)
		}
	}
	if !commitRe.MatchString(g.Commit) {
		return refuse("the code commit is a full 40-character commit")
	}
	if g.CommitBasis != "profile" && g.CommitBasis != "typed" {
		return refuse("say where the commit comes from")
	}
	switch {
	case g.MaxJobs < 1 || g.MaxJobs > MaxGrantJobs:
		return refuse("the number of jobs is 1 to %d", MaxGrantJobs)
	case g.MaxCoreHours < 1 || g.MaxCoreHours > MaxGrantCoreHours:
		return refuse("the core-hours are 1 to %d", MaxGrantCoreHours)
	case g.MaxAtOnce < 1 || g.MaxAtOnce > MaxGrantAtOnce || g.MaxAtOnce > g.MaxJobs:
		return refuse("jobs at once are 1 to %d, and not more than the number of jobs", MaxGrantAtOnce)
	case !g.ExpiresAt.After(now):
		return refuse("the expiry date is in the past")
	case g.ExpiresAt.After(now.Add(MaxGrantDays * 24 * time.Hour)):
		return refuse("a standing approval lasts at most %d days", MaxGrantDays)
	}
	return nil
}

// CheckGrantInputs validates the input folders and files of a grant: at least one, at most MaxGrantInputs, each an absolute clean
// path without spaces, quotes or "..", not "/", and inside the host's source roots (inRoots). Nothing here reads the host.
func CheckGrantInputs(roots, files []string, inRoots func(string) bool) error {
	if len(roots)+len(files) == 0 {
		return refuse("name at least one input folder or file on the host")
	}
	if len(roots)+len(files) > MaxGrantInputs {
		return refuse("at most %d input folders and files", MaxGrantInputs)
	}
	for _, p := range append(append([]string{}, roots...), files...) {
		if p == "/" || !jobs.ValidPath(p) || path.Clean(p) != p {
			return refuse("%q is not an absolute, clean path (no spaces, quotes, .. or trailing /)", p)
		}
		if !inRoots(p) {
			return refuse("%q is not inside a source root of the host", p)
		}
	}
	return nil
}

// InputAllowed reports whether an input path is one of the grant's files or lies under one of its folders. The path must itself be
// absolute and clean: a path with ".." is never inside anything. The check is lexical, so callers pass the real path the host's
// worker resolved and compare it with the grant's folders and files, which are stored resolved (a link's name proves nothing).
func (g Grant) InputAllowed(p string) bool {
	if p == "" || !path.IsAbs(p) || path.Clean(p) != p || strings.Contains(p, "..") {
		return false
	}
	for _, f := range g.Files {
		if p == f {
			return true
		}
	}
	for _, r := range g.Roots {
		if strings.HasPrefix(p, strings.TrimRight(r, "/")+"/") {
			return true
		}
	}
	return false
}

// CheckGrantAsk is what of an ask the grant covers, on both sides: the same label and template, no grid, every setting a key of the
// grant inside its narrowed bound, and the asked resources within the per-job caps. It returns a code and a sentence.
func CheckGrantAsk(o GrantOffer, a Ask) (string, error) {
	if strings.TrimSpace(a.Label) != o.Label || a.Template != o.Template {
		return GrantUnknown, refuse("the standing approval is for %s on %q", o.Template, o.Label)
	}
	if len(a.Grid) > 0 {
		return GrantGrid, refuse("a grid always waits for the key holder's approval")
	}
	t := TemplateBounds{Template: o.Template, Keys: o.Keys}
	for _, k := range sortedKeys(a.Params) {
		listed := false
		for _, b := range o.Keys {
			listed = listed || b.Key == k
		}
		if !listed {
			return GrantOutside, refuse("the standing approval does not cover %s", k)
		}
		if err := checkValue(t, k, a.Params[k]); err != nil {
			return GrantOutside, refuse("outside the standing approval: %s", Sentence(err))
		}
	}
	r := a.Resources
	if r.CPUs > o.PerJob.MaxCPUs || r.MemGiB > o.PerJob.MaxMemGiB || r.TimeMinutes > o.PerJob.MaxTimeMinutes {
		return GrantOutside, refuse("the standing approval allows at most %d CPUs, %d GiB and %d minutes per job", o.PerJob.MaxCPUs, o.PerJob.MaxMemGiB, o.PerJob.MaxTimeMinutes)
	}
	return "", nil
}

// checkOffers validates the offers an inbox publishes (CheckInbox runs it on both sides).
func checkOffers(in Inbox) error {
	if len(in.Grants) > MaxGrants {
		return refuse("an inbox offers at most %d standing approvals", MaxGrants)
	}
	seen := map[string]bool{}
	for _, o := range in.Grants {
		if !ValidGrantID(o.ID) || seen[o.ID] {
			return refuse("a standing approval needs its own id")
		}
		seen[o.ID] = true
		g := Grant{RequesterID: o.Requester, Template: o.Template, Label: o.Label, Keys: o.Keys, PerJob: o.PerJob, MaxJobs: o.MaxJobs,
			MaxCoreHours: o.MaxCoreHours, MaxAtOnce: o.MaxAtOnce, ExpiresAt: o.ExpiresAt, Commit: strings.Repeat("0", 40), CommitBasis: "typed"}
		if err := CheckGrantTerms(in, g, o.ExpiresAt.Add(-time.Second)); err != nil {
			return refuse("standing approval %s: %s", o.ID[:8], Sentence(err))
		}
		if o.JobsUsed < 0 || o.JobsUsed > o.MaxJobs || o.CoreHoursUsed < 0 || math.IsNaN(o.CoreHoursUsed) || o.CoreHoursUsed > MaxGrantCoreHours {
			return refuse("standing approval %s: its usage is out of range", o.ID[:8])
		}
	}
	return nil
}
