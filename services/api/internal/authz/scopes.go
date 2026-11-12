package authz

// Scope is what a personal access token may do (ADR 0050 section 2, amended by ADR 0066 section 1). A token's scopes are a
// subset of its account's permissions and never include an account, admin or recent-sign-in permission: a token cannot mint
// another token, unlock anything, delete, or publish to people outside the group.
type Scope string

const (
	ScopeRead  Scope = "read"  // every read-class permission
	ScopeWrite Scope = "write" // the read class and the write class
	// ScopePush and ScopeRelay are ADR 0066's narrow scopes for a linked local instance. Neither implies read or write; their
	// routes come with slices 4 and 5, so today they allow nothing.
	ScopePush  Scope = "push"
	ScopeRelay Scope = "relay"
)

// Scopes lists every scope a token may carry.
func Scopes() []Scope { return []Scope{ScopeRead, ScopeWrite, ScopePush, ScopeRelay} }

// Known reports whether s is one of Scopes.
func (s Scope) Known() bool {
	for _, k := range Scopes() {
		if s == k {
			return true
		}
	}
	return false
}

type class int

const (
	sessionOnly class = iota // never through a token
	readClass
	writeClass
)

// classes assigns every permission to the token class that may use it. A test fails for a permission missing from it, so a
// new permission is a decision about tokens, not a silent default.
var classes = map[Permission]class{
	CapabilitiesRead: readClass, NotificationSelf: readClass,
	DatasetRead: readClass, RunRead: readClass, FigureRead: readClass, BoardRead: readClass, ReviewRead: readClass,
	ReportRead: readClass, ProjectRead: readClass, HistoryRead: readClass, FilesRead: readClass, AuditRead: readClass,
	JobRead: readClass, SweepRead: readClass, ClusterRead: readClass, MemberRead: readClass,

	DatasetWrite: writeClass, AnalysisRun: writeClass, RunWrite: writeClass, FigureWrite: writeClass, BoardWrite: writeClass,
	LifecycleArchive: writeClass, ReportWrite: writeClass, ProjectWrite: writeClass, HistoryWrite: writeClass,
	JobSubmit: writeClass, JobAccess: writeClass, SweepSubmit: writeClass, ClusterUse: writeClass, CommentWrite: writeClass,

	// a recent sign-in, an outsider-facing act or an account or admin act: a person at a browser, never a token
	LifecycleDelete: sessionOnly, ReviewPublish: sessionOnly, PublishManage: sessionOnly, SupportExport: sessionOnly,
	ClusterAdmin: sessionOnly, AccountSelf: sessionOnly, AccountCredentials: sessionOnly, AccountAdmin: sessionOnly,
	LocalOnly: sessionOnly, Anonymous: sessionOnly,
	MemberInvite: sessionOnly, MemberManage: sessionOnly, WorkspaceAdmin: sessionOnly,
}

// Allows reports whether a token carrying this scope may use p.
func (s Scope) Allows(p Permission) bool {
	c, ok := classes[p]
	if !ok || c == sessionOnly {
		return false
	}
	switch s {
	case ScopeRead:
		return c == readClass
	case ScopeWrite:
		return c == readClass || c == writeClass
	}
	return false // push and relay: no route yet
}

// recent are the permissions a session may use only within the recent sign-in window (ADR 0050 section 2: creating an access
// token, deleting, publishing outside the group, admin actions, changing roles).
var recent = map[Permission]bool{
	AccountCredentials: true, AccountAdmin: true, LifecycleDelete: true, ReviewPublish: true, PublishManage: true,
	SupportExport: true, ClusterAdmin: true, MemberManage: true, WorkspaceAdmin: true,
}

// NeedsRecentSignIn reports whether p needs a recent sign-in. The route check enforces it after Can.
func (p Permission) NeedsRecentSignIn() bool { return recent[p] }
