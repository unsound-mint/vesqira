package jobs

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/unsound-mint/vesqira/services/api/internal/jobtemplate"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
)

// Job templates as data (ADR 0068, slice 5). A tool's profile declares a jobtemplate.Template; this file renders its script and
// edits its config. A profile reaches the script only through the closed steps and placeholders of jobtemplate and the typed
// keys below, so a value or a word of a profile is either checked against a fixed character set or written double-quoted.

// ShippedTemplate is the job template of a tool Vesqira ships a profile for ("vgt", "rohsa"), or nil.
func ShippedTemplate(id string) *jobtemplate.Template { return runs.Shipped().TemplateByID(id) }

// Settings are what any template's script is rendered from.
type Settings struct {
	Name       string
	RepoDir    string // the tool's checkout on the host
	ConfigPath string // the config on the host the settings started from (recorded; the job runs Config)
	Config     string // the full config text, written into the job directory by the script
	CPUs       int
	Mem        string
	Time       string
	Partition  string
	Account    string
	// ExpectCommit makes the job stop before the program starts unless the code folder is at this revision with no changed
	// tracked file (a strict rerun, ADR 0054).
	ExpectCommit string
	// HashInput makes the job hash every byte of its input file before the program starts.
	HashInput bool
}

const configEnd = "VESQIRA_CONFIG_END"

