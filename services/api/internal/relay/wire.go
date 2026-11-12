// Package relay holds relayed run requests (ADR 0066 section 3): what a key holder's inbox accepts, what a member asks for, the
// validation both sides run with the same code, the group server's store of requests and inboxes, and the key holder's local
// mirror. A request names a template, typed values of whitelisted settings within the inbox's bounds, an optional grid, an
// optional base (a pushed run item), a plain-text note and an expiry. It never names a path, a script, a host or a dataset id,
// and it cannot run by itself: the key holder's own computer renders it into a draft with its own templates, the key holder
// reads the exact script, and only a successful claim on the server, bound to that script's hash, lets the computer submit it
// under the key holder's own cluster account.
//
// The server's validation is a first filter. The local one is the real defence: a compromised server can forge or alter any
// request, so the key holder's computer validates every value against the inbox it stored itself, never against bounds the
// server echoes.
package relay

import "time"

// Templates a request may name. There is never a custom script.
const (
	TemplateVGT   = "vgt"
	TemplateROHSA = "rohsa"
)

// Limits (ADR 0066 section 3).
const (
	MaxLabels         = 8
	MaxNote           = 4000 // characters of a request's note, a follow-up note or a decline note
	MaxGridKeys       = 3
	MaxCombos         = 64 // a grid expands to at most 64 combinations (ADR 0048)
	MaxValuesPerKey   = 64
	MaxExpiryDays     = 30
	DefaultExpiryDays = 14
	MaxOpenPerPair    = 10   // open requests per requester per addressee
	MaxFiledPerDay    = 30   // requests filed per requester per day
	MaxPage           = 100  // requests in one pull
	MaxReports        = 2000 // status reports one request takes (a 64-job sweep reports a few per job); its sequence numbers stop there
	MaxCursorJump     = 100000
	MaxNotes          = 20 // follow-up notes on one request
	MaxPageBytes      = 2 << 20
	MaxCPUs           = 256 // the templates' own cap (jobs.RenderVGT, jobs.RenderROHSA)
	MaxMemGiB         = 4096
	MaxTimeMinutes    = 30 * 24 * 60
)

// Server states of a request.
const (
	StateRequested = "requested"      // filed; waiting for the key holder's computer to fetch it
	StateReview    = "waiting_review" // the draft is rendered; waiting for the key holder to review it
	StateApproved  = "approved"       // claimed with a script hash or a sweep hash
	StateSubmitted = "submitted"
	StateRunning   = "running"
	StateFinished  = "finished" // the job ran to its end; results not yet checked (ADR 0043)
	StateFailed    = "failed"
	StateUnknown   = "unknown" // how it ended is not known (ADR 0043)
	StateDeclined  = "declined"
	StateWithdrawn = "withdrawn"
	StateExpired   = "expired"
)

// IsOpen reports whether a request may still be reviewed, withdrawn, declined or expire.
func IsOpen(state string) bool { return state == StateRequested || state == StateReview }

// rank orders the states a computer reports after a claim; a report never lowers it, and an end state is final.
func rank(state string) int {
	switch state {
	case StateApproved:
		return 1
	case StateSubmitted:
		return 2
	case StateRunning:
		return 3
	case StateFinished, StateFailed, StateUnknown:
		return 4
	}
	return 0
}

// Codes qualify a reported state with a fixed sentence the server shows; nothing else the computer saw (no Slurm sentence, id,
// folder or log line) crosses.
const (
	CodeNone             = ""
	CodeNotConfirmed     = "not_confirmed"     // approved; the submission's answer was not seen (ADR 0016 amendment)
	CodeSubmissionFailed = "submission_failed" // approved; the cluster refused the submission
	CodeCancelled        = "cancelled"         // failed: cancelled on the key holder's computer
	CodeTimeout          = "timeout"           // failed: stopped at its time limit
	CodeGone             = "gone"              // unknown: the job record is no longer on the key holder's computer
)

var codes = map[string]bool{CodeNone: true, CodeNotConfirmed: true, CodeSubmissionFailed: true, CodeCancelled: true, CodeTimeout: true, CodeGone: true}

// Bound is the typed bound of one setting under a template: an inclusive numeric range, or a list of allowed values.
type Bound struct {
	Key     string   `json:"key"`
	Min     *float64 `json:"min,omitempty"`
	Max     *float64 `json:"max,omitempty"`
	Choices []string `json:"choices,omitempty"`
}

// Limits caps what a request may ask for in resources. Zero means the request may not ask: the base's own value is used.
type Limits struct {
	MaxCPUs        int `json:"maxCpus"`
	MaxMemGiB      int `json:"maxMemGiB"`
	MaxTimeMinutes int `json:"maxTimeMinutes"`
}

