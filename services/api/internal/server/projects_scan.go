package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/discover"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/projects"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

const maxRegisterPerScan = 5000

// scanState is what one project's scan is doing now: walking, then registering FITS headers in the background.
type scanState struct {
	mu sync.Mutex
	scanView
	cancel context.CancelFunc
}

// scanView is what the API says about a scan.
type scanView struct {
	State      string     `json:"state"` // idle | scanning | registering | done | canceled | error
	Entries    int        `json:"entries"`
	ToRegister int        `json:"toRegister"`
	Registered int        `json:"registered"`
	Failed     int        `json:"failed"`
	StartedAt  *time.Time `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt"`
	Error      string     `json:"error"`
}

func (st *scanState) snapshot() scanView {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.scanView
}

func (st *scanState) active() bool { return st.State == "scanning" || st.State == "registering" }

func (s *Server) scanOf(id int64) *scanState {
	s.projMu.Lock()
	defer s.projMu.Unlock()
	if s.projScans == nil {
		s.projScans = map[int64]*scanState{}
	}
	st := s.projScans[id]
	if st == nil {
		st = &scanState{scanView: scanView{State: "idle"}}
		s.projScans[id] = st
	}
	return st
}

// startScan begins a scan unless one is running for the project. It returns the state and whether it started one.
// reqCtx is the request that starts it: the scan outlives the request but acts as its actor, in its workspace.
func (s *Server) startScan(reqCtx context.Context, p projects.Project) (*scanState, bool) {
	st := s.scanOf(p.ID)
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.active() {
		return st, false
	}
	ctx, cancel := context.WithCancel(context.WithoutCancel(reqCtx))
	now := time.Now()
	st.State, st.Entries, st.ToRegister, st.Registered, st.Failed, st.Error, st.StartedAt, st.FinishedAt, st.cancel = "scanning", 0, 0, 0, 0, "", &now, nil, cancel
	go s.runScan(ctx, p, st)
	return st, true
}

func (st *scanState) set(f func()) {
	st.mu.Lock()
	f()
	st.mu.Unlock()
}

func (st *scanState) finish(state, errMsg string) {
	st.set(func() {
		now := time.Now()
		st.State, st.Error, st.FinishedAt = state, errMsg, &now
		if st.cancel != nil {
			st.cancel()
		}
	})
}

// storedSummary is what a scan leaves in the project row; the summary the API returns adds the live scan, the health list
// and the needs count to it.
type storedSummary struct {
	ScannedAt    time.Time    `json:"scannedAt"`
	Counts       counts       `json:"counts"`
	Git          discover.Git `json:"git"`
	Truncated    bool         `json:"truncated"`
	TruncatedWhy string       `json:"truncatedWhy"`
	Entries      int          `json:"entries"`
	Notes        []string     `json:"notes"`
	Links        struct {
		Outside  int             `json:"outside"`
		Dangling int             `json:"dangling"`
		Inside   int             `json:"inside"`
		Examples []discover.Link `json:"examples"` // the ones that point outside or nowhere, at most 20
	} `json:"links"`
	ResultLike  []string `json:"resultLike"`
	Changes     *changes `json:"changes"`
	ProjectFile struct {
		Exists    bool              `json:"exists"`
		Error     string            `json:"error"`
		Name      string            `json:"name"`
		HostRepos map[string]string `json:"hostRepos"`
	} `json:"projectFile"`
	Proposed string `json:"proposedProjectFile"`
	// ImportRules counts what the project's saved import rules select (ADR 0058); nil when the project has none.
	ImportRules  *importSummary `json:"importRules"`
	Registration struct {
		Registered int      `json:"registered"`
		Failed     int      `json:"failed"`
		Errors     []string `json:"errors"`
		Capped     bool     `json:"capped"`
	} `json:"registration"`
}

type counts struct {
	Runs        int            `json:"runs"`
	ManualRuns  int            `json:"manualRuns"` // runs without a record: marked by you in project.toml
	Data        int            `json:"data"`
	Configs     int            `json:"configs"`
	Jobs        int            `json:"jobs"`
	RunOutputs  int            `json:"runOutputs"`
	DataFolders int            `json:"dataFolders"`
	ByFormat    map[string]int `json:"byFormat"`
	NoViewer    int            `json:"noViewer"` // data files in a format Vesqira cannot show yet
	OtherFiles  int            `json:"otherFiles"`
	Archives    struct {
		Count int   `json:"count"`
		Bytes int64 `json:"bytes"`
	} `json:"archives"`
	SkippedDirs int `json:"skippedDirs"`
	NeverRead   int `json:"neverRead"`
	RoleRaw     int `json:"roleRaw"` // data files in a raw or external folder (read-only inputs)
}

