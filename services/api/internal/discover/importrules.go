package discover

import (
	"bytes"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Researcher import rules (ADR 0058). A rule maps a pattern on a file's path, relative to the project folder, to a role and
// to declarations about the file's content. Rules never change what the built-in rules found and never reach a file they
// skip: they classify only files the walk already listed (never-read names, skipped folders, links and archives are
// decided before any rule sees a name). A rule's declarations are the researcher's, labelled as declared by that rule.

// Rule roles.
const (
	RoleDataset     = "dataset"     // a FITS image registered as a dataset; may declare a brightness unit
	RoleOrientation = "orientation" // an axial orientation map: angle unit required, frame and quantity optional
	RoleCatalog     = "catalog"     // a point catalog: columns, coordinate format and frame declared
	RoleMask        = "mask"        // a FITS image that is a typed mask of another dataset (ADR 0055): type, meaning of nonzero and target declared
	RoleIgnore      = "ignore"      // listed, never offered for registration
)

var ruleRoles = map[string]bool{RoleDataset: true, RoleOrientation: true, RoleCatalog: true, RoleMask: true, RoleIgnore: true}

// Rule limits: a rule file is untrusted text.
const (
	MaxRules       = 100
	MaxPatternLen  = 300
	MaxRuleFields  = 8 // value columns of a catalog
	MaxFieldLength = 64
)

var ruleName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 ._-]{0,39}$`)

// Rule is one researcher rule. The declaration fields are flat so the rule is one TOML table; which of them a role accepts
// is checked by Validate.
type Rule struct {
	Name    string `json:"name" toml:"name"`
	Kind    string `json:"kind" toml:"kind"` // glob | regex
	Pattern string `json:"pattern" toml:"pattern"`
	Role    string `json:"role" toml:"role"`

	// dataset: the unit the files are declared to have (a file's own BUNIT always wins)
	Unit string `json:"unit,omitempty" toml:"unit,omitempty"`
	// orientation: angle unit (deg|rad, required), angle frame (pixel|sky) and quantity (the vocabulary of ADR 0042)
	AngleUnit  string `json:"angleUnit,omitempty" toml:"angle_unit,omitempty"`
	AngleFrame string `json:"angleFrame,omitempty" toml:"angle_frame,omitempty"`
	Quantity   string `json:"quantity,omitempty" toml:"quantity,omitempty"`
	// catalog: longitude and latitude columns, how they are written, their frame (and equinox for fk4/fk5); optionally
	// the angle column (with AngleUnit and Quantity), its uncertainty column and up to eight value columns
	Lon         string   `json:"lon,omitempty" toml:"lon,omitempty"`
	Lat         string   `json:"lat,omitempty" toml:"lat,omitempty"`
	CoordFormat string   `json:"coordFormat,omitempty" toml:"coord_format,omitempty"`
	CoordFrame  string   `json:"coordFrame,omitempty" toml:"coord_frame,omitempty"`
	Equinox     string   `json:"equinox,omitempty" toml:"equinox,omitempty"`
	AngleColumn string   `json:"angleColumn,omitempty" toml:"angle_column,omitempty"`
	SigmaColumn string   `json:"sigmaColumn,omitempty" toml:"sigma_column,omitempty"`
	Values      []string `json:"values,omitempty" toml:"values,omitempty"`

	// mask: what the files flag (MaskType, required), what a nonzero pixel of the file means (MaskNonzero, flagged|valid,
	// required: never assumed), a description (MaskMeaning, required for type other), and which dataset the mask belongs to,
	// found from the mask file's own path: MaskTarget is a project-relative path with {dir} (the mask's folder) and {stem}
	// (its file name without extension and without MaskStrip), for example "{dir}/{stem}.fits" with MaskStrip "_mask".
	MaskType    string `json:"maskType,omitempty" toml:"mask_type,omitempty"`
	MaskNonzero string `json:"maskNonzero,omitempty" toml:"mask_nonzero,omitempty"`
	MaskMeaning string `json:"maskMeaning,omitempty" toml:"mask_meaning,omitempty"`
	MaskTarget  string `json:"maskTarget,omitempty" toml:"mask_target,omitempty"`
	MaskStrip   string `json:"maskStrip,omitempty" toml:"mask_strip,omitempty"`
}

// DeclaredBy is the author label a rule's declarations carry in the catalog. It reads "rule:<name>", which no person, adapter
// or analysis uses.
func (r Rule) DeclaredBy() string { return RulePrefix + r.Name }

// RulePrefix starts the declared-by label of every declaration a researcher rule made.
const RulePrefix = "rule:"

// RuleName returns the rule's name when by is a rule label.
func RuleName(by string) (string, bool) { return strings.CutPrefix(by, RulePrefix) }

// MaxMaskMeaning bounds a mask's description (the same bound the mask route applies).
const MaxMaskMeaning = 300

// maskTypes are the types a rule may declare: the one type left out, `missing`, is computed from the data and never declared.
var maskTypes = map[string]bool{"low_snr": true, "saturated": true, "invalid": true, "user_excluded": true, "other": true}

var (
	angleUnits   = map[string]bool{"deg": true, "rad": true}
	angleFrames  = map[string]bool{"pixel": true, "sky": true}
	quantities   = map[string]bool{"b_field": true, "gradient": true, "dust_polarization": true, "starlight_polarization": true, "unspecified": true}
	coordFormats = map[string]bool{"degrees": true, "hours_degrees": true, "sexagesimal_hours_degrees": true, "sexagesimal_degrees": true}
	coordFrames  = map[string]bool{"icrs": true, "fk5": true, "fk4": true, "galactic": true}
)

// Validate lists every problem with the rule (empty: usable). It checks the pattern compiles and the declarations fit the
// role; the unit text and the catalog columns are checked again where they are applied.
func (r Rule) Validate() []string {
	var bad []string
	add := func(s string, a ...any) { bad = append(bad, fmt.Sprintf(s, a...)) }
	if !ruleName.MatchString(r.Name) {
		add("name: 1 to 40 letters, digits, blanks, dots, dashes or underscores, starting with a letter or digit")
	}
	if _, err := r.Matcher(); err != nil {
		add("pattern: %v", err)
	}
	if !ruleRoles[r.Role] {
		add("role must be dataset, orientation, catalog, mask or ignore")
		return bad
	}
	type f struct{ name, val string }
	onlyFor := func(role string, fs ...f) {
		if r.Role == role {
			return
		}
		for _, x := range fs {
			if x.val != "" {
				add("%s is a declaration for a %s rule, not a %s rule", x.name, role, r.Role)
			}
		}
	}
	onlyFor(RoleDataset, f{"unit", r.Unit})
	onlyFor(RoleOrientation, f{"angle frame", r.AngleFrame})
	onlyFor(RoleMask, f{"mask type", r.MaskType}, f{"mask nonzero", r.MaskNonzero}, f{"mask description", r.MaskMeaning}, f{"mask target", r.MaskTarget}, f{"mask strip", r.MaskStrip})
	onlyFor(RoleCatalog, f{"lon", r.Lon}, f{"lat", r.Lat}, f{"coordinate format", r.CoordFormat}, f{"coordinate frame", r.CoordFrame},
		f{"equinox", r.Equinox}, f{"angle column", r.AngleColumn}, f{"sigma column", r.SigmaColumn})
	if r.Role != RoleCatalog && len(r.Values) > 0 {
		add("values are declarations for a catalog rule, not a %s rule", r.Role)
	}
	if r.Role != RoleOrientation && r.Role != RoleCatalog {
		if r.AngleUnit != "" || r.Quantity != "" {
			add("angle unit and quantity are declarations for an orientation or catalog rule, not a %s rule", r.Role)
		}
	}
	if len([]rune(r.MaskMeaning)) > MaxMaskMeaning || strings.ContainsAny(r.MaskMeaning, "\x00\n\r") {
		add("mask description is longer than %d characters or has a control character", MaxMaskMeaning)
	}
	checked := []f{{"mask strip", r.MaskStrip}, {"unit", r.Unit}, {"lon", r.Lon}, {"lat", r.Lat}, {"angle column", r.AngleColumn}, {"sigma column", r.SigmaColumn}}
	for _, v := range r.Values {
		checked = append(checked, f{"value column", v})
	}
	for _, x := range checked {
		if len(x.val) > MaxFieldLength || strings.ContainsAny(x.val, "\x00\n\r") {
			add("%s is too long or has a control character", x.name)
		}
	}
	switch r.Role {
	case RoleDataset:
		if r.Unit != "" && (strings.TrimSpace(r.Unit) != r.Unit || strings.Contains(r.Unit, "  ")) {
			add("unit has a leading, trailing or repeated blank")
		}
	case RoleOrientation:
		if !angleUnits[r.AngleUnit] {
			add("angle unit is required and must be deg or rad: Vesqira does not guess it from the values")
		}
		if r.AngleFrame != "" && !angleFrames[r.AngleFrame] {
			add("angle frame must be pixel or sky, or left undeclared")
		}
		if r.Quantity != "" && !quantities[r.Quantity] {
			add("quantity must be b_field, gradient, dust_polarization, starlight_polarization or unspecified, or left undeclared")
		}
	case RoleCatalog:
		if r.Lon == "" || r.Lat == "" {
			add("a catalog rule names the longitude and latitude columns")
		}
		if !coordFormats[r.CoordFormat] {
			add("coordinate format must be degrees, hours_degrees, sexagesimal_hours_degrees or sexagesimal_degrees")
		}
		if !coordFrames[r.CoordFrame] {
			add("coordinate frame is required (icrs, fk5, fk4 or galactic): the file does not say, and there is no default")
		}
		if (r.CoordFrame == "fk4" || r.CoordFrame == "fk5") && r.Equinox == "" {
			add("an fk4 or fk5 frame needs an equinox (B1950, J2000, ...)")
		}
		if r.Equinox != "" && r.CoordFrame != "fk4" && r.CoordFrame != "fk5" {
			add("an equinox goes only with an fk4 or fk5 frame")
		}
		if r.AngleColumn != "" {
			if !angleUnits[r.AngleUnit] {
				add("angle unit is required with an angle column and must be deg or rad")
			}
			if r.Quantity != "" && !quantities[r.Quantity] {
				add("quantity must be b_field, gradient, dust_polarization, starlight_polarization or unspecified, or left undeclared")
			}
		} else if r.SigmaColumn != "" || r.AngleUnit != "" || r.Quantity != "" {
			add("an angle unit, quantity or sigma column needs an angle column")
		}
		if len(r.Values) > MaxRuleFields {
			add("at most %d value columns", MaxRuleFields)
		}
	case RoleMask:
		if !maskTypes[r.MaskType] {
			add("mask type is required and must be low_snr, saturated, invalid, user_excluded or other (pixels with no value are computed from the data, never declared)")
		}
		if r.MaskNonzero != "flagged" && r.MaskNonzero != "valid" {
			add("say what a nonzero pixel of the mask file means: flagged, or valid (Vesqira does not guess it)")
		}
		if r.MaskType == "other" && strings.TrimSpace(r.MaskMeaning) == "" {
			add("a mask of type other needs a description of what it flags")
		}
		if err := r.checkMaskTarget(); err != nil {
			add("mask target: %v", err)
		}
	case RoleIgnore:
		// nothing to declare: the generic check above refused every declaration
	}
	return bad
}

// checkMaskTarget checks the target template: a path inside the project made of {dir} and {stem} and plain text.
func (r Rule) checkMaskTarget() error {
	t := r.MaskTarget
	switch {
	case t == "":
		return errors.New("is required: say which dataset the mask belongs to, for example {dir}/{stem}.fits")
	case len(t) > MaxPatternLen || strings.ContainsAny(t, "\x00\n\r\\"):
		return errors.New("is too long or has a control character or a backslash")
	case !strings.Contains(t, "{stem}"):
		return errors.New("must contain {stem}, the mask's file name without its extension")
	}
	rest := strings.NewReplacer("{dir}", "", "{stem}", "").Replace(t)
	if strings.ContainsAny(rest, "{}") {
		return errors.New("may only use the placeholders {dir} and {stem}")
	}
	if strings.HasPrefix(t, "/") || strings.Contains(t, "..") || (strings.Contains(t, "{dir}") && !strings.HasPrefix(t, "{dir}/") && t != "{dir}") || strings.Count(t, "{dir}") > 1 {
		return errors.New("must stay inside the project: {dir} only at the start, no .. and no leading /")
	}
	return nil
}

// MaskTargetPath is the project-relative path of the dataset a mask file belongs to, by the rule's template: the mask's folder,
// and its file name without the extension and without MaskStrip. It refuses a mask whose name does not end with MaskStrip, or that
// would be its own target.
func (r Rule) MaskTargetPath(maskRel string) (string, error) {
	if err := r.checkMaskTarget(); err != nil {
		return "", err
	}
	dir := path.Dir(maskRel)
	if dir == "." {
		dir = ""
	}
	base := path.Base(maskRel)
	stem := strings.TrimSuffix(base, Ext(base))
	if r.MaskStrip != "" {
		if !strings.HasSuffix(stem, r.MaskStrip) || stem == r.MaskStrip {
			return "", fmt.Errorf("the file name does not end with %q, so the dataset it belongs to cannot be named", r.MaskStrip)
		}
		stem = strings.TrimSuffix(stem, r.MaskStrip)
	}
	t := strings.ReplaceAll(r.MaskTarget, "{stem}", stem)
	if dir == "" {
		t = strings.TrimPrefix(strings.Replace(t, "{dir}", "", 1), "/")
	} else {
		t = strings.Replace(t, "{dir}", dir, 1)
	}
	t = path.Clean(t)
	if t == "." || strings.HasPrefix(t, "../") || t == ".." || strings.HasPrefix(t, "/") {
		return "", errors.New("the dataset it names is outside the project")
	}
	if t == maskRel {
		return "", errors.New("the mask would be the dataset itself")
	}
	return t, nil
}

// ValidateRules checks a whole list: each rule, the count and that names are unique (a name is what the declarations point at).
func ValidateRules(rs []Rule) []string {
	var bad []string
	if len(rs) > MaxRules {
		bad = append(bad, fmt.Sprintf("at most %d rules", MaxRules))
	}
	seen := map[string]bool{}
	for i, r := range rs {
		for _, b := range r.Validate() {
			bad = append(bad, fmt.Sprintf("rule %d (%s): %s", i+1, r.Name, b))
		}
		k := strings.ToLower(r.Name)
		if seen[k] {
			bad = append(bad, fmt.Sprintf("rule %d: the name %q is used twice", i+1, r.Name))
		}
		seen[k] = true
	}
	return bad
}

// Matcher compiles the rule's pattern into a test on a relative path (slash-separated, from the project folder).
//
// glob: a pattern without "/" matches the file name at any depth ("*_kms.fits"); a pattern with "/" is anchored at the
// project folder and "*", "?" and "[...]" stay inside one path segment, while a segment "**" stands for any number of
// segments ("vda/**/psi_*.fits", "data/**"). Matching is case-sensitive.
// regex: Go (RE2) syntax, searched in the whole relative path; anchor it with ^ and $. RE2 has no backtracking, so a
// pattern cannot stall the scan; prefix it with (?i) to ignore case.
func (r Rule) Matcher() (func(rel string) bool, error) {
	p := strings.TrimSpace(r.Pattern)
	switch {
	case p == "":
		return nil, errors.New("is empty")
	case len(p) > MaxPatternLen:
		return nil, fmt.Errorf("is longer than %d characters", MaxPatternLen)
	case strings.ContainsAny(p, "\x00\n\r"):
		return nil, errors.New("has a control character")
	}
	switch r.Kind {
	case "regex":
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("is not a valid regular expression (%v)", strings.TrimPrefix(err.Error(), "error parsing regexp: "))
		}
		return re.MatchString, nil
	case "glob":
		p = strings.ReplaceAll(p, `\`, "/")
		p = strings.TrimPrefix(p, "./")
		if strings.HasPrefix(p, "/") {
			return nil, errors.New("is absolute: write it relative to the project folder")
		}
		segs := strings.Split(p, "/")
		for _, sg := range segs {
			if sg == ".." || sg == "" {
				return nil, errors.New("has an empty or .. segment")
			}
			if _, err := path.Match(sg, "x"); err != nil {
				return nil, fmt.Errorf("is not a valid glob (%v)", err)
			}
		}
		if len(segs) == 1 && segs[0] != "**" {
			return func(rel string) bool { ok, _ := path.Match(segs[0], path.Base(rel)); return ok }, nil
		}
		return func(rel string) bool { return matchSegments(segs, strings.Split(rel, "/")) }, nil
	}
	return nil, errors.New("kind must be glob or regex")
}

func matchSegments(pat, parts []string) bool {
	for len(pat) > 0 {
		if pat[0] == "**" {
			for len(pat) > 1 && pat[1] == "**" {
				pat = pat[1:]
			}
			if len(pat) == 1 {
				return len(parts) > 0 // "dir/**" matches what is under dir, not dir itself
			}
			for i := 0; i <= len(parts); i++ {
				if matchSegments(pat[1:], parts[i:]) {
					return true
				}
			}
			return false
		}
		if len(parts) == 0 {
			return false
		}
		if ok, _ := path.Match(pat[0], parts[0]); !ok {
			return false
		}
		pat, parts = pat[1:], parts[1:]
	}
	return len(parts) == 0
}

// ---- what the built-in rules say about each file ----

// Built-in roles of the preview. A built-in role says what the file is by good practice; it never says what its values are.
const (
	BIDataset       = "dataset"
	BICatalog       = "catalog"
	BIRunRecord     = "run_record"
	BIRunOutput     = "run_output"
	BIROHSAFit      = "rohsa_fit"
	BIROHSAProduct  = "rohsa_product"
	BIConfig        = "config"
	BIJob           = "job_template"
	BIArchive       = "archive"
	BIOtherData     = "other_discipline"
	BIDeclaredData  = "declared_data"
	BIOther         = "other"
	vgtRolePrefix   = "vgt_"
	ruleByBuiltin   = "built-in"
	maxConflictList = 50
	maxExamples     = 10
)

// Item is one file the walk listed, with what the built-in rules say about it.
type Item struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	MtimeNS int64  `json:"mtimeNs"`
	Format  string `json:"format,omitempty"`
	// Role is the built-in role: dataset, catalog, run_record, vgt_psi/vgt_reliability/vgt_support/vgt_fit_rbar2, run_output,
	// rohsa_fit, rohsa_product, config, job_template, archive, other_discipline, declared_data or other.
	Role string `json:"role"`
	// Why names the built-in rule behind Role.
	Why     string `json:"why"`
	RunPath string `json:"runPath,omitempty"`
	// Locked says no researcher rule applies to it: an archive is never opened and a run record belongs to the run.
	Locked bool `json:"locked,omitempty"`
}

// Items lists every file of a scan result with its built-in role. The result must come from a scan with Options.Inventory;
// without it the unrecognised files are only a count. Archives listed are capped (Skipped.Archives).
func Items(r *Result) []Item {
	var out []Item
	runKind := map[string]*Run{}
	for i := range r.Runs {
		runKind[r.Runs[i].Path] = &r.Runs[i]
	}
	for i := range r.Runs {
		run := &r.Runs[i]
		if run.Record != "" {
			out = append(out, Item{Path: joinRel(run.Path, run.Record), Size: run.RecordSize, MtimeNS: run.MtimeNS, Role: BIRunRecord, RunPath: run.Path, Locked: true,
				Why: "run record (" + run.RecordKind + "): a folder holding " + run.Record + " is a run"})
		}
		for _, o := range run.Outputs {
			role, why := runFileRole(run, o.Path, o.Format)
			out = append(out, Item{Path: o.Path, Size: o.Size, MtimeNS: o.MtimeNS, Format: o.Format, Role: role, Why: why, RunPath: run.Path})
		}
	}
	for _, f := range r.Data {
		role, why := dataRole(f)
		out = append(out, Item{Path: f.Path, Size: f.Size, MtimeNS: f.MtimeNS, Format: f.Format, Role: role, Why: why})
	}
	for _, f := range r.Configs {
		out = append(out, Item{Path: f.Path, Size: f.Size, MtimeNS: f.MtimeNS, Role: BIConfig, Why: "config file: a .toml/.yaml/.json under configs/ or a declared config folder"})
	}
	for _, f := range r.Jobs {
		out = append(out, Item{Path: f.Path, Size: f.Size, MtimeNS: f.MtimeNS, Role: BIJob, Why: "job template: .slurm/.sbatch/.pbs, or a .sh with #SBATCH, under a jobs folder"})
	}
	for _, p := range r.Skipped.Archives {
		out = append(out, Item{Path: p, Role: BIArchive, Locked: true, Why: "archive: listed, never opened"})
	}
	for _, o := range r.Others {
		it := Item{Path: o.Path, Size: o.Size, MtimeNS: o.MtimeNS, RunPath: o.RunPath, Role: BIOther, Why: "no built-in rule recognises this file"}
		if run := runKind[o.RunPath]; run != nil && o.RunPath != "" {
			it.Role, it.Why = runFileRole(run, o.Path, "")
		}
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func dataRole(f File) (role, why string) {
	where := ""
	if f.Role != "" {
		where = " in data/" + f.Role
		if f.ReadOnly {
			where += " (read-only input)"
		}
	}
	switch {
	case f.Format == "FITS":
		return BIDataset, "FITS file" + where + ": a self-describing format, registered as a dataset"
	case f.Format == "Catalog":
		return BICatalog, "table offered as a point catalog" + where + ": .ecsv/.vot anywhere, or a table-shaped text file in a declared data folder"
	case f.Format == "other":
		return BIDeclaredData, "file in a folder you declared as data in project.toml: listed, no viewer"
	}
	return BIOtherData, f.Format + " file: listed with \"no viewer yet\""
}

// runFileRole is the role of a file inside a run folder: what the run record names, or a plain output of the run.
func runFileRole(run *Run, rel, format string) (role, why string) {
	base := path.Base(rel)
	switch run.RecordKind {
	case string(recVGT):
		if r, ok := run.OutputRoles[base]; ok {
			return vgtRolePrefix + r, "VGT output: " + run.Record + " names " + base + " as the run's " + strings.ReplaceAll(r, "_", " ") + " map"
		}
	case string(recROHSA):
		stem := strings.TrimSuffix(run.Record, "_run_trace.toml")
		if base == stem+".dat" {
			return BIROHSAFit, "ROHSA fit: the raw output of the run " + run.Record + " names (" + stem + ".dat)"
		}
		if format == "FITS" || fitsName(strings.ToLower(base)) {
			for _, seg := range strings.Split(path.Dir(rel), "/") {
				if seg == "maps" || seg == "cubes" {
					return BIROHSAProduct, "ROHSA product: a FITS file under " + seg + "/ in a ROHSA run folder"
				}
			}
		}
	}
	return BIRunOutput, "file in the run folder " + run.Path + ": it belongs to the run, which does not name it"
}

// ---- classification with the researcher's rules ----

// Proposal is the final proposal for one file: the role, who proposed it and why.
type Proposal struct {
	Item
	// ProposedRole is the built-in role, or the winning rule's role (dataset, orientation, catalog, ignore).
	ProposedRole string `json:"proposedRole"`
	// By is "built-in" or the name of the rule that won; Rule is that name or "".
	By   string `json:"by"`
	Rule string `json:"rule,omitempty"`
	// Matched lists every rule that matched, in order; the first one wins.
	Matched []string `json:"matched,omitempty"`
	// Declares says what the winning rule would declare, in words, each labelled "declared by your rule <name>".
	Declares []string `json:"declares,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// RuleReport says what one rule does to the listing.
