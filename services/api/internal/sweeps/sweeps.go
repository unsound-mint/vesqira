// Package sweeps holds parameter sweeps (ADR 0048): a base job, a grid of explicit parameter values, and the ordinary draft
// jobs the grid expands to. This file is the pure part: expanding and validating the grid, naming the jobs, comparing a
// draft's script with the base, and building the table and plot series. It knows nothing about Slurm or the database.
package sweeps

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	MaxJobs        = 64 // the cartesian product of the value lists
	MaxParams      = 3
	MaxValueLen    = 40
	MaxLabelLen    = 20
	MaxConcurrency = 16
	maxNameLen     = 40 // a job name (jobs package): letters, digits, . _ -
)

var (
	valueRe = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,40}$`)
	keyRe   = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.]{0,39}$`)
	numRe   = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?([eE][-+]?[0-9]+)?$`)
	unsafe  = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
)

// Param is one swept setting: its key as the pipeline reads it, an optional display label ("λ" for lambda), and the explicit
// list of values. The values are kept as text, exactly as typed, and are never reparsed or reformatted.
type Param struct {
	Key    string   `json:"key"`
	Label  string   `json:"label"`
	Values []string `json:"values"`
}

// Name is how the parameter is shown: its label, else its key.
func (p Param) Name() string {
	if p.Label != "" {
		return p.Label
	}
	return p.Key
}

// Validate checks the shape of a grid and the size of its product. Whether a key is a setting the template can change, and
// whether a value suits that key, is the caller's to check.
func Validate(params []Param) error {
	if len(params) < 1 || len(params) > MaxParams {
		return fmt.Errorf("a sweep varies 1 to %d parameters", MaxParams)
	}
	n := 1
	seenKey := map[string]bool{}
	for _, p := range params {
		if !keyRe.MatchString(p.Key) {
			return fmt.Errorf("%q is not a setting name", p.Key)
		}
		if seenKey[p.Key] {
			return fmt.Errorf("%s is listed twice", p.Key)
		}
		seenKey[p.Key] = true
		if utf8.RuneCountInString(p.Label) > MaxLabelLen || strings.ContainsAny(p.Label, "\n\r\t") {
			return fmt.Errorf("the label of %s is at most %d characters on one line", p.Key, MaxLabelLen)
		}
		if len(p.Values) < 1 {
			return fmt.Errorf("%s needs at least one value", p.Key)
		}
		seen := map[string]bool{}
		for _, v := range p.Values {
			if !valueRe.MatchString(v) {
				return fmt.Errorf("%s: %q is not a value (letters, digits and . _ + - only, up to %d)", p.Key, v, MaxValueLen)
			}
			if seen[v] {
				return fmt.Errorf("%s: %s is listed twice", p.Key, v)
			}
			seen[v] = true
		}
		n *= len(p.Values)
		if n > MaxJobs {
			return fmt.Errorf("that grid is more than %d jobs; narrow a list", MaxJobs)
		}
	}
	return nil
}

// Count is the number of jobs the grid expands to (the product of the list lengths), without checking the grid.
func Count(params []Param) int {
	n := 1
	for _, p := range params {
		n *= len(p.Values)
		if n > 1<<20 {
			return n
		}
	}
	return n
}

// Expand lists every combination in parameter order: the first parameter is the slowest-changing, the last the fastest, and
// each list keeps the order it was given in.
func Expand(params []Param) ([]map[string]string, error) {
	if err := Validate(params); err != nil {
		return nil, err
	}
	out := []map[string]string{{}}
	for _, p := range params {
		next := make([]map[string]string, 0, len(out)*len(p.Values))
		for _, base := range out {
			for _, v := range p.Values {
				m := make(map[string]string, len(base)+1)
				for k, x := range base {
					m[k] = x
				}
				m[p.Key] = v
				next = append(next, m)
			}
		}
		out = next
	}
	return out, nil
}

// Label is "N = 12, λ = 10": the display name and value of each parameter, in parameter order. It is for the owner's screen;
// it is never stored in a board item or sent to a reviewer.
func Label(params []Param, values map[string]string) string {
	parts := make([]string, 0, len(params))
	for _, p := range params {
		parts = append(parts, p.Name()+" = "+values[p.Key])
	}
	return strings.Join(parts, ", ")
}

// JobName makes a job name that satisfies the jobs package's rule (ASCII letters, digits, . _ -, at most 40) from the sweep's
// name and the combination: "vgt-sweep-dn16-ker4". When the parameters do not fit it falls back to the position ("-s07"),
// which is always unique within the sweep.
func JobName(stem string, params []Param, values map[string]string, index int) string {
	var b strings.Builder
	for _, p := range params {
		b.WriteString("-")
		b.WriteString(unsafe.ReplaceAllString(p.Key, ""))
		b.WriteString(unsafe.ReplaceAllString(values[p.Key], ""))
	}
	suffix := b.String()
	if len(stem)+len(suffix) <= maxNameLen && len(stem) > 0 {
		return stem + suffix
	}
	fallback := fmt.Sprintf("-s%02d", index+1)
	if len(stem)+len(fallback) > maxNameLen {
		stem = stem[:maxNameLen-len(fallback)]
	}
	return stem + fallback
}

// Numeric reports whether every value parses as a number, so a plot may put them on a numeric axis.
func Numeric(values []string) bool {
	for _, v := range values {
		if !numRe.MatchString(v) {
			return false
		}
	}
	return len(values) > 0
}

// ---- comparing a draft's script with the base ----

// DiffLine is one line that differs. Op is "-" (in the base only) or "+" (in the draft only). Kind says why the line is
// expected to differ: "parameter" (a swept key), "output" (the output folder, which Vesqira sets per job), "name" (the Slurm
// job name), or "other", which is never expected and is the reason to read the script.
type DiffLine struct {
	Op   string `json:"op"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// Diff compares a draft's script with the base's, line by line (longest common subsequence), and classifies each line that