// RenderTemplate renders the batch script for one run of a tool, following the template's steps. The person reads and submits
// exactly this text.
func RenderTemplate(t *jobtemplate.Template, p Settings) (string, error) {
	switch {
	case !nameRe.MatchString(p.Name):
		return "", fmt.Errorf("%w: the job name may use letters, digits, . _ - (up to 40)", ErrInvalid)
	case !ValidPath(p.RepoDir), p.ConfigPath != "" && !ValidPath(p.ConfigPath):
		return "", fmt.Errorf("%w: %s", ErrInvalid, t.Repo.Refusal)
	case p.CPUs < 1 || p.CPUs > 256:
		return "", fmt.Errorf("%w: CPUs must be between 1 and 256", ErrInvalid)
	case !memRe.MatchString(p.Mem):
		return "", fmt.Errorf("%w: memory looks like 64G or 4096M", ErrInvalid)
	case !timeRe.MatchString(p.Time):
		return "", fmt.Errorf("%w: time looks like 1-00:00:00 or 12:00:00", ErrInvalid)
	case p.Partition != "" && !slurmWord.MatchString(p.Partition), p.Account != "" && !slurmWord.MatchString(p.Account):
		return "", fmt.Errorf("%w: partition and account are plain words", ErrInvalid)
	case len(p.Config) == 0 || len(p.Config) > MaxScript/2 || strings.Contains(p.Config, configEnd) || strings.ContainsRune(p.Config, 0):
		return "", fmt.Errorf("%w: the config text is empty, too large or malformed", ErrInvalid)
	}
	cv := t.Config.Var
	var b strings.Builder
	b.WriteString("#!/bin/bash -l\n")
	fmt.Fprintf(&b, "#SBATCH --job-name=%s\n#SBATCH --nodes=1\n#SBATCH --ntasks=1\n#SBATCH --cpus-per-task=%d\n#SBATCH --mem=%s\n#SBATCH --time=%s\n", p.Name, p.CPUs, p.Mem, p.Time)
	if p.Partition != "" {
		fmt.Fprintf(&b, "#SBATCH --partition=%s\n", p.Partition)
	}
	if p.Account != "" {
		fmt.Fprintf(&b, "#SBATCH --account=%s\n", p.Account)
	}
	b.WriteString("#SBATCH --output=%x-%j.out\n#SBATCH --error=%x-%j.err\n\n")
	b.WriteString("# Rendered by Vesqira from the settings in the job record. This exact text is what is submitted.\n")
	b.WriteString("set -euo pipefail\n")
	fmt.Fprintf(&b, "jobdir=\"$SLURM_SUBMIT_DIR\"\n%s=\"$jobdir/config.toml\"\n", cv)
	fmt.Fprintf(&b, "cat > \"$%s\" <<'%s'\n%s\n%s\n\n", cv, configEnd, strings.TrimRight(p.Config, "\n"), configEnd)
	fmt.Fprintf(&b, "cd %s\n", p.RepoDir)
	vars := map[string]bool{cv: true}
	for i := range t.Steps {
		if err := writeStep(&b, t, &t.Steps[i], vars, p.RepoDir); err != nil {
			return "", fmt.Errorf("%w: %v", ErrInvalid, err)
		}
	}
	// after the steps: the environment recorded is the one the program starts in
	b.WriteString(EnvPreamble(envSpecFor(t, p.RepoDir, p.Config, p.ExpectCommit, p.HashInput)))
	args := make([]string, 0, len(t.Command))
	for _, a := range t.Command {
		x, err := t.Expand(a, vars, true, p.RepoDir)
		if err != nil {
			return "", fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		args = append(args, x)
	}
	b.WriteString(strings.Join(args, " ") + "\ndate --iso-8601=seconds\n")
	return b.String(), nil
}

func writeStep(b *strings.Builder, t *jobtemplate.Template, s *jobtemplate.Step, vars map[string]bool, repo string) error {
	switch s.Kind {
	case jobtemplate.StepExport:
		v, err := t.Expand(s.Value, vars, !s.KeepIfSet, repo)
		if err != nil {
			return err
		}
		if s.KeepIfSet {
			fmt.Fprintf(b, "export %s=\"${%s:-%s}\"\n", s.Name, s.Name, v)
		} else {
			fmt.Fprintf(b, "export %s=%s\n", s.Name, v)
		}
	case jobtemplate.StepSet:
		v, err := t.Expand(s.Value, vars, true, repo)
		if err != nil {
			return err
		}
		fmt.Fprintf(b, "%s=%s\n", s.Name, v)
		vars[s.Name] = true
	case jobtemplate.StepAnnounce:
		b.WriteString(`echo "job_id=$SLURM_JOB_ID host=$(hostname)`)
		for _, sh := range s.Show {
			fmt.Fprintf(b, " %s=$%s", sh.Label, sh.Var)
		}
		b.WriteString("\"\ndate --iso-8601=seconds\n")
	case jobtemplate.StepRequire:
		fmt.Fprintf(b, "command -v %s >/dev/null || { echo \"%s is not on PATH\" >&2; exit 2; }\n", s.Command, s.Title)
	case jobtemplate.StepModules:
		if s.Purge {
			b.WriteString("module purge\n")
		}
		for _, m := range s.Modules {
			fmt.Fprintf(b, "module load %s\n", m)
		}
	case jobtemplate.StepBuildStale:
		names := make([]string, len(s.Sources))
		for i, w := range s.Sources {
			names[i] = "-name " + w
			if strings.Contains(w, "*") {
				names[i] = `-name "` + w + `"`
			}
		}
		fmt.Fprintf(b, "%s=%s\n", s.Var, s.Dir)
		b.WriteString("# rebuild only when a source is newer than the binary; jobs share this tree, so take the lock\n")
		fmt.Fprintf(b, "flock \"$%s/.build.lock\" bash -c '\n", s.Var)
		fmt.Fprintf(b, "    if [ ! -x \"$1/%s\" ] || [ -n \"$(find \"$1\" -maxdepth 1 \\( %s \\) -newer \"$1/%s\")\" ]; then\n", s.Binary, strings.Join(names, " -o "), s.Binary)
		fmt.Fprintf(b, "        make -C \"$1\" %s\n    fi' _ \"$%s\"\n", strings.Join(s.Target, " "), s.Var)
		vars[s.Var] = true
	case jobtemplate.StepComment:
		fmt.Fprintf(b, "# %s\n", s.Text)
	case jobtemplate.StepUlimit:
		fmt.Fprintf(b, "ulimit -%s %s\n", s.Name, s.Value)
	default:
		return fmt.Errorf("unknown step %q", s.Kind)
	}
	return nil
}

// envSpecFor reads the output folder and the input file out of the config text the job embeds ([input] output_dir and the
// template's input key), so the preamble describes the files this very script will use.
func envSpecFor(t *jobtemplate.Template, repoDir, config, expect string, hashInput bool) EnvSpec {
	_, out := ConfigPaths(config)
	in := InputPathFor(t, config)
	return EnvSpec{RepoDir: repoDir, OutputDir: out, InputFile: in, ExpectCommit: expect, HashInput: hashInput, NoJulia: !t.HasProbe(jobtemplate.ProbeJulia)}
}

// ---- editing a config: only the template's typed keys ----

// Config is a config as read: the settings Vesqira can edit, in the text form a person types them.
type Config struct {
	Values  map[string]string // only the editable keys the file has
	Guarded bool              // the template's guard table says the tool runs its own sweep from this config
}

// ReadConfig decodes a config and reports the editable settings. When the template says every top-level entry is a table it
// applies that rule, so a file the tool would refuse is refused here before a job is drafted.
func ReadConfig(t *jobtemplate.Template, text string) (Config, error) {
	var raw map[string]any
	if _, err := toml.Decode(text, &raw); err != nil {
		return Config{}, fmt.Errorf("%w: the config is not valid TOML", ErrInvalid)
	}
	cfg := Config{Values: map[string]string{}}
	for name, v := range raw {
		tbl, ok := v.(map[string]any)
		if !ok {
			if t.Config.AllTables {
				return Config{}, fmt.Errorf("%w: every top-level entry of a %s config is a table; %q is not", ErrInvalid, t.Tool, name)
			}
			continue
		}
		if g := t.Config.Guard; g != nil && name == g.Table {
			cfg.Guarded, _ = tbl[g.Key].(bool)
			continue
		}
		for k, x := range tbl {
			if spec := t.Key(k); spec == nil || spec.Internal {
				continue
			}
			if s, ok := tomlText(x); ok {
				cfg.Values[k] = s
			}
		}
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

// sameValue compares what a key reads back as with what was asked, numerically for numbers.
func sameValue(a, b string) bool {
	if a == b {
		return true
	}
	fa, ea := strconv.ParseFloat(a, 64)
	fb, eb := strconv.ParseFloat(b, 64)
	return ea == nil && eb == nil && fa == fb
}

// Expand turns a set of requested settings into config keys: a tie first, then every individual key over it, so a set that names
// both always gives the same result and an explicit key beats the tie. Keys are not checked here.
func Expand(t *jobtemplate.Template, values map[string]string) map[string]string {
	out := map[string]string{}
	for name, v := range values {
		if tie := t.Tie(name); tie != nil {
			for _, k := range tie.Keys {
				out[k] = v
			}
		}
	}
	for k, v := range values {
		if t.Tie(k) == nil {
			out[k] = v
		}
	}
	return out
}

// LineKeys are the config keys a setting changes, for telling the expected lines of a script difference apart.
func LineKeys(t *jobtemplate.Template, key string) []string {
	if tie := t.Tie(key); tie != nil {
		return tie.Keys
	}
	return []string{key}
}

// Overlap reports whether two settings change a common config key (a tie and one of its keys do).
func Overlap(t *jobtemplate.Template, a, b string) bool {
	for _, x := range LineKeys(t, a) {
		for _, y := range LineKeys(t, b) {
			if x == y {
				return true
			}
		}
	}
	return false
}

// EditableKeys lists, sorted, what a request may set (a tie included).
func EditableKeys(t *jobtemplate.Template) []string {
	out := t.Editable()
	sort.Strings(out)
	return out
}

// CheckKey says whether value is acceptable for the editable setting key (a tie is checked as its first key).
func CheckKey(t *jobtemplate.Template, key, value string) error {
	if tie := t.Tie(key); tie != nil {
		key = tie.Keys[0]
	}
	if k := t.Key(key); k == nil || k.Internal {
		return fmt.Errorf("%w: %q is not an editable setting", ErrInvalid, key)
	}
	if _, err := t.Check(key, value); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return nil
}

// KindOf is the kind of an editable setting ("number", "int", "enum", "path", ...), and false for a key a request may not set.
func KindOf(t *jobtemplate.Template, key string) (string, bool) {
	if tie := t.Tie(key); tie != nil {
		key = tie.Keys[0]
	}
	k := t.Key(key)
	if k == nil || k.Internal {
		return "", false
	}
	return k.Kind, true
}

// setByName replaces the line of key wherever the file has it outside the guard table (the tool reads a key by name, not by
// table, so a key a person put in another table is still the one that counts), or adds it to the conventional table.
func setByName(lines []string, guard, key, section, rendered string) []string {
	keyRe := keyLineRe(key)
	skip := false
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			skip = guard != "" && t == "["+guard+"]"
			continue
		}
		if !skip && keyRe.MatchString(l) {
			lines[i] = key + " = " + rendered
			return lines
		}
	}
	return setLine(lines, section, key, rendered)
}

// SetConfig returns the config text with the requested settings applied and, when outputDir is not empty, the job's own output
// folder. Only the template's editable keys are accepted, each with a value of its own type; the output folder can be set only
// through outputDir, never through values. Keys are applied in sorted order, so the same request always renders the same text.
// In by_name mode lines the request does not touch stay byte for byte as they were, a replaced line loses its own trailing
// comment, and the result is decoded again with every changed key required to read back as asked, so a config whose layout this
// line editor cannot follow (a setting written over several lines) is refused, not guessed at.
func SetConfig(t *jobtemplate.Template, text string, values map[string]string, outputDir string) (string, error) {
	byName := t.Config.Edit == jobtemplate.EditByName
	guard := ""
	if g := t.Config.Guard; g != nil {
		guard = g.Table
	}
	if byName {
		cfg, err := ReadConfig(t, text)
		if err != nil {
			return "", err
		}
		if cfg.Guarded && len(values) > 0 {
			return "", fmt.Errorf("%w: %s", ErrInvalid, t.Config.Guard.Refusal)
		}
	}
	for k := range values {
		if spec := t.Key(k); t.Tie(k) == nil && (spec == nil || spec.Internal) {
			return "", fmt.Errorf("%w: %q is not an editable setting", ErrInvalid, k)
		}
	}
	expanded := Expand(t, values)
	keys := make([]string, 0, len(expanded))
	for k := range expanded {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := strings.Split(text, "\n")
	want := map[string]string{}
	apply := func(k, val string) error {
		rendered, err := t.Check(k, val)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrInvalid, err)
		}
		if byName {
			lines = setByName(lines, guard, k, t.Key(k).Section, rendered)
		} else {
			lines = setLine(lines, t.Key(k).Section, k, rendered)
		}
		want[k] = strings.Trim(rendered, `[]"`)
		return nil
	}
	for _, k := range keys {
		if err := apply(k, expanded[k]); err != nil {
			return "", err
		}
	}
	if outputDir != "" {
		k := t.Key("output_dir")
		if k == nil {
			return "", fmt.Errorf("%w: this template has no output folder to set", ErrInvalid)
		}
		if err := apply("output_dir", outputDir); err != nil {
			return "", err
		}
		delete(want, "output_dir") // a path is read back by the input and output readers, not by the settings
	}
	out := strings.Join(lines, "\n")
	if !byName {
		return out, nil
	}
	back, err := ReadConfig(t, out)
	if err != nil {
		return "", fmt.Errorf("%w: the config could not be edited line by line (is a setting written over several lines?)", ErrInvalid)
	}
	for k, w := range want {
		if got, ok := back.Values[k]; !ok || !sameValue(got, w) {
			return "", fmt.Errorf("%w: %s did not read back as %s after editing (the config's layout is not one Vesqira can edit line by line)", ErrInvalid, k, w)
		}
	}
	return out, nil
}