type RuleReport struct {
	Name     string   `json:"name"`
	Error    string   `json:"error,omitempty"`
	Matches  int      `json:"matches"`  // files the pattern matches
	Wins     int      `json:"wins"`     // of those, files where it is the first match
	Shadowed int      `json:"shadowed"` // matches an earlier rule took
	Examples []string `json:"examples"` // up to ten matched paths
}

// Conflict is a file two or more rules match: the first rule decides.
type Conflict struct {
	Path   string   `json:"path"`
	Rules  []string `json:"rules"`
	Winner string   `json:"winner"`
	// Differ is true when the rules would not say the same thing (a different role or other declarations).
	Differ bool `json:"differ"`
}

// Classification is the preview of a listing under a list of rules.
type Classification struct {
	Proposals     []Proposal   `json:"-"`
	Rules         []RuleReport `json:"rules"`
	Conflicts     []Conflict   `json:"conflicts"`
	ConflictCount int          `json:"conflictCount"`
}

// Classify applies the rules, in order and first match wins, to the files that are not locked. A rule with a pattern error
// matches nothing and says so in its report.
func Classify(items []Item, rules []Rule) Classification {
	type compiled struct {
		rule  Rule
		match func(string) bool
	}
	var cs []compiled
	reports := make([]RuleReport, len(rules))
	for i, r := range rules {
		reports[i] = RuleReport{Name: r.Name, Examples: []string{}}
		m, err := r.Matcher()
		if err != nil {
			reports[i].Error = "pattern " + err.Error()
			cs = append(cs, compiled{r, nil})
			continue
		}
		if bad := r.Validate(); len(bad) > 0 {
			reports[i].Error = strings.Join(bad, "; ")
		}
		cs = append(cs, compiled{r, m})
	}
	out := Classification{Rules: reports, Conflicts: []Conflict{}}
	for _, it := range items {
		p := Proposal{Item: it, ProposedRole: it.Role, By: ruleByBuiltin}
		if !it.Locked {
			var hit []int
			for i, c := range cs {
				if c.match != nil && reports[i].Error == "" && c.match(it.Path) {
					hit = append(hit, i)
				}
			}
			for k, i := range hit {
				rp := &out.Rules[i]
				rp.Matches++
				if len(rp.Examples) < maxExamples {
					rp.Examples = append(rp.Examples, it.Path)
				}
				if k == 0 {
					rp.Wins++
				} else {
					rp.Shadowed++
				}
				p.Matched = append(p.Matched, cs[i].rule.Name)
			}
			if len(hit) > 0 {
				w := cs[hit[0]].rule
				p.ProposedRole, p.By, p.Rule = w.Role, w.Name, w.Name
				p.Declares = w.describe()
				p.Warnings = ruleWarnings(w, it)
				if len(hit) > 1 {
					out.ConflictCount++
					if len(out.Conflicts) < maxConflictList {
						differ := false
						for _, i := range hit[1:] {
							if !sameDeclaration(cs[hit[0]].rule, cs[i].rule) {
								differ = true
							}
						}
						out.Conflicts = append(out.Conflicts, Conflict{Path: it.Path, Rules: p.Matched, Winner: w.Name, Differ: differ})
					}
				}
			}
		}
		out.Proposals = append(out.Proposals, p)
	}
	return out
}

