package discover

import (
	"context"
	"encoding/json"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/unsound-mint/vesqira/services/api/internal/runs"
)

// Field is one fact about a run and where it came from. Status is "declared" (a run record said it), "inferred"
// (guessed from a folder name; never promoted to declared) or "you" (the researcher wrote it in project.toml).
type Field struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Status string `json:"status"`
	Source string `json:"source"`
}

// Setting is one run setting read from a run record.
type Setting struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// File is a file the scan classified.
type File struct {
	Path     string `json:"path"` // relative to the project folder
	Size     int64  `json:"size"`
	MtimeNS  int64  `json:"mtimeNs"`
	Format   string `json:"format,omitempty"` // FITS, TIFF, ...; "other" for a file in a folder the researcher declared as data
	Viewer   bool   `json:"viewer"`           // Vesqira can show it
	Role     string `json:"role,omitempty"`   // raw, interim, processed, external (from the data/<role> layout)
	ReadOnly bool   `json:"readOnly,omitempty"`
}

// Run is a folder that is a run: it has a run record, or the researcher marked it.
type Run struct {
	Path       string    `json:"path"`
	Record     string    `json:"record"`     // the file that made it a run, relative to the run folder; "" for a manual mark
	RecordKind string    `json:"recordKind"` // vgt, provenance, run-json, params, mlflow, manual
	Fields     []Field   `json:"fields"`
	Settings   []Setting `json:"settings"`
	// Differs is the settings of this run that differ from its siblings in the same folder (computed once per scan).
	Differs     []Setting `json:"differs"`
	Notes       []string  `json:"notes"`
	Outputs     []File    `json:"outputs"` // files inside the run folder, at most maxRunOutputs of them
	OutputCount int       `json:"outputCount"`
	OutputBytes int64     `json:"outputBytes"`
	MtimeNS     int64     `json:"mtimeNs"` // of the run record
	RecordSize  int64     `json:"recordSize"`
	// OutputRoles maps an output file name to the role the run record gives it (VGT: psi, reliability, support, fit_rbar2). Only
	// what the record names; a file the record does not name has no role here.
	OutputRoles map[string]string `json:"outputRoles,omitempty"`

	// jobID and jobTemplate are the Vesqira job a run.json names (0 and "" when none): what lets a job's record and the fit
	// it produced be recognised as one run (mergeJobRecords). Not part of the wire.
	jobID       int64
	jobTemplate string
}

type Link struct {
	Path  string `json:"path"`
	State string `json:"state"` // outside | dangling | inside
}

type Skipped struct {
	Dirs         []string `json:"dirs"`      // folders not walked (build caches, environments, your skip patterns)
	NeverRead    []string `json:"neverRead"` // names marked as not to be opened: listed by name only
	Archives     []string `json:"archives"`  // archive files, listed and never opened (capped)
	ArchiveCount int      `json:"archiveCount"`
	ArchiveBytes int64    `json:"archiveBytes"`
}

type Git struct {
	Present bool   `json:"present"`
	Commit  string `json:"commit"`          // short; "" when not known
	Dirty   *int   `json:"dirty,omitempty"` // changed or untracked files; nil when not known
}

// ManualRun is a run the researcher marked in project.toml.
type ManualRun struct {
	Path      string
	Name      string
	Commit    string
	StartedAt string
}

// Layout is the researcher's own statement of the project's layout (project.toml [layout]).
type Layout struct {
	Data    []string // folders whose files are all listed as data, whatever their format
	Runs    []string // folders whose subfolders are all runs
	Configs []string // folders searched for config files in addition to configs/ and config*/
	Jobs    []string // folders searched for job templates in addition to hpc/, slurm/, jobs/, scripts/
	Skip    []string
}

type Options struct {
	Layout Layout
	// Catalogs offers tables as point catalogs (ADR 0044). Off for a project on another host: catalogs are read from this
	// computer only, and deciding that a text file is a table would cost a remote read per file.
	Catalogs    bool
	ManualRuns  []ManualRun
	MaxEntries  int           // 0 means DefaultMaxEntries
	MaxDuration time.Duration // 0 means DefaultMaxDuration
	Now         func() time.Time
	Progress    func(entries int, dir string)
	// Inventory also lists the files nothing recognises (Result.Others), for the import preview and the researcher's rules.
	Inventory bool
	// MaxDepth stops the walk below that many folder levels (0 means no depth limit) and says so (Truncated).
	MaxDepth int
	// Dirty counts changed files in the git work tree; nil leaves it unknown. Never called when there is no .git.
	Dirty func(ctx context.Context) (int, bool)
}

const (
	DefaultMaxEntries  = 200_000
	DefaultMaxDuration = 20 * time.Second
	maxRunOutputs      = 200
	maxList            = 200
	jobPeek            = 4 << 10
)