// InputKeyOf is the config key that names the input cube ("input_file" in a VGT config, "input" in a ROHSA config).
func InputKeyOf(t *jobtemplate.Template) string { return t.Config.InputKey }

// InputPathFor is the input cube path a config names, or "" when it names none that is a string.
func InputPathFor(t *jobtemplate.Template, config string) string {
	var cfg struct {
		Input map[string]any `toml:"input"`
	}
	if _, err := toml.Decode(config, &cfg); err != nil {
		return ""
	}
	s, _ := cfg.Input[t.Config.InputKey].(string)
	return s
}

// SetInputPath returns config text with the input path replaced and nothing else changed (the other lines stay byte for byte).
// The result is read back and must name the path, so a layout this line editor cannot follow is refused, not guessed at.
func SetInputPath(t *jobtemplate.Template, text, path string) (string, error) {
	if !ValidPath(path) {
		return "", fmt.Errorf("%w: the input file must be an absolute path without spaces or quotes", ErrInvalid)
	}
	lines := strings.Split(text, "\n")
	rendered := `"` + path + `"`
	if t.Config.Edit == jobtemplate.EditByName {
		guard := ""
		if g := t.Config.Guard; g != nil {
			guard = g.Table
		}
		lines = setByName(lines, guard, t.Config.InputKey, "input", rendered)
	} else {
		lines = setLine(lines, "input", t.Config.InputKey, rendered)
	}
	out := strings.Join(lines, "\n")
	if InputPathFor(t, out) != path {
		return "", fmt.Errorf("%w: the input line of the config could not be replaced (is it written over several lines?)", ErrInvalid)
	}
	return out, nil
}
