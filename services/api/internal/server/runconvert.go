package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"math"
	"net/http"

	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

// Unit harmonisation of a run's declared numbers for the cross-run plot (ADR 0062, amendment). The worker converts with plain
// astropy dimensional conversion and no equivalency, so a conversion that needs a dataset's beam, frame or spectral axis is
// refused there. This handler only validates the request, takes an interactive slot through the worker service (the person is
// waiting on the plot; worker.ClassOf classes the operation) and passes the answer on. It reads no file and no run: the numbers
// come from the browser, which already holds the run list, and nothing is stored.

const (
	maxConvertValues = 5000
	maxConvertUnit   = 40
)

type convertItem struct {
	Value       *float64 `json:"value"` // a pointer: a missing value must be refused, not read as 0
	Uncertainty *float64 `json:"uncertainty"`
	From        string   `json:"from"`
}

type convertItemResult struct {
	Value       float64  `json:"value"`
	Uncertainty *float64 `json:"uncertainty"`
}

func (s *Server) convertRunValues(w http.ResponseWriter, r *http.Request) {
	var in struct {
		To     string        `json:"to"`
		Values []convertItem `json:"values"`
	}
	if !body(w, r, 1<<20, &in, true) {
		return
	}
	if in.To == "" || len(in.To) > maxConvertUnit || len(in.Values) == 0 || len(in.Values) > maxConvertValues {
		fail(w, 400, "give a target unit and between 1 and 5000 values")
		return
	}
	for _, v := range in.Values {
		if v.From == "" || len(v.From) > maxConvertUnit || v.Value == nil || math.IsNaN(*v.Value) || math.IsInf(*v.Value, 0) ||
			(v.Uncertainty != nil && (math.IsNaN(*v.Uncertainty) || math.IsInf(*v.Uncertainty, 0) || *v.Uncertainty < 0)) {
			fail(w, 400, "each value needs a finite number, a finite non-negative uncertainty or none, and the unit it was recorded in")
			return
		}
	}
	svc := s.planesOnRaw(0)
	if svc == nil {
		fail(w, 503, "the scientific worker is not configured")
		return
	}
	rep, err := svc.Do(r.Context(), map[string]any{"op": "convert_values", "to": in.To, "values": in.Values})
	var re *worker.RequestError
	switch {
	case errors.As(err, &re):
		fail(w, 422, re.Message)
		return
	case errors.Is(err, context.Canceled):
		return // the person moved on; nothing to answer
	case err != nil:
		log.Printf("convert values: %v", err)
		fail(w, 502, "the scientific worker is unavailable")
		return
	}
	var out struct {
		Refused bool                `json:"refused"`
		Reason  string              `json:"reason"`
		From    string              `json:"from"`
		To      string              `json:"to"`
		Factors map[string]float64  `json:"factors"`
		Results []convertItemResult `json:"results"`
		Method  string              `json:"method"`
	}
	if json.Unmarshal(rep.Head, &out) != nil || (!out.Refused && len(out.Results) != len(in.Values)) {
		fail(w, 502, "the worker answered unexpectedly")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if out.Refused { // a refusal is an answer, not an error: nothing is converted and the sentence says why
		writeJSON(w, 200, map[string]any{"refused": true, "reason": out.Reason, "from": out.From})
		return
	}
	writeJSON(w, 200, map[string]any{"refused": false, "to": out.To, "factors": out.Factors, "results": out.Results, "method": out.Method,
		"note": "converted by plain unit conversion only; no beam, frame, rest frequency or spectral axis was assumed"})
}
