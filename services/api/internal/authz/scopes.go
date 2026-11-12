package authz

// Scope is what a personal access token may do (ADR 0050 section 2, amended by ADR 0066 section 1). A token's scopes are a
// subset of its account's permissions and never include an account, admin or recent-sign-in permission: a token cannot mint
// another token, unlock anything, delete, or publish to people outside the group.
type Scope string

const (
	ScopeRead  Scope = "read"  // every read-class permission
	ScopeWrite Scope = "write" // the read class and the write class
	// ScopePush and ScopeRelay are ADR 0066's narrow scopes for a linked local instance. Neither implies read or write, and a
	// token carrying one is bound to one workspace and carries no other scope (accounts.CreateToken). Push allows the push
	// routes of slice 4 (push.upload); relay the relay routes of slice 5 (relay.serve) and declining (run_request.decline).
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
	pushClass      // a linked instance's push token only, never a session (ADR 0066: "a token route refuses a session")
	relayClass     // a linked instance's relay token only, never a session
	relayOrSession // a relay token or a browser session, never a read or write token (ADR 0066 section 4: "B relay or S")
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

	// slice 4: the push routes take only a push token; what was pushed is read and archived by members at a browser (ADR 0066
	// section 4: "S"), so a read or write token cannot reach it either
	PushUpload: pushClass, PushedRead: sessionOnly, PushedArchive: sessionOnly,

	// slice 5: the key holder's computer serves its inbox through a relay token; members file and follow requests at a browser,
	// and the addressee declines from either
	RelayServe: relayClass, RunRequestDecline: relayOrSession, RunRequestRead: sessionOnly, RunRequestFile: sessionOnly,
}

// TokenOnly reports whether p is reached only through a token, never by a session.
func (p Permission) TokenOnly() bool { c := classes[p]; return c == pushClass || c == relayClass }

// Bound reports whether a token with this scope must be bound to one workspace and carry no other scope (ADR 0066 section 1).
func (s Scope) Bound() bool { return s == ScopePush || s == ScopeRelay }

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
	case ScopePush:
		return c == pushClass
	case ScopeRelay:
		return c == relayClass || c == relayOrSession
	}
	return false
}

// recent are the permissions a session may use only within the recent sign-in window (ADR 0050 section 2: creating an access
// token, deleting, publishing outside the group, admin actions, changing roles).
var recent = map[Permission]bool{
	AccountCredentials: true, AccountAdmin: true, LifecycleDelete: true, ReviewPublish: true, PublishManage: true,
	SupportExport: true, ClusterAdmin: true, MemberManage: true, WorkspaceAdmin: true,
}

// NeedsRecentSignIn reports whether p needs a recent sign-in. The route check enforces it after Can.
func (p Permission) NeedsRecentSignIn() bool { return recent[p] }