func sameDeclaration(a, b Rule) bool {
	a.Name, a.Kind, a.Pattern, b.Name, b.Kind, b.Pattern = "", "", "", "", "", ""
	return a.Role == b.Role && a.Unit == b.Unit && a.AngleUnit == b.AngleUnit && a.AngleFrame == b.AngleFrame && a.Quantity == b.Quantity &&
		a.Lon == b.Lon && a.Lat == b.Lat && a.CoordFormat == b.CoordFormat && a.CoordFrame == b.CoordFrame && a.Equinox == b.Equinox &&
		a.AngleColumn == b.AngleColumn && a.SigmaColumn == b.SigmaColumn && strings.Join(a.Values, "\x00") == strings.Join(b.Values, "\x00") &&
		a.MaskType == b.MaskType && a.MaskNonzero == b.MaskNonzero && a.MaskMeaning == b.MaskMeaning && a.MaskTarget == b.MaskTarget && a.MaskStrip == b.MaskStrip
}

// describe says in words what the rule declares, each statement marked as the rule's.
func (r Rule) describe() []string {
	by := " (declared by your rule " + r.Name + ")"
	var out []string
	switch r.Role {
	case RoleDataset:
		if r.Unit != "" {
			out = append(out, "brightness unit "+r.Unit+by)
		}
	case RoleOrientation:
		out = append(out, "axial orientation in "+r.AngleUnit+by)
		if r.AngleFrame != "" {
			out = append(out, "angle frame "+r.AngleFrame+by)
		}
		if r.Quantity != "" {
			out = append(out, "quantity "+r.Quantity+by)
		}
	case RoleCatalog:
		out = append(out, "position columns "+r.Lon+", "+r.Lat+" as "+r.CoordFormat+" in "+r.CoordFrame+strings.TrimPrefix(" "+r.Equinox, " ")+by)
		if r.AngleColumn != "" {
			out = append(out, "angle column "+r.AngleColumn+" in "+r.AngleUnit+by)
		}
	case RoleMask:
		meaning := map[string]string{"flagged": "nonzero pixels are flagged", "valid": "nonzero pixels are valid, zero pixels are flagged"}[r.MaskNonzero]
		out = append(out, "mask of type "+r.MaskType+": "+meaning+by, "belongs to the dataset at "+r.MaskTarget+" (stem of the mask's name"+map[bool]string{true: " without " + r.MaskStrip, false: ""}[r.MaskStrip != ""]+")")
	case RoleIgnore:
		out = append(out, "not offered for registration")
	}
	return out
}

