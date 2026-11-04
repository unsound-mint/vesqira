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

// capabilities answers what this running server can do, from what it was actually started with, so a client can show a
// feature as unavailable instead of failing on a 404. Application version, protocol version and data-schema version are
// reported separately because compatibility depends on the contract, not on matching version numbers.
func (s *Server) capabilities(w http.ResponseWriter, r *http.Request) {
	remote := s.HostDB != nil && s.Remote != nil
	writeJSON(w, 200, map[string]any{
		"app":      map[string]any{"name": "vesqira", "version": Version},
		"protocol": ProtocolVersion,
		"schema":   schema.Version,
		"features": map[string]bool{
			"planes":         s.Planes != nil,
			"planeCache":     s.Cache != nil,
			"preview":        s.Planes != nil, // reduced plane previews (the step parameter)
			"hosts":          remote,
			"jobs":           remote && s.Jobs != nil,
			"analysis":       s.analysisDeps(),
			"remoteAnalysis": s.analysisDeps() && remote && s.Jobs != nil,
			"runs":           s.Runs != nil,
			"boards":         s.Boards != nil,
			"regions":        s.Regions != nil,
			"audit":          s.Audit != nil,
		},
		"limits": map[string]any{"previewStepMax": 64, "metricsPerRunMax": 50},
	})
}