// differs. keys are the swept settings.
func Diff(base, draft string, keys []string) []DiffLine {
	a, b := strings.Split(base, "\n"), strings.Split(draft, "\n")
	n, m := len(a), len(b)
	// lcs[i][j]: length of the common subsequence of a[i:] and b[j:]
	lcs := make([][]int32, n+1)
	for i := range lcs {
		lcs[i] = make([]int32, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else if lcs[i+1][j] >= lcs[i][j+1] {
				lcs[i][j] = lcs[i+1][j]
			} else {
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}
	var out []DiffLine
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, DiffLine{"-", classify(a[i], keys), a[i]})
			i++
		default:
			out = append(out, DiffLine{"+", classify(b[j], keys), b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, DiffLine{"-", classify(a[i], keys), a[i]})
	}
	for ; j < m; j++ {
		out = append(out, DiffLine{"+", classify(b[j], keys), b[j]})
	}
	return out
}

func classify(line string, keys []string) string {
	t := strings.TrimSpace(line)
	for _, k := range keys {
		if strings.HasPrefix(t, k) && strings.HasPrefix(strings.TrimSpace(t[len(k):]), "=") {
			return "parameter"
		}
	}
	switch {
	case strings.HasPrefix(t, "output_dir") && strings.HasPrefix(strings.TrimSpace(t[len("output_dir"):]), "="):
		return "output"
	case strings.HasPrefix(t, "#SBATCH --job-name="):
		return "name"
	}
	return "other"
}

// Unexpected counts the differing lines that are neither a swept setting, the output folder nor the job name.
func Unexpected(d []DiffLine) int {
	n := 0
	for _, l := range d {
		if l.Kind == "other" {
			n++
		}
	}
	return n
}

// ---- the table and the plot ----

// Metric is one declared number of a run. It mirrors runs.Metric so this package stays free of the runs package; Value nil
// is "not recorded", never zero.
type Metric struct {
	Key         string   `json:"key"`
	Value       *float64 `json:"value"`
	Unit        string   `json:"unit"`
	Uncertainty *float64 `json:"uncertainty"`
	Definition  string   `json:"definition"`
}

// Row is one job of the grid with what its run declared, when it has one.
type Row struct {
	JobID   int64             `json:"jobId"`
	Name    string            `json:"name"`
	Label   string            `json:"label"`
	Values  map[string]string `json:"values"`
	State   string            `json:"state"`
	RunID   *int64            `json:"runId"`
	RunName string            `json:"runName"`
	Metrics []Metric          `json:"metrics"` // never nil
}

// MetricInfo summarises one metric key across the rows that have it. A key whose runs declared different units is not
// comparable as one series (UnitsDiffer); different definitions are reported and left to the researcher (DefinitionsDiffer).
type MetricInfo struct {
	Key               string   `json:"key"`
	Unit              string   `json:"unit"`
	Units             []string `json:"units"`
	UnitsDiffer       bool     `json:"unitsDiffer"`
	DefinitionsDiffer bool     `json:"definitionsDiffer"`
	Runs              int      `json:"runs"`   // rows whose run declared this key
	Values            int      `json:"values"` // of those, rows with a recorded value
}

// Metrics lists the metric keys that appear in the rows, in the order first met.
func Metrics(rows []Row) []MetricInfo {
	var order []string
	info := map[string]*MetricInfo{}
	defs := map[string]map[string]bool{}
	units := map[string]map[string]bool{}
	for _, r := range rows {
		for _, m := range r.Metrics {
			mi := info[m.Key]
			if mi == nil {
				mi = &MetricInfo{Key: m.Key, Units: []string{}}
				info[m.Key] = mi
				defs[m.Key], units[m.Key] = map[string]bool{}, map[string]bool{}
				order = append(order, m.Key)
			}
			mi.Runs++
			if m.Value == nil {
				continue
			}
			mi.Values++
			defs[m.Key][m.Definition] = true
			if !units[m.Key][m.Unit] {
				units[m.Key][m.Unit] = true
				mi.Units = append(mi.Units, m.Unit)
			}
		}
	}
	out := make([]MetricInfo, 0, len(order))
	for _, k := range order {
		mi := info[k]
		if len(mi.Units) == 1 {
			mi.Unit = mi.Units[0]
		}
		mi.UnitsDiffer = len(mi.Units) > 1
		mi.DefinitionsDiffer = len(defs[k]) > 1
		out = append(out, *mi)
	}
	return out
}

// Point is one job's value on a plot. Value nil is a gap: the run has no recorded value (or no run exists yet), and the point
// is not drawn as zero.
type Point struct {
	X           string   `json:"x"`
	XNum        *float64 `json:"xNum"`
	Value       *float64 `json:"value"`
	Uncertainty *float64 `json:"uncertainty"`
	JobID       int64    `json:"jobId"`
}

// Line is the points that share every parameter except the one on the x axis, in the x parameter's order.
type Line struct {
	Label  string  `json:"label"`
	Points []Point `json:"points"`
}

// Plot is a metric against one parameter, one line per combination of the other parameters.
type Plot struct {
	Metric  string   `json:"metric"`
	Unit    string   `json:"unit"`
	XKey    string   `json:"xKey"`
	XName   string   `json:"xName"`
	Numeric bool     `json:"numeric"` // the x values are numbers and sit on a numeric axis; otherwise they are categories in list order
	XValues []string `json:"xValues"`
	Lines   []Line   `json:"lines"`
	Notes   []string `json:"notes"` // why the plot is incomplete or cautioned; never nil
}

// BuildPlot assembles the series for metric against the parameter xKey. It refuses (nil and a reason) a metric whose runs
// declared different units: those numbers are not one quantity. The caller has checked that xKey is one of params.
func BuildPlot(params []Param, rows []Row, metric, xKey string) (*Plot, string) {
	var xp *Param
	var others []Param
	for i := range params {
		if params[i].Key == xKey {
			xp = &params[i]
		} else {
			others = append(others, params[i])
		}
	}
	if xp == nil {
		return nil, "that is not a parameter of this sweep"
	}
	var mi *MetricInfo
	for _, m := range Metrics(rows) {
		if m.Key == metric {
			m := m
			mi = &m
		}
	}
	if mi == nil {
		return nil, "no run of this sweep declares " + metric
	}
	if mi.UnitsDiffer {
		return nil, fmt.Sprintf("the runs declare %s in different units (%s); they are not one quantity, so no line is drawn", metric, strings.Join(mi.Units, ", "))
	}
	p := &Plot{Metric: metric, Unit: mi.Unit, XKey: xKey, XName: xp.Name(), Numeric: Numeric(xp.Values), XValues: xp.Values, Lines: []Line{}, Notes: []string{}}
	if mi.DefinitionsDiffer {
		p.Notes = append(p.Notes, metric+" is defined differently in some runs; compare the definitions before reading the lines together")
	}
	// one line per combination of the other parameters, in parameter order
	combos := []map[string]string{{}}
	if len(others) > 0 {
		combos, _ = Expand(others)
	}
	for _, c := range combos {
		ln := Line{Label: Label(others, c), Points: []Point{}}
		if len(others) == 0 {
			ln.Label = metric
		}
		for _, x := range xp.Values {
			pt := Point{X: x}
			if p.Numeric {
				f, _ := strconv.ParseFloat(x, 64)
				pt.XNum = &f
			}
			for _, r := range rows {
				if r.Values[xKey] != x || !matches(r.Values, c) {
					continue
				}
				pt.JobID = r.JobID
				for _, m := range r.Metrics {
					if m.Key == metric && m.Value != nil {
						pt.Value, pt.Uncertainty = m.Value, m.Uncertainty
					}
				}
			}
			ln.Points = append(ln.Points, pt)
		}
		p.Lines = append(p.Lines, ln)
	}
	return p, ""
}

func matches(values, c map[string]string) bool {
	for k, v := range c {
		if values[k] != v {
			return false
		}
	}
	return true
}