func ruleWarnings(r Rule, it Item) []string {
	var w []string
	switch r.Role {
	case RoleDataset, RoleOrientation, RoleMask:
		if it.Format != "FITS" && !fitsName(strings.ToLower(path.Base(it.Path))) {
			w = append(w, "the name is not a FITS name; registering it needs the worker to read it as FITS")
		}
	case RoleCatalog:
		if it.Format == "FITS" || fitsName(strings.ToLower(path.Base(it.Path))) {
			w = append(w, "a FITS file is read as a table only when it has no image axes")
		}
	}
	if it.RunPath != "" && r.Role != RoleIgnore {
		w = append(w, "inside the run "+it.RunPath+": the file is registered by the run's import, and the rule's declarations apply once it is")
	}
	return w
}

// ---- grouping ----

// Group is the preview's unit: files of one extension with one proposed role and one author.
type Group struct {
	Role    string     `json:"role"`
	Ext     string     `json:"ext"` // lower-case extension with the dot, ".fits.gz" kept whole, or "(none)"
	By      string     `json:"by"`
	Why     string     `json:"why"`
	Count   int        `json:"count"`
	Bytes   int64      `json:"bytes"`
	Samples []Proposal `json:"samples"`
}

// Ext is the extension a file is grouped under.
func Ext(name string) string {
	l := strings.ToLower(path.Base(name))
	for _, e := range []string{".fits.gz", ".ome.tiff", ".ome.tif", ".tar.gz", ".tar.bz2", ".tar.xz"} {
		if strings.HasSuffix(l, e) {
			return e
		}
	}
	if e := filepath.Ext(l); e != "" && e != l {
		return e
	}
	return "(none)"
}