// Result is everything one scan found. Lists are sorted by path.
type Result struct {
	Runs    []Run   `json:"runs"`
	Data    []File  `json:"data"`
	Configs []File  `json:"configs"`
	Jobs    []File  `json:"jobs"`
	Skipped Skipped `json:"skipped"`
	Links   []Link  `json:"links"`
	Git     Git     `json:"git"`
	// ResultLike are folders named like results that hold no run: nothing says they are runs, so nothing guesses.
	ResultLike   []string `json:"resultLike"`
	OtherFiles   int      `json:"otherFiles"`
	Entries      int      `json:"entries"`
	Truncated    bool     `json:"truncated"`
	TruncatedWhy string   `json:"truncatedWhy,omitempty"`
	Canceled     bool     `json:"canceled"`
	Notes        []string `json:"notes"`
	DataFolders  int      `json:"dataFolders"`
	// Others are the files no built-in rule recognised, when Options.Inventory is set: at most MaxInventory of them
	// (OthersTruncated says when there were more), each with the run folder it sits in, if any.
	Others          []Other `json:"others,omitempty"`
	OthersTruncated bool    `json:"othersTruncated,omitempty"`
}

// Other is a file that no built-in rule recognised.
type Other struct {
	Path    string `json:"path"`
	Size    int64  `json:"size"`
	MtimeNS int64  `json:"mtimeNs"`
	RunPath string `json:"runPath,omitempty"` // the run folder it belongs to; "" when it is in none
}

// MaxInventory bounds Result.Others.
const MaxInventory = 50_000

type scanner struct {
	ctx    context.Context
	fs     FS
	opt    Options
	res    *Result
	start  time.Time
	manual map[string]ManualRun
	layRun map[string]bool
	layDat []string
	layCfg []string
	layJob []string
	stop   bool
}

func (s *scanner) now() time.Time {
	if s.opt.Now != nil {
		return s.opt.Now()
	}
	return time.Now()
}

