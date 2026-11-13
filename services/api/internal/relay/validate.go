package relay

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
)

// ErrInvalid wraps every refusal of an inbox, a request, a claim or a report; the message is the sentence to show.
var ErrInvalid = errors.New("invalid")

func refuse(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Sentence is the refusal without its wrapper.
func Sentence(err error) string { return strings.TrimPrefix(err.Error(), ErrInvalid.Error()+": ") }

var (
	labelRe    = regexp.MustCompile(`^[A-Za-z0-9 -]{1,40}$`)
	shaRe      = regexp.MustCompile(`^[0-9a-f]{64}$`)
	instanceRe = regexp.MustCompile(`^[0-9a-f]{32}$`)
	exitRe     = regexp.MustCompile(`^[0-9]{1,3}:[0-9]{1,3}$`)
	originRe   = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

// ValidSHA reports a lowercase SHA-256.
func ValidSHA(s string) bool { return shaRe.MatchString(s) }

// ValidOrigin reports an opaque origin key (16 bytes in hex, ADR 0066 section 2).
func ValidOrigin(s string) bool { return originRe.MatchString(s) }

// ValidInstance reports a 128-bit instance id in hex.
func ValidInstance(s string) bool { return instanceRe.MatchString(s) }

// settable reports whether a request may set key under template (ADR 0066 section 3): a key the template's profile declares, and not
// one Vesqira sets or reads as a path. The output folder is declared internal and the input cube is a path key (or not declared at
// all), so neither can be set whatever a profile lists; a profile that declared a new path key would be refused the same way.
func settable(template, key string) bool {
	_, ok := jobs.SettingKind(template, key)
	return ok && !jobs.FixedByVesqira(template, key)
}

// Settable is settable for callers outside the package (the local re-render of a key holder's own changes).
func Settable(template, key string) bool { return settable(template, key) }

// PlainText checks a note: valid UTF-8, at most MaxNote characters, no control characters other than newline and tab, and no format
// characters (bidirectional overrides, zero-width marks), which could make the text read other than it is. It is shown as text,
// never as Markdown or HTML.
func PlainText(s string, what string) error {
	if !utf8.ValidString(s) {
		return refuse("%s is not valid text", what)
	}
	if utf8.RuneCountInString(s) > MaxNote {
		return refuse("%s is at most %d characters", what, MaxNote)
	}
	for _, r := range s {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return refuse("%s holds a control character", what)
		}
		if unicode.Is(unicode.Cf, r) {
			return refuse("%s holds an invisible formatting character", what)
		}
	}
	return nil
}

// ValidLabel checks a label's shape: at most 40 letters, digits, spaces and -, so it holds no dot or @ and cannot be a host name.
func ValidLabel(l string) bool { return labelRe.MatchString(l) && strings.TrimSpace(l) == l }

// CheckInbox validates an inbox as a key holder's computer publishes it, on both sides.
func CheckInbox(in Inbox) error {
	if err := checkInboxLabels(in); err != nil {
		return err
	}
	return checkOffers(in)
}

func checkInboxLabels(in Inbox) error {
	if len(in.Labels) > MaxLabels {
		return refuse("an inbox has at most %d labels", MaxLabels)
	}
	seen := map[string]bool{}
	for _, l := range in.Labels {
		if !ValidLabel(l.Label) {
			return refuse("label %q: at most 40 letters, digits, spaces and -, so a host name never becomes a label", l.Label)
		}
		if seen[strings.ToLower(l.Label)] {
			return refuse("label %q appears twice", l.Label)
		}
		seen[strings.ToLower(l.Label)] = true
		if len(l.Templates) == 0 || len(l.Templates) > 2 {
			return refuse("label %q takes requests for vgt, rohsa or both", l.Label)
		}
		tseen := map[string]bool{}
		for _, t := range l.Templates {
			if jobs.ShippedTemplate(t.Template) == nil {
				return refuse("label %q: only a tool's template takes requests (vgt, rohsa); there is never a custom script", l.Label)
			}
			if tseen[t.Template] {
				return refuse("label %q lists %s twice", l.Label, t.Template)
			}
			tseen[t.Template] = true
			if t.Limits.MaxCPUs < 0 || t.Limits.MaxCPUs > MaxCPUs || t.Limits.MaxMemGiB < 0 || t.Limits.MaxMemGiB > MaxMemGiB ||
				t.Limits.MaxTimeMinutes < 0 || t.Limits.MaxTimeMinutes > MaxTimeMinutes {
				return refuse("label %q: resource caps are at most %d CPUs, %d GiB and %d minutes", l.Label, MaxCPUs, MaxMemGiB, MaxTimeMinutes)
			}
			kseen := map[string]bool{}
			for _, b := range t.Keys {
				if !settable(t.Template, b.Key) {
					return refuse("label %q: %s is not a setting a request may change under %s", l.Label, b.Key, t.Template)
				}
				if kseen[b.Key] {
					return refuse("label %q lists %s twice", l.Label, b.Key)
				}
				kseen[b.Key] = true
				if b.Min != nil && b.Max != nil && *b.Min > *b.Max {
					return refuse("label %q: %s has a minimum above its maximum", l.Label, b.Key)
				}
				for _, f := range []*float64{b.Min, b.Max} {
					if f != nil && (math.IsNaN(*f) || math.IsInf(*f, 0)) {
						return refuse("label %q: %s has a bound that is not a number", l.Label, b.Key)
					}
				}
				if len(b.Choices) > MaxValuesPerKey {
					return refuse("label %q: %s lists at most %d values", l.Label, b.Key, MaxValuesPerKey)
				}
				for _, c := range b.Choices {
					if err := jobs.CheckTemplateSetting(t.Template, b.Key, c); err != nil {
						return refuse("label %q: %s", l.Label, strings.TrimPrefix(err.Error(), jobs.ErrInvalid.Error()+": "))
					}
				}
			}
		}
	}
	return nil
}

func (in Inbox) find(label, template string) (TemplateBounds, error) {
	for _, l := range in.Labels {
		if l.Label != label {
			continue
		}
		for _, t := range l.Templates {
			if t.Template == template {
				return t, nil
			}
		}
		return TemplateBounds{}, refuse("%q does not take %s requests", label, template)
	}
	return TemplateBounds{}, refuse("this inbox has no label %q", label)
}

// checkValue validates one value of one setting: the template's own validator, then the inbox's bound for it.
func checkValue(t TemplateBounds, key, value string) error {
	var bound *Bound
	for i := range t.Keys {
		if t.Keys[i].Key == key {
			bound = &t.Keys[i]
		}
	}
	if bound == nil || !settable(t.Template, key) {
		return refuse("%s is not a setting this inbox lets a request change", key)
	}
	if err := jobs.CheckTemplateSetting(t.Template, key, value); err != nil {
		return refuse("%s", strings.TrimPrefix(err.Error(), jobs.ErrInvalid.Error()+": "))
	}
	if len(bound.Choices) > 0 {
		ok := false
		for _, c := range bound.Choices {
			ok = ok || c == value
		}
		if !ok {
			return refuse("%s must be one of %s", key, strings.Join(bound.Choices, ", "))
		}
	}
	if bound.Min != nil || bound.Max != nil {
		f, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return refuse("%s must be a number here", key)
		}
		if bound.Min != nil && f < *bound.Min {
			return refuse("%s must be at least %s", key, strconv.FormatFloat(*bound.Min, 'g', -1, 64))
		}
		if bound.Max != nil && f > *bound.Max {
			return refuse("%s must be at most %s", key, strconv.FormatFloat(*bound.Max, 'g', -1, 64))
		}
	}
	return nil
}

