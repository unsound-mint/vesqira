package runs

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Tool profiles for runs (ADR 0068, item 1): which tool wrote a folder, recognised by a file and a key inside it, and which of
// the tool's outputs fill which generic role. A profile is data. The parsers below keep validating what they read; a profile
// never runs code, so a wrong profile gives a wrong label, not a wrong value, and the label says who declared it.

//go:embed run_profiles.json
var shippedProfiles []byte

// Generic output roles. The ids are stored in run_outputs.role and sent in contracts; the tool's name is only a label.
const (
	RoleOrientation    = "orientation"
	RoleReliability    = "reliability"
	RoleSupport        = "support"
	RoleComponentTable = "component_table"
	RoleComponentMaps  = "component_maps"
	RoleModelCube      = "model_cube"
	RoleResidualCube   = "residual_cube"
	RoleCrop           = "crop"
)

// LegacyProfile labels a run recorded before profiles existed. The run was recognised by Vesqira's own earlier code, not by a
// profile, so no name and version is claimed for it.
const LegacyProfile = "legacy"

// Registry is a set of profiles. The shipped one is Shipped(); a test builds its own with LoadProfiles.
type Registry struct {
	roles    map[string]string
	profiles []*Profile
}

type Profile struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	// Kind is the run kind the profile's runs get (runs.kind). The rerun and job code keys on it until run templates are data.
	Kind   string `json:"kind"`
	Tool   string `json:"label"` // the tool's plain name in messages ("VGT"), a label and nothing else
	Source string `json:"source"`
	// Orientation names the entry of the worker's orientation_profiles.json that reads this tool's header cards; Components names
	// the profile whose component table this one's runs use.
	Orientation string              `json:"orientation"`
	Components  string              `json:"components"`
	Record      *RecordSpec         `json:"record"`
	Files       map[string]FileSpec `json:"files"`
	Outputs     *OutputSpec         `json:"outputs"`
	recordRe    *regexp.Regexp      `json:"-"`
	fileRe      map[string]*regexp.Regexp
}

// RecordSpec is how a run of the tool is recognised: a record file (by name or by pattern) and, inside it, a key.
type RecordSpec struct {
	File       string `json:"file"`
	Pattern    string `json:"pattern"`
	Reader     string `json:"reader"` // the layout of the record, one of the readers Vesqira has
	StemSuffix string `json:"stemSuffix"`
	// AnyKey: the record is this tool's when it is TOML holding at least one of these keys (a dotted path names a key in a table).
	AnyKey []string `json:"anyKey"`
}

// FileSpec names a file of a run: an exact name with {stem}, {region} or {label} filled in, or a pattern.
type FileSpec struct {
	File    string `json:"file"`
	Pattern string `json:"pattern"`
	Input   bool   `json:"input"`
}

// OutputSpec says which outputs a run has and which role each fills: the entries of a table in the record that name files, or
// a product tree with a folder per kind.
type OutputSpec struct {
	Table   string               `json:"table"`
	Files   map[string]OutputRef `json:"files"`
	Tree    string               `json:"tree"`
	Folders []OutputFolder       `json:"folders"`
}

type OutputRef struct {
	Product string `json:"product"`
	Role    string `json:"role"`
	// Extra labels a role that is not a generic one: the tool's own output, shown by this label.
	Extra string `json:"extra"`
}

type OutputFolder struct {
	Dir           string `json:"dir"`
	ProductPrefix string `json:"productPrefix"`
	// Role is the role of every product of the folder no other entry places; empty leaves such a product an extra under its own name.
	Role string `json:"role"`
	// Match places a product whose name matches a pattern; Products places one by its exact name.
	Match    []ProductMatch    `json:"match"`
	Products map[string]string `json:"products"`
}

type ProductMatch struct {
	Pattern string `json:"pattern"`
	Role    string `json:"role"`
	re      *regexp.Regexp
}

// Readers a profile may name. Each is Go code that validates what it reads.
const (
	ReaderRunMetadata = "run_metadata" // a TOML run record with tables config, outputs, ... (ParseRecord)
	ReaderROHSATrace  = "rohsa_trace"  // ROHSA-LMC's run trace (ParseROHSA)
)

