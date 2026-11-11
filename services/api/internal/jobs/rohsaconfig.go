package jobs

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// The ROHSA config (ADR 0046, 0048). A ROHSA run is configured by a TOML file of the pipeline (`configs/rohsa/<region>_<N>.toml`).
// The pipeline flattens every table of that file into one option list and rejects an unknown key, so a table name only
// groups keys for the reader; a setting is found by its key. Vesqira edits the config the way it edits a VGT config: the
// original text is kept line for line (comments, the order, keys it does not know) and only the lines of the settings
// that changed are replaced, then the result is decoded again and every changed key is checked to read back as asked. The
// text is embedded in the job script as a heredoc, so the script the owner reads is the config that runs.

type rohsaKey struct {
	Section  string // where a key the file lacks is added
	Kind     string // number | gb | int | bool | intlist | kernel | path
	Min, Max int    // int and intlist bounds
	Internal bool   // set by Vesqira per job, never by a request
}

// The settings a person may change. Chosen from the keys the pipeline's own configs vary (configs/rohsa/README): the
// number of Gaussians, the regularisation weights, the width bounds, the iteration limits, the noise switch, the spectral
// smoothing kernel and the crop. The input cube and the output folder are not here: the input is a file of the host's, and
// the output folder is Vesqira's per job so a repeated run never writes over an earlier one.
var rohsaKeys = map[string]rohsaKey{
	"n_gauss":        {"rohsa", "intlist", 1, 64, false},
	"lambda_amp":     {"rohsa", "number", 0, 0, false},
	"lambda_mu":      {"rohsa", "number", 0, 0, false},
	"lambda_sig":     {"rohsa", "number", 0, 0, false},
	"lambda_var_amp": {"rohsa", "number", 0, 0, false},
	"lambda_var_mu":  {"rohsa", "number", 0, 0, false},
	"lambda_var_sig": {"rohsa", "number", 0, 0, false},
	"sig_init":       {"rohsa", "number", 0, 0, false},
	"lb_sig_init":    {"rohsa", "number", 0, 0, false},
	"ub_sig_init":    {"rohsa", "number", 0, 0, false},
	"lb_sig":         {"rohsa", "number", 0, 0, false},
	"ub_sig":         {"rohsa", "number", 0, 0, false},
	"maxiter_init":   {"rohsa", "int", 1, 1000000, false},
	"maxiter":        {"rohsa", "int", 1, 1000000, false},
	"noise":          {"rohsa", "bool", 0, 0, false},
	// The pipeline's own memory preflight: it estimates what the fit needs and refuses to start when that is above this budget
	// (unless force_full_cube). A research setting of the config, not a credential; raising N can need it raised, together with
	// the job's Slurm memory (a separate request). A number (written as typed: 96 stays 96, not 96.0) or the word auto.
	"memory_gb":       {"input", "gb", 1, 100000, false},
	"spectral_kernel": {"input", "kernel", 0, 0, false},
	"crop_size":       {"input", "int", 0, 100000, false},
	"crop_x0":         {"input", "int", 0, 100000, false},
	"crop_y0":         {"input", "int", 0, 100000, false},
	"output_dir":      {"input", "path", 0, 0, true},
}

// ROHSATied is one setting that stands for the three smoothing weights the owner always moves together
// (`smoothNNN` in the config names: lambda_amp = lambda_mu = lambda_sig). Sweeping it makes one parameter, not three.
const ROHSATied = "lambda"

var rohsaTiedKeys = []string{"lambda_amp", "lambda_mu", "lambda_sig"}

var kernelRe = regexp.MustCompile(`^(none|hanning|gauss[0-9]+(\.[0-9]+)?)$`)

// ROHSAExpand turns a set of requested settings into config keys: the tied "lambda" first, then every individual key over it,
// so a set that names both (a base job's own lambda_amp under a swept lambda, say) always gives the same result, and an
// explicit lambda_amp beats the tie. Keys are not checked here.
func ROHSAExpand(values map[string]string) map[string]string {
	out := map[string]string{}
	if v, ok := values[ROHSATied]; ok {
		for _, t := range rohsaTiedKeys {
			out[t] = v
		}
	}
	for k, v := range values {
		if k != ROHSATied {
			out[k] = v
		}
	}
	return out
}

