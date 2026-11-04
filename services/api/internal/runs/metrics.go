package runs

import (
	"math"
	"regexp"
	"sort"
	"strings"
)

// A Metric is one measured result of a run, with what is needed to read it correctly: its unit, how uncertain it is and
// in what sense, how many independent samples it rests on, and a definition. A value that is not recorded is nil and is
// shown as unknown; it is never 0. The definition is the researcher's own text and is declared, not verified: the
// application cannot know how a number was computed, only what the run said about it.
type Metric struct {
	Key             string   `json:"key"`
	Value           *float64 `json:"value"`
	Unit            string   `json:"unit"`
	Uncertainty     *float64 `json:"uncertainty"`
	UncertaintyKind string   `json:"uncertaintyKind"` // standard_deviation | standard_error | confidence_68 | confidence_95 | other | "" (not stated)
	N               *int64   `json:"n"`               // independent samples, when stated
	Definition      string   `json:"definition"`
	Status          string   `json:"status"` // declared: it is what the run's metadata says
}

const (
	maxMetrics    = 50
	maxDefinition = 500
	maxUnit       = 40
)

var metricKey = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,59}$`)
var uncertaintyKinds = map[string]bool{"standard_deviation": true, "standard_error": true, "confidence_68": true, "confidence_95": true, "other": true}

// pathLike matches text that names a place on some machine ("/home/x/", "~/data", "C:\..."), which never leaves the machine.
var pathLike = regexp.MustCompile(`(^|[\s(\[=:"'])(~|/)[\w.\-]+/|[A-Za-z]:\\|\.\./|^\\\\|(?i:file:)|(?i:\b(?:s3|gs|nfs|smb|sftp|ssh|scp|hdfs)://)`)

func number(v any) (float64, bool) {
	var f float64
	switch x := v.(type) {
	case float64:
		f = x
	case int64:
		f = float64(x)
	default:
		return 0, false
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0, false
	}
	return f, true
}

// parseMetrics reads the optional [metrics] section of a run's metadata. Each metric is either a bare number or a table
// with value, unit, uncertainty, uncertainty_kind, n and definition. Anything malformed is dropped field by field: a
// non-finite value leaves the metric unknown, a negative uncertainty is not kept, an uncertainty without a stated kind keeps
// an empty kind rather than a guessed one.
func parseMetrics(raw map[string]any) []Metric {
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []Metric
	for _, k := range keys {
		if !metricKey.MatchString(k) || len(out) >= maxMetrics {
			continue
		}
		m := Metric{Key: k, Status: "declared"}
		switch x := raw[k].(type) {
		case map[string]any:
			if f, ok := number(x["value"]); ok {
				m.Value = &f
			}
			if u, ok := x["unit"].(string); ok && len(u) <= maxUnit && !pathLike.MatchString(u) && !strings.ContainsAny(u, "\n\r") {
				m.Unit = u
			}
			if f, ok := number(x["uncertainty"]); ok && f >= 0 {
				m.Uncertainty = &f
				if kind, ok := x["uncertainty_kind"].(string); ok && uncertaintyKinds[kind] {
					m.UncertaintyKind = kind
				}
			}
			if n, ok := x["n"].(int64); ok && n >= 0 {
				m.N = &n
			}
			if d, ok := x["definition"].(string); ok && !pathLike.MatchString(d) {
				d = strings.TrimSpace(d)
				if len(d) > maxDefinition {
					d = d[:maxDefinition]
				}
				m.Definition = d
			}
		default:
			if f, ok := number(x); ok {
				m.Value = &f
			}
		}
		out = append(out, m)
	}
	return out
}
