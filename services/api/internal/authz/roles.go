package authz

// Role is an account's role in one workspace (ADR 0050 section 3, shape.md Q44). Server-wide admin is not a role: it is the
// Actor.Admin flag, which manages accounts and workspaces but sees a workspace's content only through a membership.
type Role string

const (
	RoleNone       Role = "" // not a member: holds no workspace permission
	RoleViewer     Role = "viewer"
	RoleResearcher Role = "researcher"
	RoleMaintainer Role = "maintainer"
	RoleOwner      Role = "owner"
)

// Roles lists the roles from least to most.
func Roles() []Role { return []Role{RoleViewer, RoleResearcher, RoleMaintainer, RoleOwner} }

func (r Role) rank() int {
	for i, k := range Roles() {
		if r == k {
			return i + 1
		}
	}
	return 0
}

// Known reports whether r is one of Roles.
func (r Role) Known() bool { return r.rank() > 0 }

// AtLeast reports whether r is at least min. RoleNone is below everything.
func (r Role) AtLeast(min Role) bool { return r.rank() > 0 && r.rank() >= min.rank() }

// matrix is the permission matrix of ADR 0050 section 3: the lowest role that holds each workspace permission. The ownership
// rule (a Researcher edits, archives and publishes only what they created) is applied on top by the route check (Owns). A
// permission absent here and from accountLevel is held by no role: the local-only features, and the cluster, job and sweep
// permissions, which the group server does not mount (owner answer 3).
var matrix = map[Permission]Role{
	NotificationSelf: RoleViewer,
	DatasetRead:      RoleViewer, RunRead: RoleViewer, FigureRead: RoleViewer, BoardRead: RoleViewer, ReviewRead: RoleViewer,
	ReportRead: RoleViewer, ProjectRead: RoleViewer, HistoryRead: RoleViewer, CommentWrite: RoleViewer, MemberRead: RoleViewer,

	DatasetWrite: RoleResearcher, AnalysisRun: RoleResearcher, RunWrite: RoleResearcher, FigureWrite: RoleResearcher,
	BoardWrite: RoleResearcher, LifecycleArchive: RoleResearcher, ReviewPublish: RoleResearcher, ReportWrite: RoleResearcher,
	ProjectWrite: RoleResearcher, HistoryWrite: RoleResearcher, FilesRead: RoleResearcher,
	PushUpload: RoleResearcher, PushedArchive: RoleResearcher, PushedRead: RoleViewer,

	LifecycleDelete: RoleMaintainer, PublishManage: RoleMaintainer, AuditRead: RoleMaintainer, MemberInvite: RoleMaintainer,

	MemberManage: RoleOwner,
}

// accountLevel are the permissions that do not depend on a workspace: what an account does to itself, and the admin's.
var accountLevel = map[Permission]bool{
	// any account; capabilities too, so an account in no workspace (or naming one it left) still learns where it stands
	AccountSelf: false, AccountCredentials: false, CapabilitiesRead: false,
	AccountAdmin: true, WorkspaceAdmin: true, SupportExport: true, ClusterAdmin: true, // admin only
}

// MinRole returns the lowest role that holds p in a workspace, or RoleNone when no role does (an account-level, admin or
// unmounted permission).
func MinRole(p Permission) Role { return matrix[p] }

// ownership are the permissions the ownership rule applies to: below Maintainer, the actor must have created the object the
// route's {id} names (ADR 0050 section 3: "Researchers edit, archive and publish what they created").
var ownership = map[Permission]bool{
	FigureWrite: true, BoardWrite: true, ReportWrite: true, LifecycleArchive: true, ReviewPublish: true, PushedArchive: true,
}

// OwnershipApplies reports whether p is limited to the actor's own objects below Maintainer.
func OwnershipApplies(p Permission) bool { return ownership[p] }

// Owns decides the ownership rule for an object created by createdBy: the local owner and a Maintainer or Owner act on
// anything in the workspace, anyone else only on what they created.
func (a Actor) Owns(createdBy string) bool {
	return a.IsLocalOwner() || a.Role.AtLeast(RoleMaintainer) || (a.ID != "" && createdBy == a.ID)
}

// MayInvite reports whether the actor may invite someone with role r: an Owner any role, a Maintainer only Viewer or Researcher
// (ADR 0050 threat model: privilege escalation).
func (a Actor) MayInvite(r Role) bool {
	if !r.Known() || !a.Can(MemberInvite) {
		return false
	}
	return a.IsLocalOwner() || a.Role.AtLeast(RoleOwner) || !r.AtLeast(RoleMaintainer)
}