// ROHSAOverlap reports whether two settings change a common config key ("lambda" and "lambda_amp" do).
func ROHSAOverlap(a, b string) bool {
	for _, x := range ROHSALineKeys(a) {
		for _, y := range ROHSALineKeys(b) {
			if x == y {
				return true
			}
		}
	}
	return false
}

// ROHSAEditableKeys lists, sorted, what a request may set (the tied key included).
func ROHSAEditableKeys() []string {
	out := []string{ROHSATied}
	for k, spec := range rohsaKeys {
		if !spec.Internal {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// ROHSALineKeys are the config keys a setting changes, for telling the expected lines of a script difference apart.
func ROHSALineKeys(key string) []string {
	if key == ROHSATied {
		return rohsaTiedKeys
	}
	return []string{key}
}

// checkROHSAValue validates val for key and returns it as TOML text.
func checkROHSAValue(key, val string) (string, error) {
	spec := rohsaKeys[key]
	switch spec.Kind {
	case "number":
		if !numRe.MatchString(val) {
			return "", fmt.Errorf("%w: %s must be a number", ErrInvalid, key)
		}
		if !strings.ContainsAny(val, ".eE") {
			val += ".0" // the pipeline's configs write real values as reals (lambda_amp = 30.0)
		}
		return val, nil
	case "gb":
		if val == "auto" {
			return `"auto"`, nil
		}
		if !numRe.MatchString(val) || strings.ContainsAny(val, "eE") {
			return "", fmt.Errorf("%w: %s must be a number of GB (96 or 96.5) or auto", ErrInvalid, key)
		}
		if f, _ := strconv.ParseFloat(val, 64); f < float64(spec.Min) || f > float64(spec.Max) {
			return "", fmt.Errorf("%w: %s must be between %d and %d", ErrInvalid, key, spec.Min, spec.Max)
		}
		return val, nil
	case "int", "intlist":
		if !intRe.MatchString(val) {
			return "", fmt.Errorf("%w: %s must be a whole number", ErrInvalid, key)
		}
		n, _ := strconv.Atoi(val)
		if n < spec.Min || n > spec.Max {
			return "", fmt.Errorf("%w: %s must be between %d and %d", ErrInvalid, key, spec.Min, spec.Max)
		}
		if spec.Kind == "intlist" {
			return "[" + strconv.Itoa(n) + "]", nil // the pipeline's configs write the count as a list: n_gauss = [16]
		}
		return strconv.Itoa(n), nil
	case "bool":
		if val != "true" && val != "false" {
			return "", fmt.Errorf("%w: %s must be true or false", ErrInvalid, key)
		}
		return val, nil
	case "kernel":
		if !kernelRe.MatchString(val) {
			return "", fmt.Errorf("%w: %s must be none, hanning or gauss<sigma in channels> (gauss1, gauss1.5)", ErrInvalid, key)
		}
		return `"` + val + `"`, nil
	case "path":
		if !ValidPath(val) {
			return "", fmt.Errorf("%w: %s must be an absolute path without spaces or quotes", ErrInvalid, key)
		}
		return `"` + val + `"`, nil
	}
	return "", fmt.Errorf("%w: %q is not an editable setting", ErrInvalid, key)
}

// CheckROHSASetting says whether value is acceptable for the editable ROHSA setting key.
func CheckROHSASetting(key, value string) error {
	if key == ROHSATied {
		return CheckROHSASetting("lambda_amp", value)
	}
	if spec, ok := rohsaKeys[key]; !ok || spec.Internal {
		return fmt.Errorf("%w: %q is not an editable setting", ErrInvalid, key)
	}
	_, err := checkROHSAValue(key, value)
	return err
}

// ROHSAConfig is a ROHSA config as read: the settings Vesqira can edit, in the text form a person types them.
type ROHSAConfig struct {
	Values     map[string]string // only the editable keys the file has
	CropSize   int               // 0 is the whole cube, the pipeline's meaning
	HasCrop    bool
	NGauss     int // the first count; 0 when the file has none
	HasNGauss  bool
	AutoConfig bool // [auto_config] enabled: the pipeline runs its own candidate sweep
}

// ReadROHSAConfig decodes a ROHSA config and reports the editable settings. It applies the pipeline's own structure rule
// (every top-level entry is a table), so a file the pipeline would refuse is refused here before a job is drafted.
func ReadROHSAConfig(text string) (ROHSAConfig, error) {
	var raw map[string]any
	if _, err := toml.Decode(text, &raw); err != nil {
		return ROHSAConfig{}, fmt.Errorf("%w: the config is not valid TOML", ErrInvalid)
	}
	cfg := ROHSAConfig{Values: map[string]string{}}
	for name, v := range raw {
		tbl, ok := v.(map[string]any)
		if !ok {
			return ROHSAConfig{}, fmt.Errorf("%w: every top-level entry of a ROHSA config is a table; %q is not", ErrInvalid, name)
		}
		if name == "auto_config" {
			cfg.AutoConfig, _ = tbl["enabled"].(bool)
			continue
		}
		for k, x := range tbl {
			if spec, ok := rohsaKeys[k]; !ok || spec.Internal {
				continue
			}
			if s, ok := tomlText(x); ok {
				cfg.Values[k] = s
			}
		}
	}
	if s, ok := cfg.Values["crop_size"]; ok {
		cfg.CropSize, _ = strconv.Atoi(s)
		cfg.HasCrop = true
	}
	if s, ok := cfg.Values["n_gauss"]; ok {
		first, _, _ := strings.Cut(s, ",")
		cfg.NGauss, _ = strconv.Atoi(first)
		cfg.HasNGauss = cfg.NGauss > 0
	}
	return cfg, nil
}

// tomlText renders a decoded scalar (or a short list) the way a person types it: 10.0 is "10", true is "true", [16] is "16".
func tomlText(x any) (string, bool) {
	switch v := x.(type) {
	case int64:
		return strconv.FormatInt(v, 10), true
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(v), true
	case string:
		return v, true
	case []any:
		parts := make([]string, 0, len(v))
		for _, e := range v {
			s, ok := tomlText(e)
			if !ok {
				return "", false
			}
			parts = append(parts, s)
		}
		return strings.Join(parts, ","), true
	}
	return "", false
}

// sameROHSAValue compares what a key reads back as with what was asked, numerically for numbers.
func sameROHSAValue(a, b string) bool {
	if a == b {
		return true
	}
	fa, ea := strconv.ParseFloat(a, 64)
	fb, eb := strconv.ParseFloat(b, 64)
	return ea == nil && eb == nil && fa == fb
}

// setROHSALine replaces the line of key wherever the file has it outside [auto_config] (the pipeline reads a key by name, not
// by table, so a key a person put in another table is still the one that counts), or adds it to the conventional table.
func setROHSALine(lines []string, key, section, rendered string) []string {
	keyRe := regexp.MustCompile(`^\s*` + regexp.QuoteMeta(key) + `\s*=`)
	auto := false
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			auto = t == "[auto_config]"
			continue
		}
		if !auto && keyRe.MatchString(l) {
			lines[i] = key + " = " + rendered
			return lines
		}
	}
	return setLine(lines, section, key, rendered)
}

// SetROHSAConfig returns the config text with the requested settings applied and, when outputDir is not empty, the job's own
// output folder. Only the editable keys are accepted, each with a value of its own type; the output folder can be set only
// through outputDir, never through values. Lines the request does not touch stay byte for byte as they were; a replaced line
// loses its own trailing comment. The result is decoded again and every changed key must read back as asked, so a config
// whose layout this line editor cannot follow (a setting written over several lines) is refused, not guessed at.
func SetROHSAConfig(text string, values map[string]string, outputDir string) (string, error) {
	cfg, err := ReadROHSAConfig(text)
	if err != nil {
		return "", err
	}
	if cfg.AutoConfig && len(values) > 0 {
		return "", fmt.Errorf("%w: this config runs the pipeline's own candidate sweep ([auto_config] enabled), whose axes Vesqira does not edit; use a config without it", ErrInvalid)
	}
	for k := range values {
		if spec, ok := rohsaKeys[k]; k != ROHSATied && (!ok || spec.Internal) {
			return "", fmt.Errorf("%w: %q is not an editable setting", ErrInvalid, k)
		}
	}
	expanded := ROHSAExpand(values)
	keys := make([]string, 0, len(expanded))
	for k := range expanded {
		keys = append(keys, k)
	}
	sort.Strings(keys) // one order, so the same request always renders the same text and hash
	lines := strings.Split(text, "\n")
	want := map[string]string{}
	for _, k := range keys {
		rendered, err := checkROHSAValue(k, expanded[k])
		if err != nil {
			return "", err
		}
		lines = setROHSALine(lines, k, rohsaKeys[k].Section, rendered)
		want[k] = strings.Trim(rendered, `[]"`)
	}
	if outputDir != "" {
		rendered, err := checkROHSAValue("output_dir", outputDir)
		if err != nil {
			return "", err
		}
		lines = setROHSALine(lines, "output_dir", "input", rendered)
	}
	out := strings.Join(lines, "\n")
	back, err := ReadROHSAConfig(out)
	if err != nil {
		return "", fmt.Errorf("%w: the config could not be edited line by line (is a setting written over several lines?)", ErrInvalid)
	}
	for k, w := range want {
		if got, ok := back.Values[k]; !ok || !sameROHSAValue(got, w) {
			return "", fmt.Errorf("%w: %s did not read back as %s after editing (the config's layout is not one Vesqira can edit line by line)", ErrInvalid, k, w)
		}
	}
	return out, nil
}

// Sizing is a resource request suggested for a ROHSA run, with where the numbers come from. It is shown and never applied.
type Sizing struct {
	CropSize        int      `json:"cropSize"`
	NGauss          int      `json:"nGauss"`
	Assumed         []string `json:"assumed"` // inputs the config did not give, so the rule's own default was used
	CPUs            int      `json:"cpus"`
	Mem             string   `json:"mem"`
	Time            string   `json:"time"`
	ScriptPartition string   `json:"scriptPartition"` // what the pipeline's script submits to; a site setting, never applied here
	Basis           string   `json:"basis"`
}

// SuggestROHSASizing mirrors the table in the pipeline's hpc/irgetas/submit_rohsa.sh, case for case: a crop up to 512
// pixels, 1024, 2048 (more memory and time when n_gauss is above 6), else the whole cube. crop 0 means the whole cube there,
// as in the config; a missing crop_size reads as 0 and a missing n_gauss as 6, as the script does.
func SuggestROHSASizing(crop, n int, haveCrop, haveN bool) Sizing {
	z := Sizing{CropSize: crop, NGauss: n, Assumed: []string{}, ScriptPartition: "ZEN4"}
	if !haveCrop {
		z.CropSize = 0
		z.Assumed = append(z.Assumed, "crop_size (read as 0, the whole cube)")
	}
	if !haveN {
		z.NGauss = 6
		z.Assumed = append(z.Assumed, "n_gauss (read as 6)")
	}
	switch c := z.CropSize; {
	case c > 0 && c <= 512:
		z.CPUs, z.Mem, z.Time = 16, "12G", "03:00:00"
	case c > 0 && c <= 1024:
		z.CPUs, z.Mem, z.Time = 16, "40G", "12:00:00"
	case c > 0 && c <= 2048:
		z.CPUs = 32
		if z.NGauss <= 6 {
			z.Mem, z.Time = "100G", "1-00:00:00"
		} else {
			z.Mem, z.Time = "140G", "1-12:00:00"
		}
	default:
		z.CPUs, z.Mem, z.Time = 32, "180G", "3-00:00:00"
	}
	z.Basis = fmt.Sprintf("the sizing table of your pipeline's hpc/irgetas/submit_rohsa.sh (crop_size %d, n_gauss %d), a starting point from your own measurements; that script submits to partition %s. Nothing is applied until you choose it", z.CropSize, z.NGauss, z.ScriptPartition)
	return z
}

// SizingOf is the suggestion for a config's own crop and Gaussian count.
func (c ROHSAConfig) SizingOf() Sizing {
	return SuggestROHSASizing(c.CropSize, c.NGauss, c.HasCrop, c.HasNGauss)
}