// CheckResources validates resources against a template's caps.
func CheckResources(r Resources, l Limits) error {
	check := func(v, max int, what string) error {
		if v < 0 {
			return refuse("%s cannot be negative", what)
		}
		if v > 0 && max == 0 {
			return refuse("this inbox does not let a request choose %s", what)
		}
		if v > max {
			return refuse("%s is at most %d here", what, max)
		}
		return nil
	}
	if err := check(r.CPUs, l.MaxCPUs, "CPUs"); err != nil {
		return err
	}
	if err := check(r.MemGiB, l.MaxMemGiB, "memory (GiB)"); err != nil {
		return err
	}
	return check(r.TimeMinutes, l.MaxTimeMinutes, "time (minutes)")
}

// CheckAsk validates a request against an inbox: the label and template it names, every parameter and grid value by the
// template's validator and the inbox's bound, the resources by the caps, at most three grid keys expanding to at most 64
// combinations, and the note. The server runs it as a first filter; the key holder's computer runs it again against the inbox
// it stored itself.
func CheckAsk(in Inbox, a Ask) error {
	if jobs.ShippedTemplate(a.Template) == nil {
		return refuse("the template is a tool's template (vgt, rohsa); there is never a custom script")
	}
	if a.Grant != "" && !ValidGrantID(a.Grant) {
		return refuse("a standing approval is named by its id")
	}
	t, err := in.find(a.Label, a.Template)
	if err != nil {
		return err
	}
	if len(a.Params) > len(t.Keys) {
		return refuse("more settings than this inbox lets a request change")
	}
	for _, k := range sortedKeys(a.Params) {
		if err := checkValue(t, k, a.Params[k]); err != nil {
			return err
		}
	}
	if err := CheckResources(a.Resources, t.Limits); err != nil {
		return err
	}
	if len(a.Grid) > MaxGridKeys {
		return refuse("a grid has at most %d settings", MaxGridKeys)
	}
	combos := 1
	gseen := map[string]bool{}
	for _, g := range a.Grid {
		if gseen[g.Key] {
			return refuse("%s appears twice in the grid", g.Key)
		}
		gseen[g.Key] = true
		if _, both := a.Params[g.Key]; both {
			return refuse("%s is both a setting and a swept setting", g.Key)
		}
		if len(g.Values) == 0 || len(g.Values) > MaxValuesPerKey {
			return refuse("%s needs 1 to %d values", g.Key, MaxValuesPerKey)
		}
		vseen := map[string]bool{}
		for _, v := range g.Values {
			if vseen[v] {
				return refuse("%s lists %s twice", g.Key, v)
			}
			vseen[v] = true
			if err := checkValue(t, g.Key, v); err != nil {
				return err
			}
		}
		combos *= len(g.Values)
		if combos > MaxCombos {
			return refuse("a grid expands to at most %d combinations", MaxCombos)
		}
	}
	// settings a template ties together (lambda and lambda_amp) change the same config lines, whichever tool declares the tie
	keys := append(sortedKeys(a.Params), gridKeys(a.Grid)...)
	for i, x := range keys {
		for _, y := range keys[i+1:] {
			if jobs.OverlapIn(a.Template, x, y) {
				return refuse("%s and %s change the same config lines; ask for one of them", x, y)
			}
		}
	}
	return PlainText(a.Note, "the note")
}