// GroupProposals groups the proposals by (role, extension, author), largest first, with at most samples examples each. When
// expand is non-empty the group with that key ("role|ext|by") lists up to expandMax examples instead.
func GroupProposals(ps []Proposal, samples int, expand string, expandMax int) []Group {
	idx := map[string]*Group{}
	var order []string
	for _, p := range ps {
		k := p.ProposedRole + "|" + Ext(p.Path) + "|" + p.By
		g := idx[k]
		if g == nil {
			g = &Group{Role: p.ProposedRole, Ext: Ext(p.Path), By: p.By, Why: p.Why, Samples: []Proposal{}}
			if p.Rule != "" {
				g.Why = "your rule " + p.Rule
			}
			idx[k] = g
			order = append(order, k)
		}
		g.Count++
		g.Bytes += p.Size
		limit := samples
		if k == expand {
			limit = expandMax
		}
		if len(g.Samples) < limit {
			g.Samples = append(g.Samples, p)
		}
	}
	out := make([]Group, 0, len(order))
	for _, k := range order {
		out = append(out, *idx[k])
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Role+out[i].Ext < out[j].Role+out[j].Ext
	})
	return out
}

// GroupKey is the key GroupProposals expands by.
func GroupKey(g Group) string { return g.Role + "|" + g.Ext + "|" + g.By }