type changes struct {
	Added   int `json:"added"`
	Removed int `json:"removed"`
	Changed int `json:"changed"`
}

func fingerprint(size, mtime int64) string { return fmt.Sprintf("%d|%d", size, mtime) }

// fingerprints lists what a scan saw for each file and run record, for the next rescan to compare with (size and
// modification time only: no hashing, no watching).
func fingerprints(r *discover.Result) map[string]string {
	m := map[string]string{}
	for _, f := range r.Data {
		m[f.Path] = fingerprint(f.Size, f.MtimeNS)
	}
	for _, f := range r.Configs {
		m[f.Path] = fingerprint(f.Size, f.MtimeNS)
	}
	for _, f := range r.Jobs {
		m[f.Path] = fingerprint(f.Size, f.MtimeNS)
	}
	for _, run := range r.Runs {
		if run.Record != "" {
			m[run.Path+"/"+run.Record] = fingerprint(run.RecordSize, run.MtimeNS)
		}
		for _, o := range run.Outputs {
			m[o.Path] = fingerprint(o.Size, o.MtimeNS)
		}
	}
	return m
}

// previousFingerprints reads the same from the stored rows of the last scan.
func (s *Server) previousFingerprints(id int64) map[string]string {
	rows, err := s.Projects.Files(id)
	if err != nil {
		return nil
	}
	m := map[string]string{}
	for _, r := range rows {
		var f struct {
			Size       int64  `json:"size"`
			MtimeNS    int64  `json:"mtimeNs"`
			RecordSize int64  `json:"recordSize"`
			Record     string `json:"record"`
		}
		if json.Unmarshal(r.Data, &f) != nil {
			continue
		}
		if r.Kind == "run" {
			if f.Record != "" {
				m[r.Path+"/"+f.Record] = fingerprint(f.RecordSize, f.MtimeNS)
			}
			continue
		}
		m[r.Path] = fingerprint(f.Size, f.MtimeNS)
	}
	return m
}

func diffFingerprints(prev, cur map[string]string) *changes {
	if prev == nil {
		return nil
	}
	c := &changes{}
	for p, fp := range cur {
		if old, ok := prev[p]; !ok {
			c.Added++
		} else if old != fp {
			c.Changed++
		}
	}
	for p := range prev {
		if _, ok := cur[p]; !ok {
			c.Removed++
		}
	}
	return c
}

func summarize(r *discover.Result) storedSummary {
	var sum storedSummary
	sum.Counts.Runs, sum.Counts.Data, sum.Counts.Configs, sum.Counts.Jobs = len(r.Runs), len(r.Data), len(r.Configs), len(r.Jobs)
	sum.Counts.ByFormat = map[string]int{}
	for _, d := range r.Data {
		sum.Counts.ByFormat[d.Format]++
		if !d.Viewer {
			sum.Counts.NoViewer++
		}
		if d.ReadOnly {
			sum.Counts.RoleRaw++
		}
	}
	for _, run := range r.Runs {
		sum.Counts.RunOutputs += run.OutputCount
		if run.RecordKind == "manual" {
			sum.Counts.ManualRuns++
		}
	}
	sum.Counts.DataFolders, sum.Counts.OtherFiles = r.DataFolders, r.OtherFiles
	sum.Counts.Archives.Count, sum.Counts.Archives.Bytes = r.Skipped.ArchiveCount, r.Skipped.ArchiveBytes
	sum.Counts.SkippedDirs, sum.Counts.NeverRead = len(r.Skipped.Dirs), len(r.Skipped.NeverRead)
	sum.Git, sum.Truncated, sum.TruncatedWhy, sum.Entries, sum.Notes = r.Git, r.Truncated, r.TruncatedWhy, r.Entries, append([]string{}, r.Notes...)
	sum.ResultLike = append([]string{}, r.ResultLike...)
	sum.Links.Examples = []discover.Link{}
	for _, l := range r.Links {
		switch l.State {
		case "outside":
			sum.Links.Outside++
		case "dangling":
			sum.Links.Dangling++
		default:
			sum.Links.Inside++
			continue
		}
		if len(sum.Links.Examples) < 20 {
			sum.Links.Examples = append(sum.Links.Examples, l)
		}
	}
	return sum
}

