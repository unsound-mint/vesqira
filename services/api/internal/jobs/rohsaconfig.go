package jobs

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/unsound-mint/vesqira/services/api/internal/jobtemplate"
)

// The ROHSA config (ADR 0046, 0048). A ROHSA run is configured by a TOML file of the pipeline (`configs/rohsa/<region>_<N>.toml`).
// The pipeline flattens every table of that file into one option list and rejects an unknown key, so a table name only groups
// keys for the reader; a setting is found by its key. Vesqira edits the config the way it edits a VGT config: the original text
// is kept line for line (comments, the order, keys it does not know) and only the lines of the settings that changed are
// replaced, then the result is decoded again and every changed key is checked to read back as asked. The text is embedded in the
// job script as a heredoc, so the script the owner reads is the config that runs.
//
// The editable keys, their ranges and the tied smoothing weights are the ROHSA-LMC profile's template (internal/runs/
// run_profiles.json). What stays here is the ROHSA-specific sizing table and the names the rest of the program has used.

// ROHSATied is one setting that stands for the three smoothing weights the owner always moves together
// (`smoothNNN` in the config names: lambda_amp = lambda_mu = lambda_sig). Sweeping it makes one parameter, not three.
const ROHSATied = "lambda"

func rohsaTemplate() *jobtemplate.Template { return ShippedTemplate("rohsa") }

// ROHSAExpand turns a set of requested settings into config keys: the tied "lambda" first, then every individual key over it.
func ROHSAExpand(values map[string]string) map[string]string { return Expand(rohsaTemplate(), values) }

// ROHSAOverlap reports whether two settings change a common config key ("lambda" and "lambda_amp" do).
func ROHSAOverlap(a, b string) bool { return Overlap(rohsaTemplate(), a, b) }

// ROHSAEditableKeys lists, sorted, what a request may set (the tied key included).
func ROHSAEditableKeys() []string { return EditableKeys(rohsaTemplate()) }

// ROHSALineKeys are the config keys a setting changes, for telling the expected lines of a script difference apart.
func ROHSALineKeys(key string) []string { return LineKeys(rohsaTemplate(), key) }

// CheckROHSASetting says whether value is acceptable for the editable ROHSA setting key.
func CheckROHSASetting(key, value string) error { return CheckKey(rohsaTemplate(), key, value) }

// SetROHSAConfig returns the config text with the requested settings applied and, when outputDir is not empty, the job's own
// output folder (see SetConfig).
func SetROHSAConfig(text string, values map[string]string, outputDir string) (string, error) {
	return SetConfig(rohsaTemplate(), text, values, outputDir)
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
	c, err := ReadConfig(rohsaTemplate(), text)
	if err != nil {
		return ROHSAConfig{}, err
	}
	cfg := ROHSAConfig{Values: c.Values, AutoConfig: c.Guarded}
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