// CheckAskWhitelist validates a request against the whitelist and the template validators alone, without an inbox's bounds: what
// the key holder's computer still requires of a request it accepted when the key holder has since lowered a bound, which ADR 0066
// leaves to their judgement. Every other rule of CheckAsk holds (keys, values, grid size, resources within the absolute caps).
func CheckAskWhitelist(a Ask) error {
	if !ValidLabel(a.Label) {
		return refuse("a label is 1 to 40 letters, digits, spaces and -")
	}
	t := TemplateBounds{Template: a.Template, Limits: Limits{MaxCPUs: MaxCPUs, MaxMemGiB: MaxMemGiB, MaxTimeMinutes: MaxTimeMinutes}}
	for _, k := range append(sortedKeys(a.Params), gridKeys(a.Grid)...) {
		if !settable(a.Template, k) {
			return refuse("%s cannot be set by a request", k)
		}
		t.Keys = append(t.Keys, Bound{Key: k})
	}
	return CheckAsk(Inbox{Labels: []Label{{Label: a.Label, Templates: []TemplateBounds{t}}}}, a)
}

// Combos expands a grid in the order of ADR 0048 (the first setting changes slowest). Nil for no grid.
func Combos(grid []GridKey) []Combo {
	if len(grid) == 0 {
		return nil
	}
	out := []Combo{{}}
	for _, g := range grid {
		var next []Combo
		for _, c := range out {
			for _, v := range g.Values {
				n := Combo{}
				for k, x := range c {
					n[k] = x
				}
				n[g.Key] = v
				next = append(next, n)
			}
		}
		out = next
	}
	return out
}

