package server

import (
	"net/http"

	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

// analysisLabels name the analysis programs in a queue reason.
var analysisLabels = map[string]string{
	"relorient_maps": "relative orientation", "relorient_catalog": "relative orientation", "stats": "statistics",
	"moment": "moment map", "cutout": "cutout", "slab": "slab mean", "align": "alignment", "convolve": "convolution",
	"scalar_diff": "difference map", "axial_diff": "axial difference",
}

func analysisLabel(op string) string {
	if l, ok := analysisLabels[op]; ok {
		return l
	}
	return "analysis"
}

// WorkloadReply is the body of GET /api/workload.
type WorkloadReply struct {
	Classes []worker.ClassState  `json:"classes"`
	Request *worker.RequestState `json:"request,omitempty"`
}

// workload reports the budget, running and waiting counts of each workload class and, when ?request=<id> names the
// X-Request-Id a slow request was sent with, whether that request is waiting and why. A panel that has been waiting for a
// heavy answer asks this, so the person reads a reason instead of a silent spinner (ADR 0056). Nothing in it names a file,
// dataset or path: only operation names.
func (s *Server) workload(w http.ResponseWriter, r *http.Request) {
	out := WorkloadReply{Classes: []worker.ClassState{}}
	if s.Load != nil {
		out.Classes = s.Load.Snapshot()
		if id := r.URL.Query().Get("request"); id != "" {
			st := s.Load.Request(id)
			out.Request = &st
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, out)
}