var snake = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// LoadProfiles reads and checks a profile file. It refuses an unknown field, a role that is neither generic nor a labelled extra,
// a name that cannot be part of a label, and a pattern that does not compile.
func LoadProfiles(data []byte) (*Registry, error) {
	var doc struct {
		Format   int               `json:"format"`
		About    string            `json:"about"`
		Roles    map[string]string `json:"roles"`
		Profiles []*Profile        `json:"profiles"`
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("run profiles: %w", err)
	}
	if doc.Format != 1 || len(doc.Profiles) == 0 {
		return nil, errors.New("run profiles: unknown format")
	}
	reg := &Registry{roles: doc.Roles, profiles: doc.Profiles}
	seen := map[string]bool{}
	for _, p := range reg.profiles {
		if p.Name == "" || p.Version == "" || p.Kind == "" || strings.ContainsAny(p.Name+p.Version, "@:") {
			return nil, fmt.Errorf("run profile %q needs a name, a version and a kind, and no '@' or ':' in them", p.Name)
		}
		if seen[p.Label()] {
			return nil, fmt.Errorf("run profile %s is declared twice", p.Label())
		}
		seen[p.Label()] = true
		if err := p.compile(reg); err != nil {
			return nil, fmt.Errorf("run profile %s: %w", p.Label(), err)
		}
	}
	for _, p := range reg.profiles {
		if p.Components != "" && reg.ByName(p.Components) == nil {
			return nil, fmt.Errorf("run profile %s: components names the unknown profile %q", p.Label(), p.Components)
		}
	}
	return reg, nil
}