func (s *Server) updateSummary(ws workspace.ID, id int64, f func(*storedSummary)) {
	p, err := s.Projects.Get(ws, id)
	if err != nil {
		return
	}
	var sum storedSummary
	_ = json.Unmarshal(p.Summary, &sum)
	f(&sum)
	b, _ := json.Marshal(sum)
	if err := s.Projects.SetSummary(ws, id, b); err != nil {
		log.Printf("projects: %v", err)
	}
}

// manualFromAnswers are the folders the researcher marked as runs with an answer, whether or not it reached project.toml.
func manualFromAnswers(answers map[string]projects.Answer) []discover.ManualRun {
	var out []discover.ManualRun
	for need, a := range answers {
		if p, ok := strings.CutPrefix(need, "results:"); ok && a.Answer == "mark_run" {
			out = append(out, discover.ManualRun{Path: p})
		}
	}
	return out
}

func (s *Server) lane() chan struct{} {
	s.laneOnce.Do(func() { s.heavy = make(chan struct{}, 1) })
	return s.heavy
}

// runScan walks the folder, stores what it found, then registers the FITS headers in the background (one at a time on
// the heavy lane), reporting progress and stopping when cancelled.
func (s *Server) runScan(ctx context.Context, p projects.Project, st *scanState) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("projects: scan panicked: %v", r)
			st.finish("error", "the scan failed")
		}
	}()
	var (
		fsys   discover.FS
		h      hosts.Host
		remote = p.HostID != 0
	)
	var mem *discover.MemFS
	if remote {
		var err error
		if s.HostDB == nil || s.Remote == nil {
			st.finish("error", "remote hosts are not configured")
			return
		}
		if h, err = s.HostDB.Get(p.HostID); err != nil {
			st.finish("error", "the host of this project was removed")
			return
		}
		mem = discover.NewMemFS(s.remoteReader(ctx, h, p.Path))
		fsys = mem
	} else {
		fsys = discover.LocalFS{Root: p.Path}
	}
	pf, fileExists, _, perr := readProjectFile(fsys)
	answers, _ := s.Projects.Answers(p.ID)
	manual := append(pf.ManualRuns(), manualFromAnswers(answers)...)
	opt := discover.Options{Layout: pf.ScanLayout(), ManualRuns: manual, Inventory: true, Progress: func(n int, _ string) { st.set(func() { st.Entries = n }) }}
	if !remote {
		opt.Dirty = localDirty(p.Path)
		opt.Catalogs = true
	}
	var walkTruncated bool
	var walkWhy string
	if remote {
		var err error
		walkTruncated, walkWhy, err = s.walkRemote(ctx, h, p.Path, pf.ScanLayout().Skip, mem, discover.DefaultMaxEntries, discover.DefaultMaxDuration, func(n int) { st.set(func() { st.Entries = n }) })
		if err != nil {
			if ctx.Err() != nil {
				st.finish("canceled", "")
				return
			}
			_, msg := hostErrStatus(err)
			st.finish("error", msg)
			return
		}
	}
	res := discover.Scan(ctx, fsys, opt)
	if ctx.Err() != nil {
		st.finish("canceled", "") // the previous scan stays as it was
		return
	}
	if walkTruncated {
		res.Truncated, res.TruncatedWhy = true, walkWhy
	}
	prev := s.previousFingerprints(p.ID)
	cur := fingerprints(res)
	sum := summarize(res)
	sum.ScannedAt = time.Now().UTC()
	sum.Changes = diffFingerprints(prev, cur)
	sum.ProjectFile.Exists, sum.ProjectFile.Name = fileExists, pf.Project.Name
	sum.ProjectFile.HostRepos = map[string]string{}
	for alias, hr := range pf.Hosts {
		if hr.Repo != "" {
			sum.ProjectFile.HostRepos[alias] = hr.Repo
		}
	}
	if perr != nil {
		sum.ProjectFile.Error = perr.Error()
	}
	if !fileExists {
		sum.Proposed = discover.ProposedProject(p.Name, res)
	}
	// answers the file holds are the researcher's own: keep them in the catalog too, so the file and the catalog agree
	for _, a := range pf.Answers {
		_ = s.Projects.SetAnswer(p.ID, projects.Answer{Need: a.Need, Answer: a.Answer, Value: a.Value, DeclaredBy: a.DeclaredBy, Date: a.Date, Written: true})
	}
	b, _ := json.Marshal(sum)
	if err := s.Projects.SaveScan(wsCtx(ctx), p.ID, time.Now(), b, projects.Rows(res)); err != nil {
		log.Printf("projects: %v", err)
		st.finish("error", "internal error")
		return
	}
	ignored := s.importAfterScan(wsCtx(ctx), p, pf, res) // counts what the rules select; registers and declares nothing
	s.registerFound(ctx, p, h, res, prev, st, ignored)
}