// ---- project.toml ----

const (
	rulesBegin = "# vesqira:import-rules begin (written by Vesqira after you confirmed; edit freely; the first matching rule wins)"
	rulesEnd   = "# vesqira:import-rules end"
)

// ErrForeignRules is returned when project.toml has [[import.rules]] outside the section Vesqira writes: those are the
// researcher's own and are never rewritten.
var ErrForeignRules = errors.New("project.toml has [[import.rules]] that Vesqira did not write; edit them there (the first matching rule wins), or delete them to let Vesqira write the saved rules")

// HasRulesSection says whether the file text holds the rules section Vesqira wrote (so [[import.rules]] outside it are the
// researcher's own).
func HasRulesSection(text string) bool { return strings.Contains(text, rulesBegin) }

// RulesEqual says whether two rule lists are the same rules in the same order.
func RulesEqual(a, b []Rule) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Name != b[i].Name || a[i].Kind != b[i].Kind || a[i].Pattern != b[i].Pattern || !sameDeclaration(a[i], b[i]) {
			return false
		}
	}
	return true
}

// RulesBlock is the TOML text of the rules section, markers included.
func RulesBlock(rules []Rule) string {
	var b strings.Builder
	b.WriteString("\n" + rulesBegin + "\n")
	for _, r := range rules {
		b.WriteString("[[import.rules]]\nname = " + tomlString(r.Name) + "\nkind = " + tomlString(r.Kind) + "\npattern = " + tomlString(r.Pattern) + "\nrole = " + tomlString(r.Role) + "\n")
		for _, kv := range [][2]string{{"unit", r.Unit}, {"angle_unit", r.AngleUnit}, {"angle_frame", r.AngleFrame}, {"quantity", r.Quantity}, {"lon", r.Lon}, {"lat", r.Lat},
			{"coord_format", r.CoordFormat}, {"coord_frame", r.CoordFrame}, {"equinox", r.Equinox}, {"angle_column", r.AngleColumn}, {"sigma_column", r.SigmaColumn},
			{"mask_type", r.MaskType}, {"mask_nonzero", r.MaskNonzero}, {"mask_meaning", r.MaskMeaning}, {"mask_target", r.MaskTarget}, {"mask_strip", r.MaskStrip}} {
			if kv[1] != "" {
				b.WriteString(kv[0] + " = " + tomlString(kv[1]) + "\n")
			}
		}
		if len(r.Values) > 0 {
			qs := make([]string, len(r.Values))
			for i, v := range r.Values {
				qs[i] = tomlString(v)
			}
			b.WriteString("values = [" + strings.Join(qs, ", ") + "]\n")
		}
	}
	b.WriteString(rulesEnd + "\n")
	return b.String()
}

