// Package jobtemplate describes how Vesqira writes a Slurm job script for a tool (ADR 0068, item 4). A template is data inside a
// tool profile: where the code comes from, which config keys a person may change and what each accepts, the preparation steps,
// the one command that runs, and what to read from the log. It is declaration only. Rendering, quoting and validating values
// stay in the jobs package, and the template can reach the script only through a closed set of steps and placeholders, so no
// text of a profile or of a person is ever written into a script unquoted.
//
// A template is untrusted input. Compile checks every string that can land in a script or a config against the character set
// its place allows, and the profile file is read with unknown fields refused.
package jobtemplate

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Key kinds. The set is closed: a profile picks one, it cannot define one, so the characters a value may have are fixed here.
const (
	KindNumber = "number" // a decimal number, optionally with an exponent; range and bare words are optional
	KindInt    = "int"    // 1 to 9 digits, optional range
	KindPosInt = "posint" // a whole number, 1 or more
	KindEnum   = "enum"   // one of a list, and optionally a listed word followed by a number (gauss1.5)
	KindBool   = "bool"   // true or false
	KindPath   = "path"   // an absolute path without spaces, quotes, $ or ..
)

// Edit modes: how a changed key is written into the config text.
const (
	EditSection = "section" // the key is looked up in its own table only; a missing key is added to that table
	EditByName  = "by_name" // the key is found in any table (the tool reads keys by name), the result is read back and checked
)

// Step kinds, the whole vocabulary of preparation lines.
const (
	StepExport     = "export"         // export NAME=value
	StepSet        = "set"            // name=value, a variable later steps and the command can use
	StepAnnounce   = "announce"       // echo the job id, host and variables, then the date
	StepRequire    = "require"        // stop with a plain message when a command is not on PATH
	StepModules    = "modules"        // module purge and module load
	StepBuildStale = "build_if_stale" // make a target when a source is newer than the binary, under a lock
	StepComment    = "comment"        // a line of explanation in the script
	StepUlimit     = "ulimit"         // ulimit -<flag> <value>
)

// Probes a template may declare for the environment record (ADR 0054).
const ProbeJulia = "julia"

// Progress readers a template may name.
const ReaderChunks = "vgt_chunks" // the Julia pipeline's "Done chunk" blocks (internal/vgtlog)

// Template is one tool's job template.
type Template struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Tool is the tool's plain name inside sentences ("VGT", "ROHSA").
	Tool   string `json:"tool"`
	About  string `json:"about"`
	Repo   Repo   `json:"repo"`
	Config Config `json:"config"`
	Keys   []Key  `json:"keys"`
	Ties   []Tie  `json:"ties,omitempty"`
	// SweepKey is the setting a new sweep row starts on: one of the declared keys that a sweep may vary (not the output folder, not a
	// path). Empty means the first such key. It is a starting choice, not a limit.
	SweepKey  string    `json:"sweepKey,omitempty"`
	Steps     []Step    `json:"steps"`
	Command   []string  `json:"command"`
	Progress  *Progress `json:"progress,omitempty"`
	Resources Resources `json:"resources"`
	Probes    []string  `json:"probes,omitempty"`
	// Sizing names a built-in sizing table for the request, shown beside it and never applied ("rohsa_crop_table").
	Sizing string `json:"sizing,omitempty"`

	keys map[string]*Key
	ties map[string]*Tie
}

// Repo says where the tool's code comes from. Nothing here is fetched by Vesqira: the person points at a checkout on the host.
// The commit is the revision the template was written against; a job is stopped on any other revision only when the person asks
// for that (the strict-rerun check, ADR 0054).
type Repo struct {
	URL    string `json:"url"`
	Commit string `json:"commit"`
	// FolderLabel and Placeholder are the form's words for the checkout; Refusal is the sentence for a path that is not plain.
	FolderLabel string `json:"folderLabel"`
	Placeholder string `json:"placeholder"`
	Hint        string `json:"hint"`
	Refusal     string `json:"refusal"`
	// Verified says what was checked against the commit, in one line, so a reader knows how far the declaration is trusted.
	Verified string `json:"verified"`
}