// register imports the VGT runs (the existing importer: a run found again by its folder is the same run) and registers
// every other FITS file whose header is not in the catalog as it is on disk now. Files already registered and unchanged
// are skipped, so a rescan costs one catalog read, not one header per file.
func (s *Server) registerFound(ctx context.Context, p projects.Project, h hosts.Host, res *discover.Result, prev map[string]string, st *scanState, ignored map[string]bool) {
	remote := p.HostID != 0
	abs := func(rel string) string {
		if remote {
			return hostPath(p.Path, rel)
		}
		return filepath.Join(p.Path, filepath.FromSlash(rel))
	}
	type task struct {
		run  *discover.Run
		file string
		size int64
		mt   int64
	}
	var tasks []task
	if s.Runs != nil {
		for i := range res.Runs {
			r := &res.Runs[i]
			if !s.importsFromRecord(r) {
				continue
			}
			key := abs(r.Path)
			if remote {
				key = hostKey(h.ID, key)
			}
			id, _ := s.Runs.IDByDir(wsCtx(ctx), key)
			if id != 0 && prev != nil && prev[r.Path+"/"+r.Record] == fingerprint(r.RecordSize, r.MtimeNS) {
				continue // imported before and its record has not changed
			}
			tasks = append(tasks, task{run: r})
		}
	}
	nRuns := len(tasks)
	facts, _ := s.Store.FactsByPath(wsCtx(ctx), p.HostID)
	var fits []discover.File
	for _, d := range res.Data {
		if ignored[d.Path] {
			continue // the researcher's rule says not to register it
		}
		if d.Format == "FITS" || d.Format == "Catalog" { // registerPath tells the two apart
			fits = append(fits, d)
		}
	}
	for _, r := range res.Runs {
		for _, o := range r.Outputs {
			if o.Format == "FITS" && !s.importsFromRecord(&r) { // a run whose profile has its record name the outputs registers those
				fits = append(fits, o)
			}
		}
	}
	capped := false
	for _, f := range fits {
		if fa, ok := facts[abs(f.Path)]; ok && fa.Size == f.Size && fa.MtimeNS == f.MtimeNS {
			continue
		}
		if len(tasks)-nRuns >= maxRegisterPerScan {
			capped = true
			break
		}
		tasks = append(tasks, task{file: f.Path, size: f.Size, mt: f.MtimeNS})
	}
	st.set(func() { st.State, st.ToRegister = "registering", len(tasks) })
	var errs []string
	failed := 0
	for _, t := range tasks {
		if ctx.Err() != nil {
			break
		}
		select {
		case s.lane() <- struct{}{}:
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		var code int
		var msg, what string
		func() {
			defer func() { <-s.lane() }()
			tctx, cancel := context.WithTimeout(ctx, 60*time.Second)
			defer cancel()
			switch {
			case t.run != nil:
				what = t.run.Path
				src := runSource(localRun{s: s, dir: abs(t.run.Path), meta: abs(t.run.Path + "/" + t.run.Record)})
				if remote {
					src = remoteRun{s: s, h: h, dir: abs(t.run.Path)}
				}
				_, code, msg = s.importFrom(tctx, src)
			case remote:
				what = t.file
				_, code, msg = s.registerRemoteFile(tctx, h, abs(t.file))
			default:
				what = t.file
				real, err := s.roots(wsCtx(ctx)).Resolve(abs(t.file))
				if err != nil {
					code, msg = 403, "not available"
					return
				}
				_, code, msg = s.registerPath(tctx, real)
			}
		}()
		st.set(func() {
			if code != 0 {
				st.Failed++
			} else {
				st.Registered++
			}
		})
		if code != 0 {
			failed++
			if len(errs) < 20 {
				errs = append(errs, what+": "+msg)
			}
		}
	}
	st.mu.Lock()
	reg, fl := st.Registered, st.Failed
	st.mu.Unlock()
	s.updateSummary(wsCtx(ctx), p.ID, func(sum *storedSummary) {
		sum.Registration.Registered, sum.Registration.Failed, sum.Registration.Errors, sum.Registration.Capped = reg, fl, errs, capped
	})
	if ctx.Err() != nil {
		st.finish("canceled", "")
		return
	}
	st.finish("done", "")
}