func clean(list []string) []string {
	var out []string
	for _, p := range list {
		p = strings.Trim(path.Clean(strings.ReplaceAll(strings.TrimSpace(p), `\`, "/")), "/")
		if p != "" && p != "." && validRel(p) {
			out = append(out, p)
		}
	}
	return out
}

func within(rel string, folders []string) bool {
	for _, f := range folders {
		if rel == f || strings.HasPrefix(rel, f+"/") {
			return true
		}
	}
	return false
}

// Scan walks the project folder once. It stops at the entry or time budget and says so (Truncated), and stops when
// ctx is cancelled (Canceled); either way what was found so far is returned.
func Scan(ctx context.Context, fsys FS, opt Options) *Result {
	if opt.MaxEntries <= 0 {
		opt.MaxEntries = DefaultMaxEntries
	}
	if opt.MaxDuration <= 0 {
		opt.MaxDuration = DefaultMaxDuration
	}
	s := &scanner{ctx: ctx, fs: fsys, opt: opt, res: &Result{}, manual: map[string]ManualRun{}, layRun: map[string]bool{}}
	s.start = s.now()
	for _, m := range opt.ManualRuns {
		if p := clean([]string{m.Path}); len(p) == 1 {
			m.Path = p[0]
			s.manual[p[0]] = m
		}
	}
	for _, p := range clean(opt.Layout.Runs) {
		s.layRun[p] = true
	}
	s.layDat, s.layCfg, s.layJob = clean(opt.Layout.Data), clean(opt.Layout.Configs), clean(opt.Layout.Jobs)
	s.opt.Layout.Skip = clean(opt.Layout.Skip)
	s.git()
	s.walk("", nil)
	r := s.res
	r.Canceled = ctx.Err() != nil
	finish(r)
	return r
}

func finish(r *Result) {
	sort.Slice(r.Runs, func(i, j int) bool { return r.Runs[i].Path < r.Runs[j].Path })
	for _, f := range [][]File{r.Data, r.Configs, r.Jobs} {
		sort.Slice(f, func(i, j int) bool { return f[i].Path < f[j].Path })
	}
	sort.Slice(r.Links, func(i, j int) bool { return r.Links[i].Path < r.Links[j].Path })
	sort.Slice(r.Others, func(i, j int) bool { return r.Others[i].Path < r.Others[j].Path })
	sort.Strings(r.ResultLike)
	var top []string // a results-like folder inside another one is part of it
	for _, d := range r.ResultLike {
		if len(top) == 0 || !strings.HasPrefix(d, top[len(top)-1]+"/") {
			top = append(top, d)
		}
	}
	r.ResultLike = top
	mergeJobRecords(r)
	markDiffers(r.Runs)
	// a folder holding data counts once however many files it holds
	dirs := map[string]bool{}
	for _, f := range r.Data {
		dirs[path.Dir(f.Path)] = true
	}
	r.DataFolders = len(dirs)
	for _, l := range []*[]string{&r.Skipped.Dirs, &r.Skipped.NeverRead, &r.Skipped.Archives} {
		sort.Strings(*l)
		if len(*l) > maxList {
			*l = (*l)[:maxList]
		}
	}
	if r.Runs == nil {
		r.Runs = []Run{}
	}
}

// markDiffers sets, for every run, the settings whose value is not the same across the runs in its folder.
func markDiffers(rs []Run) {
	groups := map[string][]int{}
	for i := range rs {
		d := path.Dir(rs[i].Path)
		groups[d] = append(groups[d], i)
	}
	for _, idx := range groups {
		for _, i := range idx {
			rs[i].Differs = []Setting{}
		}
		if len(idx) < 2 {
			continue
		}
		vals := map[string]map[string]bool{}
		for _, i := range idx {
			seen := map[string]bool{}
			for _, st := range rs[i].Settings {
				if vals[st.Key] == nil {
					vals[st.Key] = map[string]bool{}
				}
				vals[st.Key][st.Value] = true
				seen[st.Key] = true
			}
		}
		for _, i := range idx {
			have := map[string]bool{}
			for _, st := range rs[i].Settings {
				have[st.Key] = true
			}
			for _, st := range rs[i].Settings {
				others := len(vals[st.Key]) > 1
				if !others {
					for _, j := range idx { // same value everywhere, but a sibling that lacks the key still differs
						if j == i {
							continue
						}
						found := false
						for _, o := range rs[j].Settings {
							if o.Key == st.Key {
								found = true
								break
							}
						}
						if !found {
							others = true
							break
						}
					}
				}
				if others {
					rs[i].Differs = append(rs[i].Differs, st)
				}
			}
		}
	}
}

// budget reports whether the scan must stop, recording why.
func (s *scanner) budget() bool {
	if s.stop {
		return true
	}
	switch {
	case s.ctx.Err() != nil:
		s.stop = true
	case s.res.Entries >= s.opt.MaxEntries:
		s.stop, s.res.Truncated, s.res.TruncatedWhy = true, true, "stopped after the limit of entries was reached"
	case s.now().Sub(s.start) > s.opt.MaxDuration:
		s.stop, s.res.Truncated, s.res.TruncatedWhy = true, true, "stopped after the time limit for walking was reached"
	}
	return s.stop
}

func (s *scanner) skippedDir(rel, name string) (string, bool) {
	for _, n := range SkipNames {
		if name == n {
			return "skip", true
		}
	}
	for _, g := range s.opt.Layout.Skip {
		if MatchGlob(g, rel) {
			return "skip", true
		}
	}
	return "", false
}

// runCtx is the run a folder belongs to while it is walked.
type runCtx struct{ idx int }

// walk lists dir and classifies what is in it. in is the run the folder belongs to, if any. It returns how many runs
// were found at or under this folder.
func (s *scanner) walk(dir string, in *runCtx) int {
	if s.budget() {
		return 0
	}
	if s.opt.MaxDepth > 0 && dir != "" && strings.Count(dir, "/")+1 > s.opt.MaxDepth {
		s.res.Truncated = true
		if s.res.TruncatedWhy == "" {
			s.res.TruncatedWhy = "stopped descending below " + strconv.Itoa(s.opt.MaxDepth) + " folder levels"
		}
		return 0
	}
	ents, err := s.fs.ReadDir(dir)
	if err != nil {
		s.res.Notes = append(s.res.Notes, "could not list "+displayDir(dir))
		return 0
	}
	sort.Slice(ents, func(i, j int) bool { return ents[i].Name < ents[j].Name })
	s.res.Entries += len(ents)
	if s.opt.Progress != nil {
		s.opt.Progress(s.res.Entries, dir)
	}
	names := map[string]bool{}
	for _, e := range ents {
		if !e.Dir && !e.Link {
			names[e.Name] = true
		}
	}
	runsBefore := len(s.res.Runs)
	cur := in
	if dir != "" {
		cur = s.maybeRun(dir, ents, names, in)
	}
	for _, e := range ents {
		if s.budget() {
			break
		}
		rel := joinRel(dir, e.Name)
		if neverRead(e) { // decided from the name alone: nothing below is statted, read or listed
			s.res.Skipped.NeverRead = append(s.res.Skipped.NeverRead, rel)
			continue
		}
		if e.Dir && !e.Link {
			if _, skip := s.skippedDir(rel, e.Name); skip {
				if !strings.HasPrefix(e.Name, ".vesqira") {
					s.res.Skipped.Dirs = append(s.res.Skipped.Dirs, rel)
				}
				continue
			}
			if isZarrDir(e.Name) {
				s.addData(rel, Entry{}, cur, format{"Zarr", false})
				continue
			}
			s.walk(rel, cur)
			continue
		}
		if matchSkipFile(s.opt.Layout.Skip, rel) {
			continue
		}
		if e.Link {
			s.link(rel)
			continue
		}
		s.file(dir, rel, e, cur, names)
	}
	n := len(s.res.Runs) - runsBefore
	if dir != "" && in == nil && cur == nil && n == 0 && looksLikeResults(path.Base(dir)) && len(ents) > 0 {
		s.res.ResultLike = append(s.res.ResultLike, dir)
	}
	return n
}

func neverRead(e Entry) bool {
	switch {
	case e.Link:
		return matchAny(NeverDirs, e.Name) || matchAny(NeverFiles, e.Name)
	case e.Dir:
		return matchAny(NeverDirs, e.Name)
	}
	return matchAny(NeverFiles, e.Name)
}

func matchSkipFile(globs []string, rel string) bool {
	for _, g := range globs {
		if MatchGlob(g, rel) {
			return true
		}
	}
	return false
}

func has(es []Entry, name string) bool {
	for _, e := range es {
		if e.Name == name {
			return true
		}
	}
	return false
}

func joinRel(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}

func displayDir(d string) string {
	if d == "" {
		return "the project folder"
	}
	return d
}

func (s *scanner) link(rel string) {
	st, err := s.fs.Stat(rel)
	switch {
	case err != nil || st.LinkDangling:
		s.res.Links = append(s.res.Links, Link{rel, "dangling"})
	case st.LinkOutside:
		s.res.Links = append(s.res.Links, Link{rel, "outside"})
	default:
		s.res.Links = append(s.res.Links, Link{rel, "inside"})
	}
}

// maybeRun decides whether dir is a run (a record, a manual mark, or a folder of runs the layout declares) and
// returns the run context its contents belong to. A run inside a run wins for its own files: the outer run keeps a note.
func (s *scanner) maybeRun(dir string, ents []Entry, names map[string]bool, in *runCtx) *runCtx {
	file, kind, ok := detectRecord(names)
	man, marked := s.manual[dir]
	layoutRun := s.layRun[path.Dir(dir)] || (path.Dir(dir) == "." && s.layRun[""])
	if !ok && !marked && !layoutRun {
		return in
	}
	r := Run{Path: dir, Fields: []Field{}, Settings: []Setting{}, Notes: []string{}, Outputs: []File{}}
	if ok {
		r.Record, r.RecordKind = file, string(kind)
		if st, err := s.fs.Stat(joinRel(dir, file)); err == nil {
			r.MtimeNS, r.RecordSize = st.MtimeNS, st.Size
		}
		if !s.readRecord(&r, kind) {
			if kind == recMLflow { // meta.yaml is only a run record when it says it is an MLflow run
				if !marked && !layoutRun {
					return in
				}
				r.Record, r.RecordKind = "", string(recManual)
			}
		}
	} else {
		r.RecordKind = string(recManual)
	}
	if marked || (!ok && layoutRun) {
		src := "project.toml"
		if !marked {
			src = "project.toml [layout]"
		}
		r.Fields = append(r.Fields, Field{"name", firstNonEmpty(man.Name, path.Base(dir)), "you", src})
		if man.Commit != "" {
			r.Fields = append(r.Fields, Field{"commit", man.Commit, "you", src})
		}
		if man.StartedAt != "" {
			r.Fields = append(r.Fields, Field{"startedAt", man.StartedAt, "you", src})
		}
		if ok {
			r.Notes = append(r.Notes, "also marked as a run in project.toml; the run record's values are the ones shown")
		}
	}
	if t, c, ok := fromFolderName(path.Base(dir)); ok {
		if !has2(r.Fields, "startedAt") {
			r.Fields = append(r.Fields, Field{"startedAt", t.Format(time.RFC3339), "inferred", "folder name"})
		}
		if !has2(r.Fields, "commit") {
			r.Fields = append(r.Fields, Field{"commit", c, "inferred", "folder name"})
		}
	}
	if in != nil {
		outer := &s.res.Runs[in.idx]
		outer.Notes = append(outer.Notes, "contains the run "+dir+", whose files belong to it, not to this run")
		r.Notes = append(r.Notes, "this run is inside the run "+outer.Path+"; its own files are counted here")
	}
	s.res.Runs = append(s.res.Runs, r)
	return &runCtx{idx: len(s.res.Runs) - 1}
}

func has2(fs []Field, key string) bool {
	for _, f := range fs {
		if f.Key == key {
			return true
		}
	}
	return false
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// readRecord fills a run from its record. It returns false when the record turned out not to be a run record.
func (s *scanner) readRecord(r *Run, kind recordKind) bool {
	data, truncated, err := s.fs.ReadFile(joinRel(r.Path, r.Record), runs.MaxMetadataBytes)
	if err != nil {
		r.Notes = append(r.Notes, "the run record "+r.Record+" could not be read")
		return true
	}
	if truncated {
		r.Notes = append(r.Notes, "the run record "+r.Record+" is too large to read here; its settings are not shown")
		return true
	}
	switch kind {
	case recVGT:
		p, err := runs.ParseVGT(data, path.Base(r.Path))
		if err != nil {
			r.Notes = append(r.Notes, "run_metadata.toml could not be read as a VGT run record: "+err.Error())
			return true
		}
		if p.GeneratedAt != "" {
			r.Fields = append(r.Fields, Field{"startedAt", p.GeneratedAt, "declared", r.Record})
		}
		if p.GitCommit != "" {
			r.Fields = append(r.Fields, Field{"commit", p.GitCommit, "declared", r.Record})
		}
		for _, p := range p.Params {
			r.Settings = append(r.Settings, Setting{p.Key, p.Value})
		}
		if len(p.OutputFiles) > 0 {
			r.OutputRoles = map[string]string{}
			for role, name := range p.OutputFiles {
				r.OutputRoles[name] = role
			}
		}
	case recROHSA:
		stem := strings.TrimSuffix(r.Record, "_run_trace.toml")
		p, err := runs.ParseROHSA(data, nil, nil, stem, path.Base(r.Path))
		if err != nil {
			r.Notes = append(r.Notes, r.Record+" could not be read as a ROHSA run trace: "+err.Error())
			return true
		}
		for _, p := range p.Params {
			r.Settings = append(r.Settings, Setting{p.Key, p.Value})
		}
		r.Notes = append(r.Notes, "a ROHSA fit: its raw output is "+stem+".dat; other fits in this folder share this run record's folder")
		// the folder may also hold the record Vesqira wrote for the job that made the fit: the same run, named once
		if rec, trunc, err := s.fs.ReadFile(joinRel(r.Path, "run.json"), runs.MaxMetadataBytes); err == nil && !trunc {
			if id, tpl, ok := jobOf(rec); ok && tpl == "rohsa" {
				r.jobID, r.jobTemplate = id, tpl
				addJob(r, id, "run.json")
			}
		}
	case recProvenance, recRunJSON:
		s.fromJSON(r, data)
		if kind == recRunJSON {
			r.jobID, r.jobTemplate, _ = jobOf(data)
		}
	case recParams:
		if strings.HasSuffix(r.Record, ".json") {
			s.fromJSON(r, data)
		} else {
			s.fromYAML(r, data)
		}
	case recMLflow:
		if !strings.Contains(string(data), "run_id") {
			return false
		}
		s.fromYAML(r, data)
	}
	return true
}

var commitKeys = map[string]bool{"commit": true, "git_commit": true, "gitcommit": true, "git_sha": true, "sha": true}
var timeKeys = map[string]bool{"started_at": true, "start_time": true, "timestamp": true, "generated_at": true, "generated_at_utc": true, "created_at": true}
var settingBlocks = []string{"params", "parameters", "config", "settings", "hyperparameters", "run_parameters"}

// fromJSON reads settings from a JSON run record: scalar entries at the top and in the usual settings blocks. A commit
// or a time is a field the record declared; locations are dropped, as run import drops them.
func (s *scanner) fromJSON(r *Run, data []byte) {
	var m map[string]any
	if json.Unmarshal(data, &m) != nil {
		r.Notes = append(r.Notes, r.Record+" is not valid JSON; no settings are shown")
		return
	}
	s.fromMap(r, m)
}

// fromYAML reads a YAML run record the way fromJSON reads a JSON one; what it shows is declared by the record.
func (s *scanner) fromYAML(r *Run, data []byte) {
	m, err := parseYAMLRecord(data)
	if err != nil {
		r.Notes = append(r.Notes, r.Record+" "+err.Error()+"; no settings are shown")
		return
	}
	s.fromMap(r, m)
}

func (s *scanner) fromMap(r *Run, m map[string]any) {
	top := map[string]any{}
	for k, v := range m {
		l := strings.ToLower(k)
		switch {
		case commitKeys[l]:
			if sv, ok := v.(string); ok && sv != "" && len(sv) <= 80 {
				r.Fields = append(r.Fields, Field{"commit", sv, "declared", r.Record})
			}
		case timeKeys[l]:
			if sv, ok := v.(string); ok && sv != "" && len(sv) <= 80 {
				r.Fields = append(r.Fields, Field{"startedAt", sv, "declared", r.Record})
			}
		default:
			top[k] = v
		}
	}
	var ps []runs.Param
	ps = append(ps, runs.Settings(top, "record")...)
	for _, b := range settingBlocks {
		for k, v := range m {
			if strings.EqualFold(k, b) {
				if sub, ok := v.(map[string]any); ok {
					ps = append(ps, runs.Settings(sub, "record")...)
				}
			}
		}
	}
	seen := map[string]bool{}
	for _, p := range ps {
		if seen[p.Key] || len(r.Settings) >= 100 {
			continue
		}
		seen[p.Key] = true
		r.Settings = append(r.Settings, Setting{p.Key, p.Value})
	}
	sort.Slice(r.Settings, func(i, j int) bool { return r.Settings[i].Key < r.Settings[j].Key })
}

// file classifies one regular file. Files inside a run belong to the run.
func (s *scanner) file(dir, rel string, e Entry, in *runCtx, names map[string]bool) {
	if in != nil {
		r := &s.res.Runs[in.idx]
		if dir == r.Path && e.Name == r.Record {
			return
		}
		st, err := s.fs.Stat(rel)
		if err != nil {
			return
		}
		r.OutputCount++
		r.OutputBytes += st.Size
		if f, ok := dataFormat(e.Name); ok {
			if len(r.Outputs) < maxRunOutputs {
				r.Outputs = append(r.Outputs, File{Path: rel, Size: st.Size, MtimeNS: st.MtimeNS, Format: f.Name, Viewer: f.Viewer})
			}
		} else {
			s.other(rel, st, r.Path)
		}
		return
	}
	if f, ok := dataFormat(e.Name); ok {
		s.addData(rel, e, in, f)
		return
	}
	if s.isCatalog(rel, e.Name) {
		s.addData(rel, e, in, format{"Catalog", true})
		return
	}
	if isArchive(e.Name) {
		st, err := s.fs.Stat(rel) // the size only: an archive is never opened
		if err == nil {
			s.res.Skipped.ArchiveBytes += st.Size
		}
		s.res.Skipped.ArchiveCount++
		if len(s.res.Skipped.Archives) < maxList {
			s.res.Skipped.Archives = append(s.res.Skipped.Archives, rel)
		}
		return
	}
	switch {
	case isConfigFile(e.Name) && (underConfigDir(rel) || within(rel, s.layCfg) || (dir == "" && strings.HasPrefix(strings.ToLower(e.Name), "config") && strings.HasSuffix(strings.ToLower(e.Name), ".toml"))):
		s.addPlain(&s.res.Configs, rel)
		return
	case hasJobExt(e.Name) && (underAny(rel, jobDirs) || within(rel, s.layJob)):
		s.addPlain(&s.res.Jobs, rel)
		return
	case strings.HasSuffix(strings.ToLower(e.Name), ".sh") && (underAny(rel, jobDirs) || within(rel, s.layJob)):
		if within(rel, s.layJob) { // the researcher said this folder holds jobs
			s.addPlain(&s.res.Jobs, rel)
			return
		}
		if head, _, err := s.fs.ReadFile(rel, jobPeek); err == nil && strings.Contains(string(head), "#SBATCH") {
			s.addPlain(&s.res.Jobs, rel)
			return
		}
	case within(rel, s.layDat):
		s.addData(rel, e, in, format{"other", false})
		return
	}
	s.res.OtherFiles++
	if s.opt.Inventory {
		st, _ := s.fs.Stat(rel)
		s.other(rel, st, "")
	}
}

// other records a file nothing recognised, when the inventory is asked for.
func (s *scanner) other(rel string, st Entry, runPath string) {
	if !s.opt.Inventory {
		return
	}
	if len(s.res.Others) >= MaxInventory {
		s.res.OthersTruncated = true
		return
	}
	s.res.Others = append(s.res.Others, Other{Path: rel, Size: st.Size, MtimeNS: st.MtimeNS, RunPath: runPath})
}

// isCatalog says whether a file is offered as a point catalog (ADR 0044) instead of "other discipline". The extensions that only
// tables use (.ecsv, .vot) count anywhere. The plain-text ones (.csv, .tsv, .dat, .txt, ...) also hold READMEs and logs, so they
// count only in a folder the researcher declared as data and only when the first lines look like a table: the same number of
// columns on nearly every line.
func (s *scanner) isCatalog(rel, name string) bool {
	switch l := strings.ToLower(name); {
	case !s.opt.Catalogs:
		return false
	case strings.HasSuffix(l, ".ecsv"), strings.HasSuffix(l, ".vot"), strings.HasSuffix(l, ".votable"):
		return true
	case !within(rel, s.layDat):
		return false
	case hasAnySuffix(l, ".csv", ".tsv", ".dat", ".txt", ".tbl", ".cat", ".xcat"):
		head, truncated, err := s.fs.ReadFile(rel, jobPeek)
		return err == nil && looksTabular(head, truncated)
	}
	return false
}

func hasAnySuffix(s string, suffixes ...string) bool {
	for _, x := range suffixes {
		if strings.HasSuffix(s, x) {
			return true
		}
	}
	return false
}

// looksTabular reports whether the head of a text file reads as a table: an ECSV or IPAC marker, or at least two lines that split
// into the same two or more fields (tab, comma or blanks; trailing empty fields do not count) on most lines. Comment lines
// are ignored. It decides only what is offered; the worker still reads and bounds the file when it is registered.
func looksTabular(head []byte, truncated bool) bool {
	text := string(head)
	if strings.HasPrefix(strings.TrimLeft(text, "\ufeff \t\r\n"), "# %ECSV") {
		return true
	}
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if truncated && len(lines) > 1 {
		lines = lines[:len(lines)-1] // the last line was cut
	}
	var rows []string
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		switch {
		case t == "", strings.HasPrefix(t, `\`):
		case strings.HasPrefix(t, "|"):
			return true // IPAC
		case strings.HasPrefix(t, "#"):
		default:
			rows = append(rows, ln)
		}
		if len(rows) == 20 {
			break
		}
	}
	if len(rows) < 2 {
		return false
	}
	split := func(ln string) int {
		var f []string
		switch {
		case strings.Contains(ln, "\t"):
			f = strings.Split(ln, "\t")
		case strings.Contains(ln, ","):
			f = strings.Split(ln, ",")
		default:
			f = strings.Fields(ln)
		}
		for len(f) > 0 && strings.TrimSpace(f[len(f)-1]) == "" {
			f = f[:len(f)-1]
		}
		return len(f)
	}
	counts := map[int]int{}
	best := 0
	for _, ln := range rows {
		n := split(ln)
		counts[n]++
		if counts[n] > counts[best] {
			best = n
		}
	}
	return best >= 2 && counts[best]*5 >= len(rows)*4
}

func (s *scanner) addPlain(to *[]File, rel string) {
	f := File{Path: rel}
	if st, err := s.fs.Stat(rel); err == nil {
		f.Size, f.MtimeNS = st.Size, st.MtimeNS
	}
	*to = append(*to, f)
}

func (s *scanner) addData(rel string, e Entry, in *runCtx, f format) {
	file := File{Path: rel, Format: f.Name, Viewer: f.Viewer}
	if st, err := s.fs.Stat(rel); err == nil {
		file.Size, file.MtimeNS = st.Size, st.MtimeNS
	}
	if role := roleOf(rel); role != "" {
		file.Role = role
		file.ReadOnly = RoleIsReadOnly(role)
	}
	s.res.Data = append(s.res.Data, file)
}

// git reads the commit from .git/HEAD (a small file, no git program needed, the same way locally and through a host).
func (s *scanner) git() {
	ents, err := s.fs.ReadDir("")
	if err != nil || !has(ents, ".git") {
		return
	}
	s.res.Git.Present = true
	head, _, err := s.fs.ReadFile(".git/HEAD", 512)
	if err != nil {
		return
	}
	h := strings.TrimSpace(string(head))
	if ref, ok := strings.CutPrefix(h, "ref: "); ok {
		ref = strings.TrimSpace(ref)
		if !strings.HasPrefix(ref, "refs/") || strings.Contains(ref, "..") {
			return
		}
		if b, _, err := s.fs.ReadFile(".git/"+ref, 128); err == nil {
			h = strings.TrimSpace(string(b))
		} else if pr, _, err := s.fs.ReadFile(".git/packed-refs", MaxReadBytes); err == nil {
			h = ""
			for _, line := range strings.Split(string(pr), "\n") {
				if f := strings.Fields(line); len(f) == 2 && f[1] == ref {
					h = f[0]
				}
			}
		} else {
			h = ""
		}
	}
	if len(h) >= 7 && isHex(h) {
		s.res.Git.Commit = h[:7]
	}
	if s.opt.Dirty != nil {
		if n, ok := s.opt.Dirty(s.ctx); ok {
			s.res.Git.Dirty = &n
		}
	}
}

func isHex(s string) bool {
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return s != ""
}

// ProposedProject is the project.toml text proposed for a project with no file yet. The layout lists stay empty on
// purpose: they change how a folder is read, so they are the researcher's to fill in. What discovery found is shown as
// comments only, which keeps what was found apart from what was declared.
func ProposedProject(name string, r *Result) string {
	var b strings.Builder
	b.WriteString("# Proposed by Vesqira. Edit freely. Vesqira writes this file only when you confirm, never adds it to version\n")
	b.WriteString("# control, and reads it again when it changes. What was only discovered is not stored here, only what you declare.\n\n")
	b.WriteString("[project]\nname = " + quote(name) + "\n\n")
	b.WriteString("[layout]\n# data: folders whose files are all listed as data; runs: folders whose subfolders are all runs;\n# configs, jobs: extra folders to search; skip: patterns such as \"old/**\".\n")
	b.WriteString("# found: data " + comment(topFolders(filePaths(r.Data), 2)) + "\n")
	b.WriteString("# found: runs " + comment(runParents(r.Runs)) + "\n")
	b.WriteString("# found: configs " + comment(topFolders(filePaths(r.Configs), 2)) + "\n")
	b.WriteString("# found: jobs " + comment(topFolders(filePaths(r.Jobs), 2)) + "\n")
	b.WriteString("data = []\nruns = []\nconfigs = []\njobs = []\nskip = []\n")
	return b.String()
}

func comment(items []string) string {
	if len(items) == 0 {
		return "(none)"
	}
	if len(items) > 8 {
		items = append(items[:8:8], "...")
	}
	return strings.ReplaceAll(strings.Join(items, ", "), "\n", " ")
}

func quote(s string) string {
	var b strings.Builder
	_ = toml.NewEncoder(&b).Encode(map[string]string{"v": s})
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(b.String()), "v = "))
}

func filePaths(fs []File) []string {
	out := make([]string, len(fs))
	for i, f := range fs {
		out[i] = f.Path
	}
	return out
}

// topFolders returns the distinct folders that hold the files, cut to depth path components.
func topFolders(files []string, depth int) []string {
	set := map[string]bool{}
	for _, f := range files {
		parts := strings.Split(path.Dir(f), "/")
		if parts[0] == "." {
			continue
		}
		if len(parts) > depth {
			parts = parts[:depth]
		}
		set[strings.Join(parts, "/")] = true
	}
	var out []string
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func runParents(rs []Run) []string {
	set := map[string]bool{}
	for _, r := range rs {
		if p := path.Dir(r.Path); p != "." {
			set[p] = true
		}
	}
	var out []string
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// jobOf reads which Vesqira job a run.json was written for. Only a record carrying Vesqira's own marker counts: any other
// run.json (another program's, a hand-made one) names no job here.
func jobOf(data []byte) (id int64, template string, ok bool) {
	var rec struct {
		Vesqira struct {
			Record string `json:"record"`
			Job    struct {
				ID       int64  `json:"id"`
				Template string `json:"template"`
			} `json:"job"`
		} `json:"vesqira"`
	}
	if json.Unmarshal(data, &rec) != nil || rec.Vesqira.Record != "vesqira-run-record" || rec.Vesqira.Job.ID <= 0 {
		return 0, "", false
	}
	return rec.Vesqira.Job.ID, rec.Vesqira.Job.Template, true
}

func addJob(r *Run, id int64, source string) {
	if !has2(r.Fields, "job") {
		r.Fields = append(r.Fields, Field{"job", "Vesqira job " + strconv.FormatInt(id, 10), "declared", source})
	}
}

// mergeJobRecords makes a ROHSA job and its fit one run. Vesqira writes run.json in the job's output folder, and the pipeline
// writes the fit (with its trace) in that folder or a folder below it, so a scan would list two runs for one job: the record
// and, nested inside it, the fit. When a run.json written for a ROHSA job has ROHSA runs below it, the record is not listed;
// each fit keeps its own settings (the pipeline's trace) and gains the job it came from. A record with no fit below it (the job
// has not produced one yet, or failed) is still listed: it is all there is.
func mergeJobRecords(res *Result) {
	drop := map[int]bool{}
	for i := range res.Runs {
		outer := res.Runs[i]
		if outer.RecordKind != string(recRunJSON) || outer.jobTemplate != "rohsa" {
			continue
		}
		found := false
		for j := range res.Runs {
			in := &res.Runs[j]
			if j == i || in.RecordKind != string(recROHSA) || in.Path == outer.Path || (outer.Path != "" && !strings.HasPrefix(in.Path, outer.Path+"/")) {
				continue
			}
			found = true
			src := joinRel(outer.Path, outer.Record)
			addJob(in, outer.jobID, src)
			kept := in.Notes[:0]
			for _, n := range in.Notes {
				if !strings.HasPrefix(n, "this run is inside the run "+outer.Path) {
					kept = append(kept, n)
				}
			}
			in.Notes = append(kept, "Vesqira's record of the job that made this fit ("+src+") is part of this run, not a second one")
		}
		if found {
			drop[i] = true
		}
	}
	if len(drop) == 0 {
		return
	}
	kept := res.Runs[:0]
	for i, r := range res.Runs {
		if !drop[i] {
			kept = append(kept, r)
		}
	}
	res.Runs = kept
}