// TemplateBounds is what a label accepts for one template.
type TemplateBounds struct {
	Template string  `json:"template"`
	Keys     []Bound `json:"keys"`
	Limits   Limits  `json:"limits"`
}

// Label is a cluster label typed by the key holder (at most 40 letters, digits, spaces and -): on the server it is free text, and
// only the key holder's computer maps it to a host.
type Label struct {
	Label     string           `json:"label"`
	Templates []TemplateBounds `json:"templates"`
}

// Inbox is what a key holder's computer publishes.
type Inbox struct {
	Paused bool    `json:"paused"`
	Labels []Label `json:"labels"`
}

// Resources a request asks for; a zero field leaves the base's value.
type Resources struct {
	CPUs        int `json:"cpus,omitempty"`
	MemGiB      int `json:"memGiB,omitempty"`
	TimeMinutes int `json:"timeMinutes,omitempty"`
}

// GridKey is one swept setting with its explicit values.
type GridKey struct {
	Key    string   `json:"key"`
	Values []string `json:"values"`
}

// Ask is what a requester asks for, validated against the addressee's inbox on both sides.
type Ask struct {
	Label     string            `json:"label"`
	Template  string            `json:"template"`
	Params    map[string]string `json:"params"`
	Resources Resources         `json:"resources"`
	Grid      []GridKey         `json:"grid"`
	Note      string            `json:"note"`
}

// Base is the pushed run a request starts from, as the key holder's computer needs it: the opaque origin key of its lineage, its
// content hash, and the instance that pushed it when that was the addressee's own (empty otherwise). It carries no local id.
type Base struct {
	Origin   string `json:"origin"`
	Hash     string `json:"hash"`
	Title    string `json:"title"`
	Instance string `json:"instance,omitempty"`
}

// FollowUp is a note added after filing, for example "please cancel".
type FollowUp struct {
	At   time.Time `json:"at"`
	By   string    `json:"by"`
	Text string    `json:"text"`
}

// Request is one request as the key holder's computer pulls it.
type Request struct {
	ID        int64      `json:"id"`
	Change    int64      `json:"change"`
	State     string     `json:"state"`
	Requester string     `json:"requester"`
	Ask       Ask        `json:"ask"`
	Base      *Base      `json:"base,omitempty"`
	CreatedAt time.Time  `json:"createdAt"`
	ExpiresAt time.Time  `json:"expiresAt"`
	Notes     []FollowUp `json:"notes"`
}

// Page is one pull: the requests addressed to the token's account in its workspace that changed after the cursor sent, in the
// order of their change, and the cursor to send next.
type Page struct {
	Workspace string    `json:"workspace"`
	Cursor    int64     `json:"cursor"`
	Requests  []Request `json:"requests"`
}

// Combo is one combination of a grid, by key.
type Combo map[string]string

// Claim is the key holder's approval, sent before anything is submitted: the hash of the exact script read (or the sweep hash
// over the kept drafts, with the kept combinations), the final parameters, resources and label, how the base was found, and how
// many lines were edited by hand. One claim wins per request; it is idempotent for the same request, hash and instance.
type Claim struct {
	Instance    string            `json:"instance"`
	ScriptHash  string            `json:"scriptHash,omitempty"`
	SweepHash   string            `json:"sweepHash,omitempty"`
	Kept        []Combo           `json:"kept,omitempty"`
	Concurrency int               `json:"concurrency,omitempty"`
	Params      map[string]string `json:"params"`
	Resources   Resources         `json:"resources"`
	Label       string            `json:"label"`
	BaseChosen  bool              `json:"baseChosen"`
	EditedLines int               `json:"editedLines"`
	AutoPush    bool              `json:"autoPush"`
}

// Report is a status transition the key holder's computer reports, with a sequence number per request. Only enumerated words
// cross: a state, a code, an exit code of the form 0:0, counts per state for a sweep, and the hash of the pushed run line.
type Report struct {
	Instance string         `json:"instance"`
	Seq      int64          `json:"seq"`
	State    string         `json:"state"`
	Code     string         `json:"code,omitempty"`
	Exit     string         `json:"exit,omitempty"`
	Counts   map[string]int `json:"counts,omitempty"`
	RunHash  string         `json:"runHash,omitempty"`
}

// Review says the draft is rendered (ADR 0066: waiting for review).
type Review struct {
	Instance string `json:"instance"`
}

// Decline is the key holder's no, with a note.
type Decline struct {
	Instance string `json:"instance,omitempty"`
	Note     string `json:"note"`
}
