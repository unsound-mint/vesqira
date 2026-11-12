package authz

// Permission is what a route needs. Names are "resource.action" and are the values of the x-vesqira-permission extension
// in packages/contracts/openapi.yaml (tools/contracts.py reads this file and the s.route declarations, so the two cannot
// drift). The comment on each says the lowest role of the ADR 0050 section 3 matrix that holds it, to be enforced in slice 3
// ("V" Viewer, "R" Researcher, "M" Maintainer, "A" admin). Where the matrix does not say, the stricter permission was chosen.
// The ownership rule (a Researcher acts on what they created) is a handler check on top of the permission, not another name.
type Permission string

const (
	// Anonymous needs no actor: the static app, and on the group server the sign-in steps and the first-admin setup. A short,
	// fixed list (the OIDC callback joins it when OIDC is built).
	Anonymous Permission = "anonymous"

	CapabilitiesRead Permission = "capabilities.read" // any account (slice 3: not a workspace permission): what this program can do, and the workload it is carrying
	NotificationSelf Permission = "notification.self" // V: the actor's own notifications

	DatasetRead  Permission = "dataset.read"  // V: view, planes, coordinates, spectra, catalogs, regions, masks, subsets, compat; pure computations over a dataset
	DatasetWrite Permission = "dataset.write" // R: register, declare unit, nature or interpretation, regions, masks, corrections, locations, subsets
	AnalysisRun  Permission = "analysis.run"  // R: local analyses, managed copy, writing a subset to a file (batch workload class)

	RunRead  Permission = "run.read"  // V: runs, history, rerun description, saved run views, unit conversion
	RunWrite Permission = "run.write" // R: import a run, QC assessment, run controls, save a run view

	FigureRead  Permission = "figure.read"  // V
	FigureWrite Permission = "figure.write" // R: create, edit text
	BoardRead   Permission = "board.read"   // V
	BoardWrite  Permission = "board.write"  // R: create, edit items, layout, pin

	LifecycleArchive Permission = "lifecycle.archive" // R: archive and restore (own)
	LifecycleDelete  Permission = "lifecycle.delete"  // M: delete and its impact preview

	ReviewRead    Permission = "review.read"    // V: snapshots of a board and the comments on them
	ReviewPublish Permission = "review.publish" // R (own boards): preflight, publish a review link, revoke, reply, resolve

	ReportRead    Permission = "report.read"    // V
	ReportWrite   Permission = "report.write"   // R: write, move, attachments, comment drafts, mark seen
	PublishManage Permission = "publish.manage" // M: the publish server: publish or withdraw a report, readers, settings, sync, send replies
	ProjectRead   Permission = "project.read"   // V: opened folders, their tree and needs
	ProjectWrite  Permission = "project.write"  // R: open a folder, scan, answer, write the project file, import rules
	HistoryRead   Permission = "history.read"   // V: the actor's exploration history
	HistoryWrite  Permission = "history.write"  // R
	FilesRead     Permission = "files.read"     // R: roots, the file tree, path completion, previews (workspace data roots only)
	AuditRead     Permission = "audit.read"     // M: the workspace's audit log
	SupportExport Permission = "support.export" // A: the support bundle

	JobRead     Permission = "job.read"     // V (state only): stored job records, rerun check, sizing
	JobSubmit   Permission = "job.submit"   // R with own cluster access: draft, edit, submit, cancel, reopen, analysis on a host
	JobAccess   Permission = "job.access"   // R, submitter only: log, result, register outputs, refresh (each an ssh read)
	SweepRead   Permission = "sweep.read"   // V (grid only)
	SweepSubmit Permission = "sweep.submit" // R with own cluster access
	ClusterRead Permission = "cluster.read" // R with own cluster access: list, browse, complete, text, storage, folders, check
	ClusterUse  Permission = "cluster.use"  // R with own cluster access: register remote, transfers, VPN connect
	// ClusterAdmin is A: declare or delete a cluster, set its VPN commands.
	ClusterAdmin Permission = "cluster.admin"
	// AccountSelf is any signed-in account, acting on itself through a session: its sign-in state, signing out (here or
	// everywhere), listing and revoking its own access tokens. Never through a token.
	AccountSelf Permission = "account.self"
	// AccountCredentials is the account changing what proves it is itself: creating an access token, enrolling the second factor
	// again, new recovery codes. A session with a recent sign-in only.
	AccountCredentials Permission = "account.credentials"
	// AccountAdmin is A: list accounts, reset another account's second factor. A recent sign-in only.
	AccountAdmin Permission = "account.admin"
	// LocalOnly is nobody in multi-user mode (not mounted): file chooser, ssh config, text editor, terminal, offline copies.
	LocalOnly Permission = "local.only"

	// Slice 3: workspaces and their members.
	CommentWrite Permission = "comment.write" // V: comment on a figure or a report of the workspace, delete one's own comment
	MemberRead   Permission = "member.read"   // V: the workspace's members and their roles
	MemberInvite Permission = "member.invite" // M: invite someone (a Maintainer only as Viewer or Researcher), list and revoke invitations
	// MemberManage is O: change a member's role, remove a member (never the last Owner). A recent sign-in only.
	MemberManage Permission = "member.manage"
	// WorkspaceAdmin is A: create a workspace and name its first Owner, declare its data roots, set its quota. A recent sign-in only.
	WorkspaceAdmin Permission = "workspace.admin"
)

// All is every permission a route may declare. A test fails for a route that names anything else.
func All() []Permission {
	return []Permission{
		Anonymous, CapabilitiesRead, NotificationSelf,
		DatasetRead, DatasetWrite, AnalysisRun, RunRead, RunWrite,
		FigureRead, FigureWrite, BoardRead, BoardWrite, LifecycleArchive, LifecycleDelete,
		ReviewRead, ReviewPublish, ReportRead, ReportWrite, PublishManage,
		ProjectRead, ProjectWrite, HistoryRead, HistoryWrite, FilesRead, AuditRead, SupportExport,
		JobRead, JobSubmit, JobAccess, SweepRead, SweepSubmit,
		ClusterRead, ClusterUse, ClusterAdmin, AccountSelf, AccountCredentials, AccountAdmin, LocalOnly,
		CommentWrite, MemberRead, MemberInvite, MemberManage, WorkspaceAdmin,
	}
}

// Known reports whether p is one of All.
func (p Permission) Known() bool {
	for _, q := range All() {
		if p == q {
			return true
		}
	}
	return false
}

func (p Permission) String() string { return string(p) }