// ReplaceRules returns the project file text with the rules section Vesqira wrote replaced by one for rules (removed when
// there are none); everything else is kept byte for byte. A file whose [[import.rules]] are not inside the markers is refused
// (ErrForeignRules). The result is checked to parse.
func ReplaceRules(existing []byte, rules []Rule) ([]byte, error) {
	text := string(existing)
	if i := strings.Index(text, rulesBegin); i >= 0 {
		j := strings.Index(text[i:], rulesEnd)
		if j < 0 {
			return nil, errors.New("project.toml has the start of the rules section but not its end; edit it by hand")
		}
		end := i + j + len(rulesEnd)
		if end < len(text) && text[end] == '\n' {
			end++
		}
		start := i
		if start > 0 && text[start-1] == '\n' && start > 1 && text[start-2] == '\n' {
			start-- // the blank line the block was written after
		}
		text = text[:start] + text[end:]
	}
	if pf, err := ParseProjectFile([]byte(text)); err != nil {
		return nil, err
	} else if len(pf.Import.Rules) > 0 {
		return nil, ErrForeignRules
	}
	out := []byte(text)
	if len(rules) > 0 {
		out = append(bytes.TrimRight(out, "\n"), '\n')
		out = append(out, RulesBlock(rules)...)
	}
	if _, err := ParseProjectFile(out); err != nil {
		return nil, fmt.Errorf("the rules could not be added without breaking the file (%v); edit project.toml by hand", err)
	}
	return out, nil
}