// CheckClaim validates a claim against the request it claims (the server cannot know the script; the hash check is the key
// holder's, ADR 0016): the hash or sweep hash present as the request's shape says, kept combinations of the request's own grid,
// the final parameters whitelisted (the key holder may change whitelisted values beyond the inbox's bounds, which is their
// judgement) and the label typed like a label.
func CheckClaim(a Ask, c Claim) error {
	if !ValidInstance(c.Instance) {
		return refuse("name the instance")
	}
	if len(a.Grid) == 0 {
		if !ValidSHA(c.ScriptHash) || c.SweepHash != "" || len(c.Kept) > 0 {
			return refuse("a single request is approved with the hash of the script read")
		}
	} else {
		if !ValidSHA(c.SweepHash) || c.ScriptHash != "" || len(c.Kept) == 0 || len(c.Kept) > MaxCombos {
			return refuse("a sweep request is approved with the sweep hash and the combinations kept")
		}
		all := Combos(a.Grid)
		seen := map[string]bool{}
		for _, k := range c.Kept {
			key := comboKey(k)
			if seen[key] {
				return refuse("a kept combination appears twice")
			}
			seen[key] = true
			found := false
			for _, x := range all {
				found = found || comboKey(x) == key
			}
			if !found {
				return refuse("a kept combination is not one of the request's grid")
			}
		}
		if c.Concurrency < 1 || c.Concurrency > 16 {
			return refuse("the concurrency limit is 1 to 16")
		}
	}
	if !ValidLabel(c.Label) {
		return refuse("the label is at most 40 letters, digits, spaces and -")
	}
	if c.Grant != "" && (c.Grant != a.Grant || len(a.Grid) > 0) {
		return refuse("a claim names only the standing approval the request was filed under")
	}
	for _, k := range sortedKeys(c.Params) {
		if !settable(a.Template, k) {
			return refuse("%s is not a setting a request may change", k)
		}
		if err := jobs.CheckTemplateSetting(a.Template, k, c.Params[k]); err != nil {
			return refuse("%s", strings.TrimPrefix(err.Error(), jobs.ErrInvalid.Error()+": "))
		}
	}
	if c.Resources.CPUs < 0 || c.Resources.CPUs > MaxCPUs || c.Resources.MemGiB < 0 || c.Resources.MemGiB > MaxMemGiB ||
		c.Resources.TimeMinutes < 0 || c.Resources.TimeMinutes > MaxTimeMinutes || c.EditedLines < 0 || c.EditedLines > 100000 {
		return refuse("resources or edited lines out of range")
	}
	return nil
}

// CheckReport validates a status report's words.
func CheckReport(r Report) error {
	if !ValidInstance(r.Instance) || r.Seq < 1 {
		return refuse("name the instance and a sequence number")
	}
	if rank(r.State) == 0 {
		return refuse("a report states approved, submitted, running, finished, failed or unknown")
	}
	if !codes[r.Code] {
		return refuse("unknown code")
	}
	if r.Exit != "" && !exitRe.MatchString(r.Exit) {
		return refuse("an exit code reads like 0:0")
	}
	if r.RunHash != "" && !ValidSHA(r.RunHash) {
		return refuse("the run line is named by its sha256")
	}
	if len(r.Counts) > 8 {
		return refuse("too many counts")
	}
	for k, n := range r.Counts {
		if rank(k) == 0 && k != "draft" || n < 0 || n > MaxCombos {
			return refuse("counts are per state, at most %d", MaxCombos)
		}
	}
	return nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func gridKeys(g []GridKey) []string {
	out := make([]string, 0, len(g))
	for _, x := range g {
		out = append(out, x.Key)
	}
	return out
}

// ComboKey is a combination's canonical text, for comparing two.
func comboKey(c Combo) string {
	keys := make([]string, 0, len(c))
	for k := range c {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "=" + c[k] + "\n")
	}
	return b.String()
}

// ComboKey is comboKey for callers outside the package.
func ComboKey(c Combo) string { return comboKey(c) }
