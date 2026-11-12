package server

import (
	"github.com/unsound-mint/vesqira/services/api/internal/accounts"
	"github.com/unsound-mint/vesqira/services/api/internal/authz"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
	"net/http"

	"github.com/unsound-mint/vesqira/services/api/internal/schema"
)

// Version is the application version. It is "dev" for a plain build; release builds set it with
// -ldflags "-X github.com/unsound-mint/vesqira/services/api/internal/server.Version=...".
var Version = "dev"

// ProtocolVersion changes only when an existing route or field changes meaning in a way an older client would misread.
// Adding a route or an optional field does not change it.
const ProtocolVersion = 1

// Capabilities is the body of GET /api/capabilities.
type Capabilities struct {
	App struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	} `json:"app"`
	Protocol int `json:"protocol"`
	Schema   int `json:"schema"`
	Features struct {
		Planes         bool `json:"planes"`
		PlaneCache     bool `json:"planeCache"`
		Preview        bool `json:"preview"` // reduced plane previews (the step parameter)
		Hosts          bool `json:"hosts"`
		Jobs           bool `json:"jobs"`
		Transfers      bool `json:"transfers"` // the resumable transfer queue (ADR 0051)
		Analysis       bool `json:"analysis"`
		RemoteAnalysis bool `json:"remoteAnalysis"`
		Runs           bool `json:"runs"`
		Boards         bool `json:"boards"`
		Regions        bool `json:"regions"`
		Audit          bool `json:"audit"`
		Editor         bool `json:"editor"`   // the text editor (--enable-editor, ADR 0029)
		Terminal       bool `json:"terminal"` // the integrated terminal (--enable-terminal, ADR 0029)
		VPN            bool `json:"vpn"`      // the VPN companion (--enable-vpn, ADR 0029)
		// Accounts is true on the group server (--multi-user): people sign in, and the account routes exist. Omitted in local
		// mode, so the local answer is what it was.
		Accounts bool `json:"accounts,omitempty"`
	} `json:"features"`
	Limits struct {
		PreviewStepMax   int `json:"previewStepMax"`
		MetricsPerRunMax int `json:"metricsPerRunMax"`
	} `json:"limits"`
	// Workspace is, on the group server, where the request acts and what the caller may do there (ADR 0050 section 3, "view
	// effective permissions"), so the page can hide what would be refused; the server still checks every request. Omitted in
	// local mode, and for an account that is a member of no workspace.
	Workspace *WorkspaceContext `json:"workspace,omitempty"`
}

// WorkspaceContext is the caller's current workspace, role, effective permissions and every workspace they belong to.
type WorkspaceContext struct {
	ID          workspace.ID          `json:"id"`
	Name        string                `json:"name"`
	Role        authz.Role            `json:"role"`
	Permissions []authz.Permission    `json:"permissions"`
	Workspaces  []accounts.Membership `json:"workspaces"`
}

// workspaceContext builds the caller's WorkspaceContext, or nil in local mode or without a membership.
func (s *Server) workspaceContext(r *http.Request) *WorkspaceContext {
	a, ok := authz.From(r.Context())
	if !s.multiUser() || !ok || a.Workspace <= 0 {
		return nil
	}
	st := s.MultiUser.Accounts
	wsp, err := st.Workspace(a.Workspace)
	if err != nil {
		return nil
	}
	ms, _ := st.Memberships(a.ID)
	out := &WorkspaceContext{ID: wsp.ID, Name: wsp.Name, Role: a.Role, Permissions: []authz.Permission{}, Workspaces: ms}
	for _, p := range authz.All() {
		if p != authz.Anonymous && a.Can(p) && !notOnGroupServer[p] {
			out.Permissions = append(out.Permissions, p)
		}
	}
	return out
}

// capabilities answers what this running server can do, from what it was actually started with, so a client can show a
// feature as unavailable instead of failing on a 404. Application version, protocol version and data-schema version are
// reported separately because compatibility depends on the contract, not on matching version numbers.
func (s *Server) capabilities(w http.ResponseWriter, r *http.Request) {
	c := s.currentCapabilities()
	c.Workspace = s.workspaceContext(r)
	writeJSON(w, 200, c)
}

func (s *Server) currentCapabilities() Capabilities {
	remote := s.HostDB != nil && s.Remote != nil
	c := Capabilities{Protocol: ProtocolVersion, Schema: schema.Version}
	c.App.Name, c.App.Version = "vesqira", Version
	f := &c.Features
	f.Planes, f.PlaneCache, f.Preview = s.Planes != nil, s.Cache != nil, s.Planes != nil
	f.Hosts, f.Jobs = remote, remote && s.Jobs != nil
	f.Transfers = remote && s.Transfers != nil
	f.Analysis, f.RemoteAnalysis = s.analysisDeps(), s.analysisDeps() && remote && s.Jobs != nil
	f.Runs, f.Boards, f.Regions, f.Audit = s.Runs != nil, s.Boards != nil, s.Regions != nil, s.Audit != nil
	f.Editor, f.Terminal = s.EditorEnabled, s.TerminalEnabled
	f.VPN = s.VPNEnabled && remote
	if s.multiUser() { // the routes these name are not mounted on the group server (route.go)
		f.Hosts, f.Jobs, f.Transfers, f.RemoteAnalysis, f.Editor, f.Terminal, f.VPN = false, false, false, false, false, false, false
		f.Accounts = true
	}
	c.Limits.PreviewStepMax, c.Limits.MetricsPerRunMax = 64, 50
	return c
}