// Config describes the tool's config file.
type Config struct {
	// Var is the shell variable that holds the path of the config the script writes into the job directory.
	Var string `json:"var"`
	// InputKey is the key of the [input] table that names the input cube.
	InputKey string `json:"inputKey"`
	Edit     string `json:"edit"`
	// Required: a job starts from a config the person names (the tool has no usable default config).
	Required    bool   `json:"required"`
	Label       string `json:"label"`
	Placeholder string `json:"placeholder"`
	// AllTables: every top-level entry of the file must be a table, as the tool itself requires.
	AllTables bool `json:"allTables,omitempty"`
	// Guard refuses edits to a config where Table.Key is true: the tool runs its own sweep from it and Vesqira does not edit its axes.
	Guard *Guard `json:"guard,omitempty"`
}

type Guard struct {
	Table   string `json:"table"`
	Key     string `json:"key"`
	Refusal string `json:"refusal"`
}

// Key is one editable setting of the tool's config.
type Key struct {
	Name    string `json:"name"`
	Section string `json:"section"`
	Kind    string `json:"kind"`
	Label   string `json:"label"`
	// Description says in plain words what the key does, taken from the tool's own documentation.
	Description string `json:"description"`
	// Expect completes "<key> must be ..." in a refusal and doubles as the form's hint.
	Expect string   `json:"expect"`
	Enum   []string `json:"enum,omitempty"`
	// EnumNumeric lists words that may be followed by a non-negative number: "gauss" admits gauss1 and gauss1.5.
	EnumNumeric []string `json:"enumNumeric,omitempty"`
	// Words are bare words a number key also accepts, written quoted ("auto").
	Words []string `json:"words,omitempty"`
	Min   *float64 `json:"min,omitempty"`
	Max   *float64 `json:"max,omitempty"`
	// Real writes a whole number as a real (10 as 10.0), as the tool's own configs do. List writes a count as a one-element list.
	Real       bool `json:"real,omitempty"`
	List       bool `json:"list,omitempty"`
	NoExponent bool `json:"noExponent,omitempty"`
	// Internal keys are set by Vesqira per job (the output folder) and never by a request.
	Internal bool     `json:"internal,omitempty"`
	Default  *Default `json:"default,omitempty"`
}

// Default is the value the tool's own documentation gives a key, kept with where it was read. The form shows it as a hint; it is
// never written into a config the person did not change.
type Default struct {
	Value  string `json:"value"`
	Reason string `json:"reason"`
}

// Tie is one setting that stands for several keys moved together; sweeping it is one parameter, not several.
type Tie struct {
	Name   string   `json:"name"`
	Label  string   `json:"label"`
	Keys   []string `json:"keys"`
	Reason string   `json:"reason"`
}

// Step is one preparation line of the script. Which fields apply depends on Kind; Compile refuses the others.
type Step struct {
	Kind string `json:"kind"`
	// export, set, ulimit
	Name      string `json:"name,omitempty"`
	Value     string `json:"value,omitempty"`
	KeepIfSet bool   `json:"keepIfSet,omitempty"`
	// require: the command and the name shown in the message
	Command string `json:"command,omitempty"`
	Title   string `json:"title,omitempty"`
	// modules
	Modules []string `json:"modules,omitempty"`
	Purge   bool     `json:"purge,omitempty"`
	// build_if_stale
	Var     string   `json:"var,omitempty"`
	Dir     string   `json:"dir,omitempty"`
	Binary  string   `json:"binary,omitempty"`
	Target  []string `json:"target,omitempty"`
	Sources []string `json:"sources,omitempty"`
	// announce
	Show []Shown `json:"show,omitempty"`
	// comment, and a plain sentence for the form on any step
	Text string `json:"text,omitempty"`
}

type Shown struct {
	Label string `json:"label"`
	Var   string `json:"var"`
}

// Progress says how to read progress from a job's log: a named reader of Vesqira's, or a pattern with the groups done and total.
type Progress struct {
	Reader  string `json:"reader,omitempty"`
	Pattern string `json:"pattern,omitempty"`
	re      *regexp.Regexp
}

// Regexp is the compiled pattern, or nil.
func (p *Progress) Regexp() *regexp.Regexp { return p.re }

// Resources are the form's starting request. They are Vesqira's convenience, not a property of the tool.
type Resources struct {
	CPUs   int    `json:"cpus"`
	Mem    string `json:"mem"`
	Time   string `json:"time"`
	Reason string `json:"reason"`
}

