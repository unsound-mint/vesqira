package server

import (
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
		Editor         bool `json:"editor"` // the text editor (--enable-editor, ADR 0029)
	} `json:"features"`
	Limits struct {
		PreviewStepMax   int `json:"previewStepMax"`
		MetricsPerRunMax int `json:"metricsPerRunMax"`
	} `json:"limits"`
}

// capabilities answers what this running server can do, from what it was actually started with, so a client can show a
// feature as unavailable instead of failing on a 404. Application version, protocol version and data-schema version are
// reported separately because compatibility depends on the contract, not on matching version numbers.
func (s *Server) capabilities(w http.ResponseWriter, r *http.Request) {
	remote := s.HostDB != nil && s.Remote != nil
	c := Capabilities{Protocol: ProtocolVersion, Schema: schema.Version}
	c.App.Name, c.App.Version = "vesqira", Version
	f := &c.Features
	f.Planes, f.PlaneCache, f.Preview = s.Planes != nil, s.Cache != nil, s.Planes != nil
	f.Hosts, f.Jobs = remote, remote && s.Jobs != nil
	f.Transfers = remote && s.Transfers != nil
	f.Analysis, f.RemoteAnalysis = s.analysisDeps(), s.analysisDeps() && remote && s.Jobs != nil
	f.Runs, f.Boards, f.Regions, f.Audit = s.Runs != nil, s.Boards != nil, s.Regions != nil, s.Audit != nil
	f.Editor = s.EditorEnabled
	c.Limits.PreviewStepMax, c.Limits.MetricsPerRunMax = 64, 50
	writeJSON(w, 200, c)
}
