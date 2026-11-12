package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/discover"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/projects"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// Generic import (ADR 0058): a read-only scan preview of a project folder (what the built-in rules recognise and what they do not,
// with the role each file would get and why), the researcher's own rules (a pattern on the relative path to a role and
// declarations, first match wins) with a live preview of what they match, and an explicit apply that registers what the rules
// select through the existing registration paths and applies their declarations as "declared by your rule". Nothing here runs by
// itself: a scan only counts what the rules would select.

const (
	importDepth         = 24                // folder levels the preview walks below the project folder
	importApplyMax      = 200               // files one apply request handles; the rest is reported as remaining
	importApplyDeadline = 150 * time.Second // and the time it may take
	importCacheTTL      = 10 * time.Minute
	importSamples       = 12  // example files listed per group
	importExpandMax     = 500 // files listed when a group is opened
)

// importInv is the last walk of a project for the import preview, kept in memory so that editing a rule is answered from it
// (the matching runs on the server, with the one glob and regex matcher; nothing is walked again until asked).
type importInv struct {
	folder          string
	at              time.Time
	items           []discover.Item
	entries         int
	truncated       bool
	why             string
	othersTruncated bool
	notes           []string
	skipped         discover.Skipped
	links           int
}

func cleanFolder(f string) (string, bool) {
	f = strings.Trim(strings.ReplaceAll(strings.TrimSpace(f), `\`, "/"), "/")
	if f == "" || f == "." {
		return "", true
	}
	if len(f) > 1024 || strings.ContainsRune(f, 0) {
		return "", false
	}
	for _, p := range strings.Split(f, "/") {
		if p == "" || p == "." || p == ".." {
			return "", false
		}
	}
	return f, true
}

// walkForImport walks the project (from folder) with the inventory on, on this computer or through the host's worker, under the
// walker's budgets and a depth cap. It authorises nothing new: the project folder is already a source root.
func (s *Server) walkForImport(ctx context.Context, p projects.Project, folder string) (*importInv, int, string) {
	var fsys discover.FS
	var h hosts.Host
	remote := p.HostID != 0
	var mem *discover.MemFS
	if remote {
		var err error
		if s.HostDB == nil || s.Remote == nil {
			return nil, 503, "remote hosts are not configured"
		}
		if h, err = s.HostDB.Get(p.HostID); err != nil {
			return nil, 409, "the host of this project was removed"
		}
		mem = discover.NewMemFS(s.remoteReader(ctx, h, p.Path))
		fsys = mem
	} else {
		fsys = discover.LocalFS{Root: p.Path}
	}
	if folder != "" && !remote { // a folder on a host is checked once its listing is in memory, below
		if st, err := fsys.Stat(folder); err != nil || !st.Dir || st.Link {
			return nil, 422, "that is not a folder inside the project (links are not followed)"
		}
	}
	pf, _, _, _ := readProjectFile(fsys)
	answers, _ := s.Projects.Answers(p.ID)
	opt := discover.Options{Layout: pf.ScanLayout(), ManualRuns: append(pf.ManualRuns(), manualFromAnswers(answers)...), Inventory: true, MaxDepth: importDepth, Start: folder, Catalogs: !remote}
	var walkTruncated bool
	var walkWhy string
	if remote {
		var err error
		walkTruncated, walkWhy, err = s.walkRemote(ctx, h, p.Path, pf.ScanLayout().Skip, mem, discover.DefaultMaxEntries, discover.DefaultMaxDuration, nil)
		if err != nil {
			code, msg := hostErrStatus(err)
			return nil, code, msg
		}
		if st, err := mem.Stat(folder); folder != "" && (err != nil || !st.Dir || st.Link) {
			return nil, 422, "that is not a folder inside the project on the host (links are not followed)"
		}
	}
	res := discover.Scan(ctx, fsys, opt)
	if ctx.Err() != nil {
		return nil, 499, "canceled"
	}
	if walkTruncated {
		res.Truncated, res.TruncatedWhy = true, walkWhy
	}
	inv := &importInv{folder: folder, at: time.Now().UTC(), items: discover.Items(res), entries: res.Entries, truncated: res.Truncated, why: res.TruncatedWhy,
		othersTruncated: res.OthersTruncated, notes: res.Notes, skipped: res.Skipped, links: len(res.Links)}
	return inv, 0, ""
}

// inventory returns the cached walk for the project and folder, or walks (fresh forces it).
func (s *Server) inventory(ctx context.Context, p projects.Project, folder string, fresh bool) (*importInv, int, string) {
	// one walk per project at a time: a preview asked for while another walks (the page's own debounce, a second tab) waits for it
	// and, when it was for the same folder, uses its answer instead of walking again
	mu, _ := s.importWalks.LoadOrStore(p.ID, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	s.importMu.Lock()
	cur := s.importInvs[p.ID]
	s.importMu.Unlock()
	if cur != nil && cur.folder == folder && time.Since(cur.at) < importCacheTTL && (!fresh || time.Since(cur.at) < time.Second) {
		return cur, 0, ""
	}
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	inv, code, msg := s.walkForImport(ctx, p, folder)
	if code != 0 {
		return nil, code, msg
	}
	s.importMu.Lock()
	if s.importInvs == nil {
		s.importInvs = map[int64]*importInv{}
	}
	s.importInvs[p.ID] = inv
	s.importMu.Unlock()
	return inv, 0, ""
}

func registerableRole(role string) bool {
	return role == discover.RoleDataset || role == discover.RoleOrientation || role == discover.RoleCatalog || role == discover.RoleMask
}

// finalOutcomes are the application records that settle a file at the size and time they were made for; "failed" and "pending"
// do not: a failure is tried again and a pending file is still waiting.
var finalOutcomes = map[string]bool{"registered": true, "declared": true, "duplicate": true, "skipped": true}

func settled(a projects.Applied, ok bool, it discover.Item) bool {
	return ok && finalOutcomes[a.Outcome] && a.Size == it.Size && a.MtimeNS == it.MtimeNS
}

// ---- rules ----

// checkRules is ValidateRules plus what only the catalog knows: the unit text.
func checkRules(rs []discover.Rule) []string {
	bad := discover.ValidateRules(rs)
	for i, r := range rs {
		if r.Unit != "" && !catalog.ValidUnit(r.Unit) {
			bad = append(bad, fmt.Sprintf("rule %d (%s): %s", i+1, r.Name, catalog.ErrInvalidUnit.Error()))
		}
	}
	return bad
}

type rulesFile struct {
	// State is none (project.toml holds no rules), same (it holds the saved rules), differs (it holds others) or foreign
	// (it holds [[import.rules]] Vesqira did not write, which it never rewrites).
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

type importSummary struct {
	Rules int `json:"rules"`
	// Matched is the files a rule selects (a role that registers: dataset, orientation or catalog) in the last scan; Pending is
	// those not yet applied at their current size and time; New is the pending ones the previous scan did not know.
	Matched int `json:"matched"`
	Pending int `json:"pending"`
	New     int `json:"new"`
}

func fileRulesState(pf discover.ProjectFile, fileText string, exists bool, saved []discover.Rule) rulesFile {
	switch {
	case !exists || len(pf.Import.Rules) == 0:
		return rulesFile{State: "none"}
	case !discover.HasRulesSection(fileText):
		return rulesFile{State: "foreign"}
	case discover.RulesEqual(pf.Import.Rules, saved):
		return rulesFile{State: "same"}
	}
	return rulesFile{State: "differs"}
}

// projectRulesFile reads the project file's rules state, on this computer or through the host.
func (s *Server) projectRulesFile(ctx context.Context, p projects.Project, saved []discover.Rule) rulesFile {
	var fsys discover.FS
	if p.HostID != 0 {
		if s.HostDB == nil || s.Remote == nil {
			return rulesFile{State: "none"}
		}
		h, err := s.HostDB.Get(p.HostID)
		if err != nil {
			return rulesFile{State: "none"}
		}
		raw, exists, err := s.hostProjectFile(ctx, h, p)
		if err != nil || !exists {
			return rulesFile{State: "none"}
		}
		pf, perr := discover.ParseProjectFile(raw)
		if perr != nil {
			return rulesFile{State: "none", Error: perr.Error()}
		}
		return fileRulesState(pf, string(raw), true, saved)
	}
	fsys = discover.LocalFS{Root: p.Path}
	pf, exists, text, err := readProjectFile(fsys)
	if err != nil {
		return rulesFile{State: "none", Error: err.Error()}
	}
	return fileRulesState(pf, text, exists, saved)
}

func (s *Server) importRulesGet(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectFrom(w, r)
	if !ok {
		return
	}
	rs, err := s.Projects.Rules(p.ID)
	if err != nil {
		log.Printf("import: %v", err)
		fail(w, 500, "internal error")
		return
	}
	writeJSON(w, 200, map[string]any{"rules": rs, "file": s.projectRulesFile(r.Context(), p, rs), "summary": s.storedSummaryOf(p).ImportRules})
}

// importRulesPut replaces the project's rule list (the order is the priority). Saving stores them in the catalog only;
// project.toml changes only through importRulesWrite, after its own confirmation.
func (s *Server) importRulesPut(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectFrom(w, r)
	if !ok {
		return
	}
	var in struct {
		Rules []discover.Rule `json:"rules"`
	}
	if !body(w, r, 256<<10, &in, true) {
		return
	}
	if in.Rules == nil {
		in.Rules = []discover.Rule{}
	}
	if bad := checkRules(in.Rules); len(bad) > 0 {
		writeJSON(w, 422, map[string]any{"error": "the rules are not valid", "reasons": bad})
		return
	}
	if err := s.Projects.SetRules(p.ID, in.Rules); err != nil {
		log.Printf("import: %v", err)
		fail(w, 500, "internal error")
		return
	}
	s.Audit.RecordCtx(r.Context(), "import rules saved", "project", p.ID, fmt.Sprintf("%d rules", len(in.Rules)))
	s.importMu.Lock()
	cur := s.importInvs[p.ID]
	s.importMu.Unlock()
	if cur != nil && cur.folder == "" { // the banner counts what the new rules select in the last whole-project walk
		s.refreshImportSummary(wsOf(r), p, cur, in.Rules)
	}
	writeJSON(w, 200, map[string]any{"rules": in.Rules, "file": s.projectRulesFile(r.Context(), p, in.Rules), "summary": s.storedSummaryOf(p).ImportRules})
}

// importRulesWrite writes the saved rules into .vesqira/project.toml as a section Vesqira marks as its own, only when the
// request says write: true. Everything else in the file stays byte for byte; a file that has [[import.rules]] outside the
// section is the researcher's and is refused. On a host the write follows ADR 0039 (a file Vesqira did not write is not
// replaced).
func (s *Server) importRulesWrite(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectFrom(w, r)
	if !ok {
		return
	}
	var in struct {
		Write bool `json:"write"`
	}
	if !body(w, r, 1<<10, &in, true) {
		return
	}
	if !in.Write {
		fail(w, 400, "the rules are written to project.toml only when you confirm: send write: true")
		return
	}
	rs, err := s.Projects.Rules(p.ID)
	if err != nil {
		log.Printf("import: %v", err)
		fail(w, 500, "internal error")
		return
	}
	proposed := s.storedSummaryOf(p).Proposed
	if p.HostID != 0 {
		s.importRulesWriteOnHost(w, r, p, rs, proposed)
		return
	}
	_, file := projectFilePaths(p.Path)
	base := ""
	if data, rerr := os.ReadFile(file); rerr == nil {
		base = string(data)
	} else if !errors.Is(rerr, os.ErrNotExist) {
		fail(w, 409, "project.toml cannot be read")
		return
	} else {
		base = proposed
	}
	out, err := discover.ReplaceRules([]byte(base), rs)
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	if err := writeProjectFile(p.Path, out); err != nil {
		if errors.Is(err, errProjectFile) {
			fail(w, 409, err.Error())
			return
		}
		log.Printf("import: %v", err)
		fail(w, 500, "internal error")
		return
	}
	s.Audit.RecordCtx(r.Context(), "import rules written to project file", "project", p.ID, fmt.Sprintf("%d rules", len(rs)))
	s.startScan(r.Context(), p)
	writeJSON(w, 200, map[string]any{"written": true, "path": discover.ProjectFilePath, "rules": len(rs)})
}

func (s *Server) importRulesWriteOnHost(w http.ResponseWriter, r *http.Request, p projects.Project, rs []discover.Rule, proposed string) {
	if s.HostDB == nil || s.Remote == nil {
		fail(w, 501, "remote hosts are not configured")
		return
	}
	h, err := s.HostDB.Get(p.HostID)
	if err != nil {
		fail(w, 409, "the host of this project was removed")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	cur, exists, rerr := s.hostProjectFile(ctx, h, p)
	if rerr != nil {
		fail(w, 502, "the host could not be reached ("+rerr.Error()+"); nothing was written")
		return
	}
	if exists && !hasProjectMarker(cur) {
		fail(w, 409, "project.toml on the host was not written by Vesqira, so it is not replaced; the rules stay in the catalog. Add them there by hand, or delete the file to let Vesqira write one")
		return
	}
	base := string(cur)
	if !exists {
		base = withProjectMarker(proposed)
	}
	out, err := discover.ReplaceRules([]byte(base), rs)
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	if werr := s.writeHostProjectFile(ctx, h, p, string(out), expectOf(cur, exists)); werr != nil {
		fail(w, 409, werr.Error())
		return
	}
	s.startScan(r.Context(), p)
	writeJSON(w, 200, map[string]any{"written": true, "path": discover.ProjectFilePath, "rules": len(rs)})
}

// ---- preview ----

type importPreview struct {
	Folder          string                `json:"folder"`
	ScannedAt       time.Time             `json:"scannedAt"`
	Entries         int                   `json:"entries"`
	Truncated       bool                  `json:"truncated"`
	TruncatedWhy    string                `json:"truncatedWhy"`
	OthersTruncated bool                  `json:"othersTruncated"`
	Notes           []string              `json:"notes"`
	Files           int                   `json:"files"`
	Groups          []discover.Group      `json:"groups"`
	RulesFrom       string                `json:"rulesFrom"` // draft | saved
	Rules           []discover.RuleReport `json:"rules"`
	Conflicts       []discover.Conflict   `json:"conflicts"`
	ConflictCount   int                   `json:"conflictCount"`
	Selected        importSelected        `json:"selected"`
	Skipped         importSkipped         `json:"skipped"`
}

type importSelected struct {
	Files   int            `json:"files"`   // files a rule selects for registration
	ByRole  map[string]int `json:"byRole"`  // by the rule's role
	Pending int            `json:"pending"` // of them, not applied yet at their current size and time
	Applied int            `json:"applied"`
}

type importSkipped struct {
	Dirs         []string `json:"dirs"`
	DirCount     int      `json:"dirCount"`
	NeverRead    []string `json:"neverRead"`
	ArchiveCount int      `json:"archiveCount"`
	ArchiveBytes int64    `json:"archiveBytes"`
	Links        int      `json:"links"`
}

// importPreviewRoute answers what a folder holds under the built-in rules and a list of rules (the draft in the request, else
// the saved ones). It is read-only: it registers nothing, writes nothing and changes no catalog row.
func (s *Server) importPreviewRoute(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectFrom(w, r)
	if !ok {
		return
	}
	var in struct {
		Folder string           `json:"folder"`
		Rescan bool             `json:"rescan"`
		Rules  *[]discover.Rule `json:"rules"`
		Expand string           `json:"expand"`
	}
	if !body(w, r, 256<<10, &in, true) {
		return
	}
	folder, ok := cleanFolder(in.Folder)
	if !ok {
		fail(w, 422, "give a folder inside the project, relative to it")
		return
	}
	rules, from := []discover.Rule{}, "saved"
	if in.Rules != nil {
		if len(*in.Rules) > discover.MaxRules {
			fail(w, 422, fmt.Sprintf("at most %d rules", discover.MaxRules))
			return
		}
		rules, from = *in.Rules, "draft"
	} else {
		var err error
		if rules, err = s.Projects.Rules(p.ID); err != nil {
			log.Printf("import: %v", err)
			fail(w, 500, "internal error")
			return
		}
	}
	inv, code, msg := s.inventory(r.Context(), p, folder, in.Rescan)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	applied, _ := s.Projects.AppliedFiles(p.ID)
	cl := discover.Classify(inv.items, rules)
	out := importPreview{Folder: inv.folder, ScannedAt: inv.at, Entries: inv.entries, Truncated: inv.truncated, TruncatedWhy: inv.why, OthersTruncated: inv.othersTruncated,
		Notes: append([]string{}, inv.notes...), Files: len(cl.Proposals), Groups: discover.GroupProposals(cl.Proposals, importSamples, in.Expand, importExpandMax),
		RulesFrom: from, Rules: cl.Rules, Conflicts: cl.Conflicts, ConflictCount: cl.ConflictCount}
	out.Selected = importSelected{ByRole: map[string]int{}}
	for _, pr := range cl.Proposals {
		if pr.Rule == "" || !registerableRole(pr.ProposedRole) {
			continue
		}
		out.Selected.Files++
		out.Selected.ByRole[pr.ProposedRole]++
		if a, ok := applied[pr.Path]; settled(a, ok, pr.Item) {
			out.Selected.Applied++
		} else {
			out.Selected.Pending++
		}
	}
	sk := inv.skipped
	out.Skipped = importSkipped{Dirs: capStrings(sk.Dirs, 20), DirCount: len(sk.Dirs), NeverRead: capStrings(sk.NeverRead, 50), ArchiveCount: sk.ArchiveCount, ArchiveBytes: sk.ArchiveBytes, Links: inv.links}
	writeJSON(w, 200, out)
}

func capStrings(in []string, n int) []string {
	if len(in) > n {
		in = in[:n]
	}
	return append([]string{}, in...)
}

// ---- apply ----

type applyResult struct {
	Path      string          `json:"path"`
	Rule      string          `json:"rule"`
	Role      string          `json:"role"`
	Outcome   string          `json:"outcome"` // registered | declared | duplicate | skipped | failed
	Detail    string          `json:"detail"`
	DatasetID *int64          `json:"datasetId"`
	Declared  []string        `json:"declared"`
	Matches   []catalog.Match `json:"matches,omitempty"`
	retry     bool            // the declaration could not be made yet (a mask's dataset is not registered): reported as failed so a later apply tries again
}

type applySummary struct {
	Registered int `json:"registered"`
	Declared   int `json:"declared"` // already registered; the rule's declarations applied
	Duplicates int `json:"duplicates"`
	Skipped    int `json:"skipped"`
	Failed     int `json:"failed"`
	Remaining  int `json:"remaining"`
}

// importApplyRoute registers the files the saved rules select, through the registration paths the rest of the program uses
// (a FITS file, a point catalog), and applies each rule's declarations to them, labelled as declared by that rule. A file whose
// content a dataset already holds is offered, not registered (ADR 0053), unless its path is in asNew. A declaration never
// replaces one the researcher made themselves, and a file's own BUNIT always wins. The summary names every file that was not
// registered or declared and why.
func (s *Server) importApplyRoute(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectFrom(w, r)
	if !ok {
		return
	}
	var in struct {
		Confirm bool     `json:"confirm"`
		Folder  string   `json:"folder"`
		Paths   []string `json:"paths"`
		AsNew   []string `json:"asNew"`
	}
	if !body(w, r, 512<<10, &in, true) {
		return
	}
	if !in.Confirm {
		fail(w, 400, "files are registered only when you confirm: send confirm: true")
		return
	}
	if s.Store == nil || s.Worker == nil {
		fail(w, 503, "registering datasets is not configured")
		return
	}
	folder, ok := cleanFolder(in.Folder)
	if !ok {
		fail(w, 422, "give a folder inside the project, relative to it")
		return
	}
	if _, busy := s.importBusy.LoadOrStore(p.ID, true); busy {
		fail(w, 409, "an import is already running for this project")
		return
	}
	defer s.importBusy.Delete(p.ID)
	rules, err := s.Projects.Rules(p.ID)
	if err != nil {
		log.Printf("import: %v", err)
		fail(w, 500, "internal error")
		return
	}
	if len(rules) == 0 {
		fail(w, 409, "this project has no saved rules")
		return
	}
	inv, code, msg := s.inventory(r.Context(), p, folder, false)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	byName := map[string]discover.Rule{}
	for _, ru := range rules {
		byName[ru.Name] = ru
	}
	want := map[string]bool{}
	for _, x := range in.Paths {
		want[x] = true
	}
	asNew := map[string]bool{}
	for _, x := range in.AsNew {
		asNew[x] = true
	}
	applied, _ := s.Projects.AppliedFiles(p.ID)
	var todo []discover.Proposal
	cl := discover.Classify(inv.items, rules)
	selected := map[string]bool{}
	for _, pr := range cl.Proposals {
		if pr.Rule == "" || !registerableRole(pr.ProposedRole) {
			continue
		}
		selected[pr.Path] = true
		if len(want) > 0 && !want[pr.Path] {
			continue
		}
		if len(want) == 0 {
			if a, ok := applied[pr.Path]; settled(a, ok, pr.Item) && !asNew[pr.Path] {
				continue // done at this size and time already
			}
		}
		todo = append(todo, pr)
	}
	// a mask is made for a dataset that must be registered by then: files of the mask role go last
	sort.SliceStable(todo, func(i, j int) bool {
		return todo[i].ProposedRole != discover.RoleMask && todo[j].ProposedRole == discover.RoleMask
	})
	for x := range want {
		if !selected[x] {
			fail(w, 422, "no saved rule selects "+x+"; save the rule and preview again")
			return
		}
	}
	for x := range asNew {
		if !selected[x] {
			fail(w, 422, "no saved rule selects "+x)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), importApplyDeadline)
	defer cancel()
	var h hosts.Host
	if p.HostID != 0 {
		if s.HostDB == nil || s.Remote == nil {
			fail(w, 503, "remote hosts are not configured")
			return
		}
		if h, err = s.HostDB.Get(p.HostID); err != nil {
			fail(w, 409, "the host of this project was removed")
			return
		}
	}
	facts, _ := s.Store.FactsByPath(wsOf(r), p.HostID)
	results := []applyResult{}
	var records []projects.Applied
	var sum applySummary
	for i, pr := range todo {
		if i >= importApplyMax || ctx.Err() != nil {
			sum.Remaining = len(todo) - i
			break
		}
		res := s.applyOne(ctx, p, h, pr, byName[pr.Rule], facts, asNew[pr.Path])
		results = append(results, res)
		switch res.Outcome {
		case "registered":
			sum.Registered++
		case "declared":
			sum.Declared++
		case "duplicate":
			sum.Duplicates++
		case "skipped":
			sum.Skipped++
		default:
			sum.Failed++
		}
		records = append(records, projects.Applied{Path: pr.Path, Rule: pr.Rule, Size: pr.Size, MtimeNS: pr.MtimeNS, Outcome: res.Outcome, Detail: res.Detail})
	}
	if err := s.Projects.SaveApplied(p.ID, records); err != nil {
		log.Printf("import: %v", err)
	}
	s.Audit.RecordCtx(r.Context(), "import rules applied", "project", p.ID, fmt.Sprintf("%d registered, %d declared, %d offered as duplicates, %d skipped, %d failed", sum.Registered, sum.Declared, sum.Duplicates, sum.Skipped, sum.Failed))
	if inv.folder == "" { // the summary counts the whole project; a walk of a subfolder would shrink it
		s.refreshImportSummary(wsOf(r), p, inv, rules)
	}
	writeJSON(w, 200, map[string]any{"results": results, "summary": sum, "importSummary": s.storedSummaryOf(mustProject(wsOf(r), s, p)).ImportRules})
}

func mustProject(ws workspace.ID, s *Server, p projects.Project) projects.Project {
	if cur, err := s.Projects.Get(ws, p.ID); err == nil {
		return cur
	}
	return p
}

// applyOne registers one file (when it is not registered) and applies the rule's declarations to its dataset.
func (s *Server) applyOne(ctx context.Context, p projects.Project, h hosts.Host, pr discover.Proposal, rule discover.Rule, facts map[string]catalog.PathFacts, asNew bool) applyResult {
	res := applyResult{Path: pr.Path, Rule: rule.Name, Role: rule.Role, Declared: []string{}}
	skip := func(why string) applyResult { res.Outcome, res.Detail = "skipped", why; return res }
	fail := func(why string) applyResult { res.Outcome, res.Detail = "failed", why; return res }
	remote := p.HostID != 0
	abs := absFor(p, pr.Path)
	if rule.Role == discover.RoleCatalog && remote {
		return skip("point catalogs are read from this computer only (ADR 0044), so a catalog on a host is not registered")
	}
	var d catalog.Dataset
	registered := false
	if fa, ok := facts[abs]; ok && fa.Size == pr.Size && fa.MtimeNS == pr.MtimeNS {
		var err error
		if d, err = s.Store.Get(wsCtx(ctx), fa.ID); err != nil {
			return fail("the registered dataset could not be read")
		}
	} else {
		if pr.RunPath != "" {
			return skip("inside the run " + pr.RunPath + ": it is registered by that run's import, so import the run first; the rule's declarations then apply")
		}
		if !remote {
			real, err := s.roots(wsCtx(ctx)).Resolve(abs)
			if err != nil {
				return fail("not available: outside the source roots")
			}
			if info, err := statFile(real); err != nil || info.Size() != pr.Size || info.ModTime().UnixNano() != pr.MtimeNS {
				return fail("the file changed since the preview; preview again")
			}
			if !asNew && !s.knownFile(wsCtx(ctx), real) {
				if m := s.sameContent(ctx, real); len(m) > 0 {
					res.Outcome, res.Matches = "duplicate", m
					res.Detail = fmt.Sprintf("A dataset already holds this content (dataset %d, %s). Not registered: add the file as another location of it, or register it separately.", m[0].DatasetID, m[0].Relation)
					return res
				}
			}
			var code int
			var msg string
			func() {
				select {
				case s.lane() <- struct{}{}:
					defer func() { <-s.lane() }()
					// Not waiting(ctx): an apply can register hundreds of files, and the interactive class is the viewer's (ADR 0056).
					tctx, cancel := context.WithTimeout(ctx, 60*time.Second)
					defer cancel()
					if rule.Role == discover.RoleCatalog { // the researcher says it is a table, whatever its name
						d, code, msg = s.registerCatalogPath(tctx, real)
					} else {
						d, code, msg = s.registerPath(tctx, real)
					}
				case <-ctx.Done():
					code, msg = 499, "canceled"
				}
			}()
			if code != 0 {
				return fail(msg)
			}
		} else {
			var code int
			var msg string
			func() {
				select {
				case s.lane() <- struct{}{}:
					defer func() { <-s.lane() }()
					tctx, cancel := context.WithTimeout(ctx, 60*time.Second)
					defer cancel()
					d, code, msg = s.registerRemoteFile(tctx, h, abs)
				case <-ctx.Done():
					code, msg = 499, "canceled"
				}
			}()
			if code != 0 {
				return fail(msg)
			}
		}
		registered = true
	}
	id := d.ID
	res.DatasetID = &id
	notes := s.declareByRule(ctx, p, &res, d, rule)
	if res.retry {
		res.Outcome, res.Detail = "failed", strings.Join(notes, "; ")
		return res
	}
	switch {
	case registered:
		res.Outcome = "registered"
	default:
		res.Outcome = "declared"
	}
	if len(res.Declared) == 0 && !registered {
		res.Outcome = "skipped" // already registered and nothing declared: say why in the detail
	}
	res.Detail = strings.Join(notes, "; ")
	if res.Detail == "" {
		res.Detail = map[bool]string{true: "registered", false: "already registered; this rule declares nothing for it"}[registered]
	}
	return res
}

// declareByRule applies the rule's declarations to d through the existing setters. A declaration made by the researcher is
// never replaced; one made by an adapter or an earlier run of a rule may be. Each refusal is a note, never an error.
func (s *Server) declareByRule(ctx context.Context, p projects.Project, res *applyResult, d catalog.Dataset, rule discover.Rule) (notes []string) {
	by := rule.DeclaredBy()
	label := " (declared by your rule " + rule.Name + ")"
	switch rule.Role {
	case discover.RoleDataset:
		if rule.Unit == "" {
			return nil
		}
		switch {
		case d.Kind == catalog.KindCatalog:
			notes = append(notes, "a point catalog has no brightness unit: its columns carry their own")
		case d.UnitDeclaration != nil && d.UnitDeclaration.DeclaredBy == "user":
			notes = append(notes, "kept your own declared unit "+d.UnitDeclaration.Unit)
		default:
			switch err := s.Store.SetUnitDeclaration(d, rule.Unit, by); {
			case errors.Is(err, catalog.ErrUnitInFile):
				notes = append(notes, "the file states its own BUNIT, which always wins: the unit was not declared")
			case errors.Is(err, catalog.ErrNotBrightness):
				notes = append(notes, "this is an orientation or reliability map, which has no brightness unit")
			case err != nil:
				notes = append(notes, "the unit could not be declared ("+err.Error()+")")
			default:
				res.Declared = append(res.Declared, "unit "+rule.Unit+label)
			}
		}
	case discover.RoleOrientation:
		switch {
		case d.Kind == catalog.KindCatalog:
			notes = append(notes, "a point catalog has no pixel grid: declare its angle column with a catalog rule")
		case nonDegenerateAxes(d.Header) != 2:
			notes = append(notes, "not a single plane (two axes longer than 1): not declared as an orientation map")
		case d.Interpretation != nil && d.Interpretation.DeclaredBy == "user":
			notes = append(notes, "kept your own orientation declaration")
		default:
			in := catalog.Interpretation{Kind: "axial_orientation", Unit: rule.AngleUnit, DeclaredBy: by}
			if rule.AngleFrame != "" {
				f := rule.AngleFrame
				in.Frame = &f
			}
			if rule.Quantity != "" {
				q := rule.Quantity
				in.Quantity = &q
			}
			if d.Interpretation != nil { // a reliability companion an adapter paired stays; a rule never pairs one
				in.ReliabilityID = d.Interpretation.ReliabilityID
			}
			if err := s.Store.SetInterpretation(d, in); err != nil {
				notes = append(notes, "the orientation could not be declared ("+err.Error()+")")
			} else {
				res.Declared = append(res.Declared, "axial orientation in "+rule.AngleUnit+label)
				if in.Frame != nil {
					res.Declared = append(res.Declared, "angle frame "+*in.Frame+label)
				}
				if in.Quantity != nil {
					res.Declared = append(res.Declared, "quantity "+*in.Quantity+label)
				}
			}
		}
	case discover.RoleMask:
		notes = s.maskByRule(ctx, p, res, d, rule)
	case discover.RoleCatalog:
		if d.Kind != catalog.KindCatalog {
			notes = append(notes, "the worker did not read this file as a table, so its columns were not declared")
			break
		}
		if d.CatalogDeclaration != nil && d.CatalogDeclaration.DeclaredBy == "user" {
			notes = append(notes, "kept your own catalog declaration")
			break
		}
		decl := catalog.CatalogDeclaration{Lon: rule.Lon, Lat: rule.Lat, Format: rule.CoordFormat, Frame: rule.CoordFrame, Values: append([]string{}, rule.Values...)}
		if rule.Equinox != "" {
			e := rule.Equinox
			decl.Equinox = &e
		}
		if rule.AngleColumn != "" {
			a := &catalog.CatalogAngle{Column: rule.AngleColumn, Unit: rule.AngleUnit, Frame: "sky"}
			if rule.Quantity != "" {
				q := rule.Quantity
				a.Quantity = &q
			}
			if rule.SigmaColumn != "" {
				sg := rule.SigmaColumn
				a.SigmaColumn = &sg
			}
			decl.Angle = a
		}
		if bad, err := catalog.ValidateCatalogDeclaration(d, decl); err != nil {
			notes = append(notes, "the declaration could not be checked ("+err.Error()+")")
		} else if len(bad) > 0 {
			notes = append(notes, "not declared, because the file's columns do not fit the rule: "+strings.Join(bad, "; "))
		} else if err := s.Store.SetCatalogDeclaration(d, decl, by); err != nil {
			notes = append(notes, "the declaration could not be stored ("+err.Error()+")")
		} else {
			res.Declared = append(res.Declared, "position columns "+rule.Lon+", "+rule.Lat+" in "+rule.CoordFrame+label)
		}
	}
	return notes
}

// ---- what a scan counts ----

// refreshImportSummary counts what the saved rules select in a walk, and which of those are not applied yet, and stores the
// count in the project summary. It reads and writes nothing else: a scan never registers a file for a rule or applies a
// declaration. "pending" rows remember the files the last scan already counted, so New is only what appeared since.
func (s *Server) refreshImportSummary(ws workspace.ID, p projects.Project, inv *importInv, rules []discover.Rule) {
	applied, _ := s.Projects.AppliedFiles(p.ID)
	sum := importSummary{Rules: len(rules)}
	var seen []projects.Applied
	if len(rules) > 0 {
		cl := discover.Classify(inv.items, rules)
		for _, pr := range cl.Proposals {
			if pr.Rule == "" || !registerableRole(pr.ProposedRole) {
				continue
			}
			sum.Matched++
			a, ok := applied[pr.Path]
			if settled(a, ok, pr.Item) {
				continue
			}
			sum.Pending++
			if ok && a.Size == pr.Size && a.MtimeNS == pr.MtimeNS {
				continue // an earlier scan counted it (or a failed application is waiting to be tried again)
			}
			sum.New++
			seen = append(seen, projects.Applied{Path: pr.Path, Rule: pr.Rule, Size: pr.Size, MtimeNS: pr.MtimeNS, Outcome: "pending"})
		}
	}
	if len(seen) > 0 {
		if err := s.Projects.SaveApplied(p.ID, seen); err != nil {
			log.Printf("import: %v", err)
		}
	}
	s.updateSummary(ws, p.ID, func(st *storedSummary) { st.ImportRules = &sum })
}

// importAfterScan is the scan's step for rules: when the project file holds rules and the catalog has none, they are loaded (a
// project opened on another computer); then the saved rules are counted against what the scan listed.
func (s *Server) importAfterScan(ws workspace.ID, p projects.Project, pf discover.ProjectFile, res *discover.Result) (ignored map[string]bool) {
	saved, err := s.Projects.Rules(p.ID)
	if err != nil {
		return nil
	}
	// a project opened on another computer takes the rules from its project.toml; once the researcher has saved a list here
	// (even an empty one) the file is never read back over it
	if listed, _ := s.Projects.RulesSaved(p.ID); !listed && len(saved) == 0 && len(pf.Import.Rules) > 0 && len(checkRules(pf.Import.Rules)) == 0 {
		if err := s.Projects.SetRules(p.ID, pf.Import.Rules); err == nil {
			saved = pf.Import.Rules
		}
	}
	if len(saved) == 0 {
		s.updateSummary(ws, p.ID, func(st *storedSummary) { st.ImportRules = nil })
		return nil
	}
	inv := &importInv{items: discover.Items(res)}
	s.refreshImportSummary(ws, p, inv, saved)
	// a file an "ignore" rule claims is not registered by the scan either (the researcher said they do not want it); it stays
	// listed. The outputs of a run belong to the run import, which a rule does not stop.
	ignored = map[string]bool{}
	for _, pr := range discover.Classify(inv.items, saved).Proposals {
		if pr.Rule != "" && pr.ProposedRole == discover.RoleIgnore && pr.RunPath == "" {
			ignored[pr.Path] = true
		}
	}
	return ignored
}