func (p *Profile) compile(reg *Registry) error {
	if r := p.Record; r != nil {
		if (r.File == "") == (r.Pattern == "") {
			return errors.New("a record names a file or a pattern, not both and not neither")
		}
		if r.Reader != ReaderRunMetadata && r.Reader != ReaderROHSATrace {
			return fmt.Errorf("unknown reader %q", r.Reader)
		}
		if len(r.AnyKey) == 0 {
			return errors.New("a record needs at least one key to recognise it by content, not by its name alone")
		}
		if r.Pattern != "" {
			re, err := regexp.Compile(r.Pattern)
			if err != nil {
				return err
			}
			p.recordRe = re
		}
	}
	p.fileRe = map[string]*regexp.Regexp{}
	for role, f := range p.Files {
		if _, ok := reg.roles[role]; !ok {
			return fmt.Errorf("file role %q is not a generic role", role)
		}
		if (f.File == "") == (f.Pattern == "") {
			return fmt.Errorf("file %q names a file or a pattern, not both and not neither", role)
		}
		if f.Pattern != "" {
			re, err := regexp.Compile(f.Pattern)
			if err != nil {
				return err
			}
			p.fileRe[role] = re
		}
	}
	if o := p.Outputs; o != nil {
		if (o.Table == "") == (o.Tree == "") {
			return errors.New("outputs name a table of the record or a product tree, not both and not neither")
		}
		check := func(role, extra string) error {
			_, generic := reg.roles[role]
			switch {
			case generic && extra == "":
			case !generic && extra != "" && snake.MatchString(role):
			default:
				return fmt.Errorf("output role %q is neither a generic role nor an extra with a label", role)
			}
			return nil
		}
		for key, ref := range o.Files {
			if key == "" || ref.Product == "" || !snake.MatchString(ref.Product) {
				return fmt.Errorf("output %q needs a snake_case product name", key)
			}
			if err := check(ref.Role, ref.Extra); err != nil {
				return err
			}
		}
		for _, f := range o.Folders {
			if f.Dir == "" || strings.ContainsAny(f.Dir, `/\`) {
				return errors.New("a product folder needs a plain directory name")
			}
			if f.Role != "" {
				if err := check(f.Role, ""); err != nil {
					return err
				}
			}
			for _, role := range f.Products {
				if err := check(role, ""); err != nil {
					return err
				}
			}
			for i := range f.Match {
				re, err := regexp.Compile(f.Match[i].Pattern)
				if err != nil {
					return err
				}
				f.Match[i].re = re
				if err := check(f.Match[i].Role, ""); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// With is this registry plus the profiles of another file (a test's synthetic profile, later a project's own), whose generic
// roles must be the same. A profile label that is already here is refused: a file cannot redefine a shipped profile.
func (r *Registry) With(data []byte) (*Registry, error) {
	o, err := LoadProfiles(data)
	if err != nil {
		return nil, err
	}
	for role := range o.roles {
		if _, ok := r.roles[role]; !ok {
			return nil, fmt.Errorf("run profiles: %q is not a generic role", role)
		}
	}
	out := &Registry{roles: r.roles, profiles: append([]*Profile{}, r.profiles...)}
	for _, p := range o.profiles {
		if out.ByLabel(p.Label()) != nil {
			return nil, fmt.Errorf("run profile %s is already defined", p.Label())
		}
		out.profiles = append(out.profiles, p)
	}
	return out, nil
}

var shipped *Registry

// Shipped is the registry compiled into the program.
func Shipped() *Registry {
	if shipped == nil {
		r, err := LoadProfiles(shippedProfiles)
		if err != nil {
			panic(err) // the file is part of the build; a test loads it
		}
		shipped = r
	}
	return shipped
}

// Label is how a run's detection is named wherever it is shown or stored: name@version.
func (p *Profile) Label() string { return p.Name + "@" + p.Version }

func (r *Registry) All() []*Profile { return r.profiles }

func (r *Registry) ByName(name string) *Profile {
	for _, p := range r.profiles {
		if p.Name == name {
			return p
		}
	}
	return nil
}

// ByLabel finds a profile by name@version; nil for "", the legacy label or a profile this build does not have.
func (r *Registry) ByLabel(label string) *Profile {
	for _, p := range r.profiles {
		if p.Label() == label {
			return p
		}
	}
	return nil
}

// RoleLabel is the plain name of a generic role, or "" for an id that is not one.
func (r *Registry) RoleLabel(role string) string { return r.roles[role] }

func (r *Registry) IsGeneric(role string) bool { _, ok := r.roles[role]; return ok }

// Candidate is a profile whose record file is among a folder's names.
type Candidate struct {
	File    string
	Profile *Profile
}

// Candidates lists, in profile order, each profile whose record file is in names (the first by name when a pattern matches
// several). A name is a candidate only: the record's content decides (Claims).
func (r *Registry) Candidates(names map[string]bool) []Candidate {
	var out []Candidate
	for _, p := range r.profiles {
		if p.Record == nil {
			continue
		}
		if p.Record.File != "" {
			if names[p.Record.File] {
				out = append(out, Candidate{p.Record.File, p})
			}
			continue
		}
		var found []string
		for n := range names {
			if p.recordRe.MatchString(n) {
				found = append(found, n)
			}
		}
		if len(found) > 0 {
			sort.Strings(found)
			out = append(out, Candidate{found[0], p})
		}
	}
	return out
}

// RecordFiles are the exact record file names of the profiles that use a reader, for a path that names a record directly.
func (r *Registry) RecordFiles(reader string) []string {
	var out []string
	for _, p := range r.profiles {
		if p.Record != nil && p.Record.Reader == reader && p.Record.File != "" {
			out = append(out, p.Record.File)
		}
	}
	return out
}

// ByRecordFile is the profile with that exact record file name and reader, or nil.
func (r *Registry) ByRecordFile(name, reader string) *Profile {
	for _, p := range r.profiles {
		if p.Record != nil && p.Record.Reader == reader && p.Record.File == name {
			return p
		}
	}
	return nil
}

// ByKind is the first profile with a run kind that has a record (the one a job of that kind imports).
func (r *Registry) ByKind(kind string) *Profile {
	for _, p := range r.profiles {
		if p.Kind == kind && p.Record != nil {
			return p
		}
	}
	return nil
}

// Claims says whether a record's content is this tool's: TOML holding one of the profile's keys. A record that is not valid TOML
// is claimed (the reader then says why it cannot read it), so an unreadable run record is not silently dropped.
func (p *Profile) Claims(data []byte) bool {
	if p.Record == nil {
		return false
	}
	var m map[string]any
	if _, err := toml.Decode(string(data), &m); err != nil {
		return true
	}
	for _, k := range p.Record.AnyKey {
		if hasPath(m, strings.Split(k, ".")) {
			return true
		}
	}
	return false
}

func hasPath(m map[string]any, path []string) bool {
	v, ok := m[path[0]]
	if !ok {
		return false
	}
	if len(path) == 1 {
		return true
	}
	sub, ok := v.(map[string]any)
	return ok && hasPath(sub, path[1:])
}

func tableAt(m map[string]any, name string) map[string]any {
	t, _ := m[name].(map[string]any)
	return t
}

// Stem is the record's name without the profile's suffix (rohsa_n4 for rohsa_n4_run_trace.toml).
func (p *Profile) Stem(record string) string {
	if p.Record == nil {
		return record
	}
	return strings.TrimSuffix(record, p.Record.StemSuffix)
}

// FileName fills a role's file template ({stem}, {region}, {label}); false when the role is not declared as one exact name.
func (p *Profile) FileName(role string, vars map[string]string) (string, bool) {
	f, ok := p.Files[role]
	if !ok || f.File == "" {
		return "", false
	}
	return fill(f.File, vars), true
}

// IsFile says whether a file name is the profile's file for a role (an exact name with the variables filled in, or a pattern).
func (p *Profile) IsFile(role, name string, vars map[string]string) bool {
	f, ok := p.Files[role]
	switch {
	case !ok:
		return false
	case f.Pattern != "":
		return p.fileRe[role].MatchString(name)
	}
	return name == fill(f.File, vars)
}

// FilePattern is the pattern a role's files match, nil when the role is declared by an exact name.
func (p *Profile) FilePattern(role string) *regexp.Regexp { return p.fileRe[role] }

func fill(t string, vars map[string]string) string {
	for k, v := range vars {
		t = strings.ReplaceAll(t, "{"+k+"}", v)
	}
	return t
}

// TreeDir is the folder (below the record's folder) a product tree sits in, "" when the profile declares none.
func (p *Profile) TreeDir(label string) string {
	if p.Outputs == nil || p.Outputs.Tree == "" {
		return ""
	}
	return strings.ReplaceAll(p.Outputs.Tree, "{label}", label)
}

// FolderRole is the generic role of a product found in a tree folder: the product name is the file name without the run's label
// and the folder's prefix. A product no entry maps stays under its own name (a declared extra), never guessed into a role.
func (p *Profile) FolderRole(dir, product string) string {
	if p.Outputs != nil {
		for _, f := range p.Outputs.Folders {
			if f.Dir != dir {
				continue
			}
			if role, ok := f.Products[product]; ok {
				return role
			}
			for _, m := range f.Match {
				if m.re.MatchString(product) {
					return m.Role
				}
			}
			if f.Role != "" {
				return f.Role
			}
		}
	}
	return product
}

// FolderRoleOfFile is the role of a file (its name without the extension) found in a tree folder when the run's label is not
// known: the folder's own role, or the role of the declared product whose name the file name ends with. "" when the profile
// places no file of that folder by either.
func (p *Profile) FolderRoleOfFile(dir, stem string) string {
	if p.Outputs == nil {
		return ""
	}
	for _, f := range p.Outputs.Folders {
		if f.Dir != dir {
			continue
		}
		keys := make([]string, 0, len(f.Products))
		for k := range f.Products {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if name := strings.TrimPrefix(k, f.ProductPrefix); stem == name || strings.HasSuffix(stem, "_"+name) {
				return f.Products[k]
			}
		}
		for _, m := range f.Match {
			if m.re.MatchString(stem) {
				return m.Role
			}
		}
		return f.Role
	}
	return ""
}

// FolderPrefix is the prefix a tree folder's products carry in their product name.
func (p *Profile) FolderPrefix(dir string) string {
	if p.Outputs != nil {
		for _, f := range p.Outputs.Folders {
			if f.Dir == dir {
				return f.ProductPrefix
			}
		}
	}
	return ""
}

// FolderDirs lists the folders of the product tree in the order declared.
func (p *Profile) FolderDirs() []string {
	var out []string
	if p.Outputs != nil {
		for _, f := range p.Outputs.Folders {
			out = append(out, f.Dir)
		}
	}
	return out
}

// NamesOutputs says whether the profile's record names its outputs (a table of file names), so a run is imported from it alone.
func (p *Profile) NamesOutputs() bool {
	return p.Outputs != nil && p.Outputs.Table != "" && p.Record != nil
}

// RoleOfProduct is the generic role (or the declared extra) a record-named output fills.
func (p *Profile) RoleOfProduct(product string) (role string, ok bool) {
	if p.Outputs == nil {
		return "", false
	}
	for _, ref := range p.Outputs.Files {
		if ref.Product == product {
			return ref.Role, true
		}
	}
	return "", false
}

// Products lists the record-named outputs in a stable order (by product name).
func (p *Profile) Products() []OutputRef {
	if p.Outputs == nil {
		return nil
	}
	var out []OutputRef
	for _, ref := range p.Outputs.Files {
		out = append(out, ref)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Product < out[j].Product })
	return out
}

// ExtraLabel is the label the profile gives a role that is not generic, "" otherwise.
func (p *Profile) ExtraLabel(role string) string {
	if p.Outputs != nil {
		for _, ref := range p.Outputs.Files {
			if ref.Role == role && ref.Extra != "" {
				return ref.Extra
			}
		}
	}
	return ""
}