var (
	identRe    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
	upperRe    = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,59}$`)
	wordRe     = regexp.MustCompile(`^[A-Za-z0-9._-]{1,40}$`)
	literalRe  = regexp.MustCompile(`^[A-Za-z0-9._/+:=,@%-]+$`)
	moduleRe   = regexp.MustCompile(`^[A-Za-z0-9._/+-]{1,60}$`)
	relDirRe   = regexp.MustCompile(`^[A-Za-z0-9._+-]+(/[A-Za-z0-9._+-]+){0,8}$`)
	globRe     = regexp.MustCompile(`^[A-Za-z0-9._*-]{1,40}$`)
	urlRe      = regexp.MustCompile(`^https://[A-Za-z0-9.-]+/[A-Za-z0-9._/-]{1,200}$`)
	commitRe   = regexp.MustCompile(`^[0-9a-f]{40}$`)
	memRe      = regexp.MustCompile(`^[0-9]{1,4}[MG]$`)
	timeRe     = regexp.MustCompile(`^([0-9]{1,2}-)?[0-9]{1,2}:[0-9]{2}:[0-9]{2}$`)
	placeholRe = regexp.MustCompile(`\{\{([a-z_]+(?::[a-z][a-z0-9_]{0,29})?)\}\}`)
	numberRe   = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?([eE][-+]?[0-9]+)?$`)
	intRe      = regexp.MustCompile(`^[0-9]{1,9}$`)
	posIntRe   = regexp.MustCompile(`^[1-9][0-9]{0,8}$`)
	suffixRe   = regexp.MustCompile(`^[0-9]+(\.[0-9]+)?$`)
)

// deniedExports are variables that change how a shell or the loader behaves, so a template may not set them.
var deniedExports = map[string]bool{"PATH": true, "BASH_ENV": true, "ENV": true, "IFS": true, "SHELLOPTS": true, "BASHOPTS": true, "PS4": true,
	"PROMPT_COMMAND": true, "LD_PRELOAD": true, "LD_AUDIT": true, "CDPATH": true, "HOME": true, "SHELL": true, "GLOBIGNORE": true}

// Placeholders a value or an argument may use.
const (
	PlaceRepo   = "repo"   // the checkout's path, already checked
	PlaceCPUs   = "cpus"   // ${SLURM_CPUS_PER_TASK:-1}
	PlaceConfig = "config" // the config's path variable
	PlaceVar    = "var"    // {{var:name}}, a variable an earlier set step made
)

func plain(what, s string, max int, required bool) error {
	if s == "" {
		if required {
			return fmt.Errorf("%s is empty", what)
		}
		return nil
	}
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > max {
		return fmt.Errorf("%s is not valid text of at most %d characters", what, max)
	}
	for _, r := range s {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return fmt.Errorf("%s holds a control or format character", what)
		}
	}
	return nil
}

func fail(format string, a ...any) error { return fmt.Errorf(format, a...) }

// Compile checks the template and prepares it for use. It must be called once, before any other method.
func (t *Template) Compile() error {
	if t.Steps == nil {
		t.Steps = []Step{} // the form maps over it: an absent list is an empty one in the JSON too
	}
	if !identRe.MatchString(t.ID) || t.ID == "custom" || t.ID == "analysis" {
		return fail("the template id must be lowercase letters, digits and _, and not custom or analysis")
	}
	for _, f := range []struct {
		what, s string
		max     int
		req     bool
	}{{"title", t.Title, 80, true}, {"tool", t.Tool, 40, true}, {"about", t.About, 1200, true}, {"repo folder label", t.Repo.FolderLabel, 80, true},
		{"repo placeholder", t.Repo.Placeholder, 200, false}, {"repo hint", t.Repo.Hint, 600, false}, {"repo refusal", t.Repo.Refusal, 200, true},
		{"repo verified note", t.Repo.Verified, 400, true}, {"config label", t.Config.Label, 80, true}, {"config placeholder", t.Config.Placeholder, 200, false},
		{"resources reason", t.Resources.Reason, 300, true}} {
		if err := plain(f.what, f.s, f.max, f.req); err != nil {
			return err
		}
	}
	if !urlRe.MatchString(t.Repo.URL) {
		return fail("the repository URL must be an https address")
	}
	if !commitRe.MatchString(t.Repo.Commit) {
		return fail("the pinned commit must be a full 40-character SHA-1")
	}
	if !identRe.MatchString(t.Config.Var) || t.Config.Var == "jobdir" || strings.HasPrefix(t.Config.Var, "ve_") {
		return fail("the config variable must be a lowercase name that is not jobdir or ve_*")
	}
	if !identRe.MatchString(t.Config.InputKey) {
		return fail("the input key must be a lowercase key name")
	}
	if t.Config.Edit != EditSection && t.Config.Edit != EditByName {
		return fail("config edit must be %q or %q", EditSection, EditByName)
	}
	if g := t.Config.Guard; g != nil {
		if !identRe.MatchString(g.Table) || !identRe.MatchString(g.Key) {
			return fail("the config guard names a table and a key")
		}
		if err := plain("the guard refusal", g.Refusal, 300, true); err != nil {
			return err
		}
	}
	r := t.Resources
	if r.CPUs < 1 || r.CPUs > 256 || !memRe.MatchString(r.Mem) || !timeRe.MatchString(r.Time) {
		return fail("resources: cpus 1 to 256, memory like 64G, time like 1-00:00:00")
	}
	if err := t.compileKeys(); err != nil {
		return err
	}
	if err := t.compileSteps(); err != nil {
		return err
	}
	if err := t.compileCommand(); err != nil {
		return err
	}
	if p := t.Progress; p != nil {
		switch {
		case (p.Reader == "") == (p.Pattern == ""):
			return fail("progress names a reader or a pattern, not both and not neither")
		case p.Reader != "" && p.Reader != ReaderChunks:
			return fail("unknown progress reader %q", p.Reader)
		case p.Pattern != "":
			re, err := regexp.Compile(p.Pattern)
			if err != nil || len(p.Pattern) > 300 || re.SubexpIndex("done") < 0 || re.SubexpIndex("total") < 0 {
				return fail("a progress pattern must compile, be at most 300 characters and have the groups done and total")
			}
			p.re = re
		}
	}
	seen := map[string]bool{}
	for _, p := range t.Probes {
		if p != ProbeJulia || seen[p] {
			return fail("unknown or repeated probe %q", p)
		}
		seen[p] = true
	}
	if t.Sizing != "" && t.Sizing != "rohsa_crop_table" {
		return fail("unknown sizing table %q", t.Sizing)
	}
	return nil
}

func (t *Template) compileKeys() error {
	if len(t.Keys) == 0 || len(t.Keys) > 80 {
		return fail("a template lists 1 to 80 keys")
	}
	t.keys, t.ties = map[string]*Key{}, map[string]*Tie{}
	for i := range t.Keys {
		k := &t.Keys[i]
		if !identRe.MatchString(k.Name) || !identRe.MatchString(k.Section) {
			return fail("key %q: names are lowercase letters, digits and _", k.Name)
		}
		if t.keys[k.Name] != nil {
			return fail("key %q is listed twice", k.Name)
		}
		t.keys[k.Name] = k
		for _, f := range []struct {
			what, s string
			max     int
		}{{"label", k.Label, 120}, {"description", k.Description, 400}, {"expect", k.Expect, 160}} {
			if err := plain("key "+k.Name+" "+f.what, f.s, f.max, f.what != "expect"); err != nil {
				return err
			}
		}
		if k.Min != nil && k.Max != nil && *k.Min > *k.Max {
			return fail("key %q has a minimum above its maximum", k.Name)
		}
		switch k.Kind {
		case KindNumber, KindInt:
		case KindPosInt, KindBool, KindPath:
			if k.Min != nil || k.Max != nil {
				return fail("key %q: a range is for number and int keys", k.Name)
			}
		case KindEnum:
			if len(k.Enum) == 0 && len(k.EnumNumeric) == 0 {
				return fail("key %q: an enum needs words", k.Name)
			}
		default:
			return fail("key %q: unknown kind %q", k.Name, k.Kind)
		}
		for _, w := range append(append(append([]string{}, k.Enum...), k.EnumNumeric...), k.Words...) {
			if !wordRe.MatchString(w) {
				return fail("key %q: %q is not a plain word", k.Name, w)
			}
		}
		if (len(k.Enum) > 0 || len(k.EnumNumeric) > 0) && k.Kind != KindEnum || len(k.Words) > 0 && k.Kind != KindNumber {
			return fail("key %q: words do not fit its kind", k.Name)
		}
		if (k.Real && k.Kind != KindNumber) || (k.NoExponent && k.Kind != KindNumber) || (k.List && k.Kind != KindInt) {
			return fail("key %q: a flag does not fit its kind", k.Name)
		}
		if k.Internal && k.Kind != KindPath {
			return fail("key %q: only a path key is set by Vesqira", k.Name)
		}
	}
	for i := range t.Keys {
		k := &t.Keys[i]
		if d := k.Default; d != nil {
			if err := plain("key "+k.Name+" default reason", d.Reason, 200, true); err != nil {
				return err
			}
			if _, err := t.Check(k.Name, d.Value); err != nil {
				return fail("key %q: its default is not a value the key accepts", k.Name)
			}
		}
	}
	if t.SweepKey != "" {
		k := t.keys[t.SweepKey]
		if k == nil || k.Internal || k.Kind == "path" {
			return fail("sweepKey %q: not a declared key a sweep may vary (not the output folder, not a path)", t.SweepKey)
		}
	}
	for i := range t.Ties {
		tie := &t.Ties[i]
		if !identRe.MatchString(tie.Name) || t.keys[tie.Name] != nil || t.ties[tie.Name] != nil || len(tie.Keys) < 2 {
			return fail("tie %q: a new name and at least two keys", tie.Name)
		}
		if err := plain("tie label", tie.Label, 120, true); err != nil {
			return err
		}
		if err := plain("tie reason", tie.Reason, 300, true); err != nil {
			return err
		}
		for _, name := range tie.Keys {
			k := t.keys[name]
			if k == nil || k.Internal || k.Kind != t.keys[tie.Keys[0]].Kind {
				return fail("tie %q: %q is not an editable key of the same kind as the first", tie.Name, name)
			}
		}
		t.ties[tie.Name] = tie
	}
	return nil
}

func (t *Template) compileSteps() error {
	vars := map[string]bool{t.Config.Var: true}
	if len(t.Steps) > 40 {
		return fail("a template has at most 40 steps")
	}
	for i := range t.Steps {
		s := &t.Steps[i]
		where := fmt.Sprintf("step %d (%s)", i+1, s.Kind)
		used := stepFields(s)
		allowed := map[string][]string{
			StepExport:     {"name", "value", "keepIfSet"},
			StepSet:        {"name", "value"},
			StepAnnounce:   {"show"},
			StepRequire:    {"command", "title"},
			StepModules:    {"modules", "purge"},
			StepBuildStale: {"var", "dir", "binary", "target", "sources"},
			StepComment:    {"text"},
			StepUlimit:     {"name", "value"},
		}[s.Kind]
		if allowed == nil {
			return fail("%s: unknown step kind", where)
		}
		for f := range used {
			if !containsStr(allowed, f) {
				return fail("%s: field %q does not belong to this step", where, f)
			}
		}
		switch s.Kind {
		case StepExport, StepSet:
			if s.Kind == StepExport && (!upperRe.MatchString(s.Name) || deniedExports[s.Name]) {
				return fail("%s: %q is not a variable a template may export", where, s.Name)
			}
			if s.Kind == StepSet && (!identRe.MatchString(s.Name) || s.Name == "jobdir" || strings.HasPrefix(s.Name, "ve_") || vars[s.Name]) {
				return fail("%s: %q is not a new variable name", where, s.Name)
			}
			if s.KeepIfSet && s.Kind != StepExport {
				return fail("%s: keepIfSet is for export", where)
			}
			if _, err := t.expand(s.Value, vars, !s.KeepIfSet, ""); err != nil {
				return fail("%s: %v", where, err)
			}
			if s.Kind == StepSet {
				vars[s.Name] = true
			}
		case StepAnnounce:
			if len(s.Show) > 6 {
				return fail("%s: at most 6 values", where)
			}
			for _, sh := range s.Show {
				if !identRe.MatchString(sh.Label) || !vars[sh.Var] {
					return fail("%s: %q must name a variable made by an earlier step or the config", where, sh.Var)
				}
			}
		case StepRequire:
			if !wordRe.MatchString(s.Command) || plain("title", s.Title, 40, true) != nil || strings.ContainsAny(s.Title, `"$`+"`\\") {
				return fail("%s: a command name and a plain title", where)
			}
		case StepModules:
			if len(s.Modules) > 12 || len(s.Modules) == 0 && !s.Purge {
				return fail("%s: 1 to 12 modules, or purge", where)
			}
			for _, m := range s.Modules {
				if !moduleRe.MatchString(m) {
					return fail("%s: %q is not a module name", where, m)
				}
			}
		case StepBuildStale:
			if !identRe.MatchString(s.Var) || vars[s.Var] || !relDirRe.MatchString(s.Dir) || strings.Contains(s.Dir, "..") || !wordRe.MatchString(s.Binary) ||
				len(s.Target) == 0 || len(s.Target) > 4 || len(s.Sources) == 0 || len(s.Sources) > 6 {
				return fail("%s: a new variable, a relative folder, a binary name, up to 4 make targets and up to 6 source patterns", where)
			}
			for _, w := range append(append([]string{}, s.Target...), s.Sources...) {
				if !globRe.MatchString(w) {
					return fail("%s: %q is not a plain word or pattern", where, w)
				}
			}
			for _, w := range s.Target {
				if strings.Contains(w, "*") {
					return fail("%s: a make target is a plain word", where)
				}
			}
			vars[s.Var] = true
		case StepComment:
			if !commentRe.MatchString(s.Text) {
				return fail("%s: a comment is plain words, at most 160 characters", where)
			}
		case StepUlimit:
			if !ulimitFlagRe.MatchString(s.Name) || !(s.Value == "unlimited" || intRe.MatchString(s.Value)) {
				return fail("%s: a flag letter and unlimited or a number", where)
			}
		}
	}
	return nil
}

var ulimitFlagRe = regexp.MustCompile(`^[a-z]$`)

var commentRe = regexp.MustCompile(`^[A-Za-z0-9 ,.;:()_/'-]{1,160}$`)

func stepFields(s *Step) map[string]bool {
	m := map[string]bool{}
	set := func(name string, on bool) {
		if on {
			m[name] = true
		}
	}
	set("name", s.Name != "")
	set("value", s.Value != "")
	set("keepIfSet", s.KeepIfSet)
	set("command", s.Command != "")
	set("title", s.Title != "")
	set("modules", len(s.Modules) > 0)
	set("purge", s.Purge)
	set("var", s.Var != "")
	set("dir", s.Dir != "")
	set("binary", s.Binary != "")
	set("target", len(s.Target) > 0)
	set("sources", len(s.Sources) > 0)
	set("show", len(s.Show) > 0)
	set("text", s.Text != "")
	return m
}

func containsStr(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func (t *Template) compileCommand() error {
	if len(t.Command) < 2 || len(t.Command) > 24 || t.Command[0] != "srun" {
		return fail("the command is a list of words that starts with srun")
	}
	vars := map[string]bool{t.Config.Var: true}
	for _, s := range t.Steps {
		switch s.Kind {
		case StepSet:
			vars[s.Name] = true
		}
	}
	for _, a := range t.Command {
		if _, err := t.expand(a, vars, true, ""); err != nil {
			return fail("command: %v", err)
		}
	}
	return nil
}

// Expand renders a value or an argument. Literal text is written as it is (its characters are checked), each placeholder is
// written double-quoted when quote is set. repo is the checkout's path (already validated by the caller) and is only used when
// the placeholder appears.
func (t *Template) expand(s string, vars map[string]bool, quote bool, repo string) (string, error) {
	var b strings.Builder
	rest := s
	for {
		loc := placeholRe.FindStringSubmatchIndex(rest)
		if loc == nil {
			if rest != "" && (!literalRe.MatchString(rest) || strings.Contains(rest, "..")) {
				return "", fmt.Errorf("%q holds characters a script value may not have", rest)
			}
			b.WriteString(rest)
			break
		}
		if lit := rest[:loc[0]]; lit != "" {
			if !literalRe.MatchString(lit) || strings.Contains(lit, "..") {
				return "", fmt.Errorf("%q holds characters a script value may not have", lit)
			}
			b.WriteString(lit)
		}
		name, arg, _ := strings.Cut(rest[loc[2]:loc[3]], ":")
		var out string
		switch {
		case name == PlaceRepo && arg == "":
			out = repo
		case name == PlaceCPUs && arg == "":
			out = "${SLURM_CPUS_PER_TASK:-1}"
		case name == PlaceConfig && arg == "":
			out = "$" + t.Config.Var
		case name == PlaceVar && vars[arg]:
			out = "$" + arg
		default:
			return "", fmt.Errorf("placeholder {{%s}} is not known here", rest[loc[2]:loc[3]])
		}
		if quote {
			out = `"` + out + `"`
		}
		b.WriteString(out)
		rest = rest[loc[1]:]
	}
	return b.String(), nil
}

// Expand is the exported form used by the renderer: vars are the variables made so far.
func (t *Template) Expand(s string, vars map[string]bool, quote bool, repo string) (string, error) {
	return t.expand(s, vars, quote, repo)
}

// Key returns the key named name, or nil.
func (t *Template) Key(name string) *Key { return t.keys[name] }

// Tie returns the tie named name, or nil.
func (t *Template) Tie(name string) *Tie { return t.ties[name] }

// Editable lists the keys a request may set, in the template's order, the ties after them.
func (t *Template) Editable() []string {
	var out []string
	for i := range t.Keys {
		if !t.Keys[i].Internal {
			out = append(out, t.Keys[i].Name)
		}
	}
	for i := range t.Ties {
		out = append(out, t.Ties[i].Name)
	}
	return out
}

// HasProbe reports whether the template declares a probe.
func (t *Template) HasProbe(p string) bool { return containsStr(t.Probes, p) }

func (k *Key) rangeText() string {
	f := func(x float64) string { return strconv.FormatFloat(x, 'f', -1, 64) }
	switch {
	case k.Min != nil && k.Max != nil:
		return fmt.Sprintf("between %s and %s", f(*k.Min), f(*k.Max))
	case k.Min != nil:
		return "at least " + f(*k.Min)
	case k.Max != nil:
		return "at most " + f(*k.Max)
	}
	return ""
}

func (k *Key) inRange(v float64) bool {
	return (k.Min == nil || v >= *k.Min) && (k.Max == nil || v <= *k.Max)
}

// Check validates a value typed for key and returns it as TOML text. An internal key is not checkable here: Vesqira sets it.
func (t *Template) Check(name, val string) (string, error) {
	k := t.keys[name]
	if k == nil {
		return "", fmt.Errorf("%q is not an editable setting", name)
	}
	bad := func(what string) (string, error) { return "", fmt.Errorf("%s must be %s", name, what) }
	expect := func(def string) string {
		if k.Expect != "" {
			return k.Expect
		}
		return def
	}
	switch k.Kind {
	case KindNumber:
		if containsStr(k.Words, val) {
			return `"` + val + `"`, nil
		}
		if !numberRe.MatchString(val) || k.NoExponent && strings.ContainsAny(val, "eE") {
			return bad(expect("a number"))
		}
		if f, _ := strconv.ParseFloat(val, 64); !k.inRange(f) {
			return bad(k.rangeText())
		}
		if k.Real && !strings.ContainsAny(val, ".eE") {
			val += ".0" // the tool's configs write real values as reals (lambda_amp = 30.0)
		}
		return val, nil
	case KindInt:
		if !intRe.MatchString(val) {
			return bad(expect("a whole number"))
		}
		n, _ := strconv.Atoi(val)
		if !k.inRange(float64(n)) {
			return bad(k.rangeText())
		}
		if k.List {
			return "[" + strconv.Itoa(n) + "]", nil // some configs write a count as a list: n_gauss = [16]
		}
		return strconv.Itoa(n), nil
	case KindPosInt:
		if !posIntRe.MatchString(val) {
			return bad(expect("a whole number, 1 or more"))
		}
		return val, nil
	case KindBool:
		if val != "true" && val != "false" {
			return bad(expect("true or false"))
		}
		return val, nil
	case KindEnum:
		ok := containsStr(k.Enum, val)
		for _, w := range k.EnumNumeric {
			if rest, found := strings.CutPrefix(val, w); found && suffixRe.MatchString(rest) {
				ok = true
			}
		}
		if !ok {
			return bad(expect("one of " + strings.Join(k.Enum, ", ")))
		}
		return `"` + val + `"`, nil
	case KindPath:
		if !ValidPath(val) {
			return bad(expect("an absolute path without spaces or quotes"))
		}
		return `"` + val + `"`, nil
	}
	return "", fmt.Errorf("%q is not an editable setting", name)
}

var pathRe = regexp.MustCompile(`^/[A-Za-z0-9_./+-]{0,300}$`)

// ValidPath: no spaces, quotes, $ or backticks, so a path is safe inside a script; no .. and no trailing slash.
func ValidPath(p string) bool {
	return pathRe.MatchString(p) && !strings.Contains(p, "..") && !strings.HasSuffix(p, "/")
}
