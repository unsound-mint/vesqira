package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/discover"
	"github.com/unsound-mint/vesqira/services/api/internal/projects"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

const maxNeedPaths = 20

type needOption struct {
	ID      string   `json:"id"`
	Label   string   `json:"label"`
	Input   string   `json:"input"` // "" (none), "text" or "choice"
	Choices []string `json:"choices,omitempty"`
	Hint    string   `json:"hint,omitempty"`
}

type needScope struct {
	Paths  []string `json:"paths"` // at most maxNeedPaths of them
	Count  int      `json:"count"`
	Folder string   `json:"folder,omitempty"`
}

// need is one thing the project cannot know and the researcher can say. Nothing here is guessed: the message says what
// is missing, the source says which file said so, and the answers are the only ways to settle it. "unknown" is one.
type need struct {
	ID         string           `json:"id"`
	Kind       string           `json:"kind"` // missing_bunit | axial_unconfirmed | result_folder | no_version_control | host_repo
	Scope      needScope        `json:"scope"`
	Message    string           `json:"message"`
	Source     string           `json:"source"`
	Answers    []needOption     `json:"answers"`
	State      string           `json:"state"` // open | answered
	Answer     *projects.Answer `json:"answer"`
	Suggestion string           `json:"suggestion,omitempty"` // what an earlier fact suggests; never applied unless chosen
	DatasetID  *int64           `json:"datasetId,omitempty"`
	datasetIDs []int64          // missing_bunit: the files the question lists as open, for applying an answer
	// missing_bunit, after an answer "declare previousUnit": the files that still have that unit. Answering again with another
	// unit moves them too, so a change of the folder's unit is one step and the answer stays true of its files.
	governedIDs  []int64
	previousUnit string
}

var unknownOpt = needOption{ID: "unknown", Label: "Leave unknown"}

func capPaths(ps []string) []string {
	if len(ps) > maxNeedPaths {
		ps = ps[:maxNeedPaths]
	}
	return append([]string{}, ps...)
}

func (s *Server) storedSummaryOf(p projects.Project) storedSummary {
	var sum storedSummary
	_ = json.Unmarshal(p.Summary, &sum)
	return sum
}

func absFor(p projects.Project, rel string) string {
	if p.HostID != 0 {
		return hostPath(p.Path, rel)
	}
	return filepath.Join(p.Path, filepath.FromSlash(rel))
}

// computeNeeds builds the list from the stored scan, the catalog and the answers. It is computed when read, so a new
// registration or an answer shows at once.
func (s *Server) computeNeeds(ws workspace.ID, p projects.Project) ([]need, error) {
	if p.LastScanAt == nil {
		return []need{}, nil
	}
	sum := s.storedSummaryOf(p)
	answers, err := s.Projects.Answers(p.ID)
	if err != nil {
		return nil, err
	}
	var out []need

	fits, err := s.Projects.ByFormat(p.ID, "FITS")
	if err != nil {
		return nil, err
	}
	facts, err := s.Store.FactsByPath(ws, p.HostID)
	if err != nil {
		return nil, err
	}
	// FITS files whose header has no BUNIT, by folder. Only files that are registered can be judged, and an orientation map
	// (or the reliability map of one) is never asked for a brightness unit. A unit a person declared (here or on the file's
	// own page) settles a file for as long as it describes the file as registered; once the question has been answered
	// "declare V", it stays settled only for files that really have V, so the answer never claims more than the files show.
	units, err := s.Store.FreshUnits(ws)
	if err != nil {
		return nil, err
	}
	notBrightness, err := s.Store.NoBrightnessIDs(ws)
	if err != nil {
		return nil, err
	}
	type bunitFile struct {
		path string
		id   int64
	}
	type folderFiles struct{ all, open []bunitFile }
	byFolder := map[string]*folderFiles{}
	for _, r := range fits {
		fa, ok := facts[absFor(p, r.Path)]
		if !ok || fa.BunitStatus == "declared" || notBrightness[fa.ID] {
			continue
		}
		dir := path.Dir(r.Path)
		if byFolder[dir] == nil {
			byFolder[dir] = &folderFiles{}
		}
		ff := byFolder[dir]
		f := bunitFile{r.Path, fa.ID}
		ff.all = append(ff.all, f)
		ans, answered := answers["bunit:"+dir]
		holds := units[fa.ID] != "" && (!answered || ans.Answer != "declare" || units[fa.ID] == ans.Value)
		if !holds {
			ff.open = append(ff.open, f)
		}
	}
	folders := make([]string, 0, len(byFolder))
	for f := range byFolder {
		folders = append(folders, f)
	}
	sort.Strings(folders)
	for _, f := range folders {
		ff := byFolder[f]
		where := "the project folder"
		if f != "." {
			where = f
		}
		ans, answered := answers["bunit:"+f]
		show, msg := ff.open, fmt.Sprintf("%d FITS file(s) in %s have no BUNIT, so their values have no declared unit.", len(ff.open), where)
		switch {
		case answered && ans.Answer == "declare" && len(ff.open) > 0:
			msg = fmt.Sprintf("%d FITS file(s) in %s have no BUNIT and not the unit you declared for this folder (%s).", len(ff.open), where, ans.Value)
		case answered && ans.Answer == "declare":
			show, msg = ff.all, fmt.Sprintf("%d FITS file(s) in %s have no BUNIT; you declared %s for them.", len(ff.all), where, ans.Value)
		case answered: // left unknown, or answered another way: it stays answered, the files untouched
			show, msg = ff.all, fmt.Sprintf("%d FITS file(s) in %s have no BUNIT, so their values have no declared unit.", len(ff.all), where)
		case len(ff.open) == 0: // every file got its unit some other way: nothing is left to ask
			continue
		}
		paths := make([]string, 0, len(show))
		for _, x := range show {
			paths = append(paths, x.path)
		}
		ids := make([]int64, 0, len(ff.open))
		for _, x := range ff.open {
			ids = append(ids, x.id)
		}
		var governed []int64
		previous := ""
		if answered && ans.Answer == "declare" {
			previous = ans.Value
			for _, x := range ff.all {
				if units[x.id] == ans.Value {
					governed = append(governed, x.id)
				}
			}
		}
		out = append(out, need{
			ID: "bunit:" + f, Kind: "missing_bunit", Scope: needScope{Paths: capPaths(paths), Count: len(show), Folder: f},
			Message:    msg,
			Source:     "the FITS headers: no BUNIT card",
			Answers:    []needOption{{ID: "declare", Label: "Declare a unit", Input: "text", Hint: "kept as declared by you and shown with the file's values as yours, never as the file's; the files are not changed"}, unknownOpt},
			datasetIDs: ids, governedIDs: governed, previousUnit: previous,
		})
	}

	// orientation maps a run import proposed and nobody confirmed
	if props, err := s.Store.ProposedInterpretations(ws); err == nil && len(props) > 0 {
		for _, r := range fits {
			fa, ok := facts[absFor(p, r.Path)]
			if !ok {
				continue
			}
			in, ok := props[fa.ID]
			if !ok || in.Kind != "axial_orientation" {
				continue
			}
			id := fa.ID
			out = append(out, need{
				ID: "axial:" + r.Path, Kind: "axial_unconfirmed", DatasetID: &id, Scope: needScope{Paths: []string{r.Path}, Count: 1},
				Message: fmt.Sprintf("%s is proposed as an axial orientation map in %s by %s. You have not confirmed it.", path.Base(r.Path), in.Unit, authorText(in.DeclaredBy)),
				Source:  proposalSource(in.DeclaredBy),
				Answers: []needOption{{ID: "confirm", Label: "Confirm: it is an axial orientation map", Input: "choice", Choices: []string{"deg", "rad"}, Hint: "leave the unit empty to keep the proposed one (" + in.Unit + ")"},
					{ID: "not_axial", Label: "It is not an orientation map"}, unknownOpt},
			})
		}
	}

	for _, d := range sum.ResultLike {
		out = append(out, need{
			ID: "results:" + d, Kind: "result_folder", Scope: needScope{Paths: []string{d}, Count: 1, Folder: d},
			Message: fmt.Sprintf("%s looks like results but has no run record: mark as a run?", d),
			Source:  "folder name; no run_metadata.toml, provenance.json, run.json, params and metrics, or meta.yaml inside",
			Answers: []needOption{{ID: "mark_run", Label: "Mark it as a run"}, {ID: "not_run", Label: "It is not a run"}, unknownOpt},
		})
	}

	if !sum.Git.Present {
		out = append(out, need{
			ID: "vcs", Kind: "no_version_control", Scope: needScope{Paths: []string{}, Count: 0},
			Message: "This project is not under version control, so a run cannot be tied to the code that made it.",
			Source:  "no .git in the project folder",
			Answers: []needOption{{ID: "acknowledge", Label: "It is versioned somewhere else"}, unknownOpt},
		})
	}

	out = append(out, s.hostRepoNeeds(p, sum)...)

	for i := range out {
		out[i].State = "open"
		if a, ok := answers[out[i].ID]; ok {
			a := a
			// a unit that was declared and later withdrawn for some files: the answer no longer holds for them
			if out[i].Kind == "missing_bunit" && a.Answer == "declare" && len(out[i].datasetIDs) > 0 {
				continue
			}
			out[i].State, out[i].Answer = "answered", &a
		}
	}
	if out == nil {
		out = []need{}
	}
	return out, nil
}

// hostRepoNeeds asks where the code is on each host the project's job templates or past jobs use. A host is used when
// a job ran on it, or (for a project that lives on a host) when the project has job templates; the project file or an
// answer settles it. A repo folder seen in an earlier job is offered as a suggestion, never applied.
func (s *Server) hostRepoNeeds(p projects.Project, sum storedSummary) []need {
	if s.HostDB == nil || s.localJobs() == nil {
		return nil
	}
	type use struct {
		repo string
		job  int64
	}
	used := map[int64]use{}
	if js, err := s.localJobs().List(); err == nil {
		for _, j := range js {
			u := used[j.HostID]
			if req, ok := j.VGTRequest(); ok && req.RepoDir != "" && j.ID >= u.job {
				u = use{req.RepoDir, j.ID}
			}
			used[j.HostID] = u
		}
	}
	if p.HostID != 0 && sum.Counts.Jobs > 0 {
		if _, ok := used[p.HostID]; !ok {
			used[p.HostID] = use{}
		}
	}
	var out []need
	ids := make([]int64, 0, len(used))
	for id := range used {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		h, err := s.HostDB.Get(id)
		if err != nil || sum.ProjectFile.HostRepos[h.Name] != "" {
			continue
		}
		n := need{
			ID: "host-repo:" + h.Name, Kind: "host_repo", Scope: needScope{Paths: []string{}, Count: 0},
			Message: fmt.Sprintf("Where is the code on %s? Vesqira cannot tell which checkout the runs there came from.", h.Name),
			Source:  "jobs on this host; no [hosts." + h.Name + "] repo in project.toml",
			Answers: []needOption{{ID: "declare", Label: "Set the code folder", Input: "text", Hint: "an absolute path on the host"}, unknownOpt},
		}
		if u := used[id]; u.repo != "" {
			n.Suggestion = fmt.Sprintf("job #%d on this host used %s", u.job, u.repo)
		}
		out = append(out, n)
	}
	return out
}

func openCount(ns []need) int {
	n := 0
	for _, x := range ns {
		if x.State == "open" {
			n++
		}
	}
	return n
}

type healthItem struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	State  string `json:"state"` // ok | warn | todo | na
	Detail string `json:"detail"`
	Why    string `json:"why"` // why it matters
}

// health is the short project-health checklist: each item says its state, the fact behind it and why it matters.
func health(p projects.Project, sum storedSummary, ns []need) []healthItem {
	if p.LastScanAt == nil {
		return []healthItem{}
	}
	var out []healthItem
	vc := healthItem{ID: "version_control", Label: "Code under version control", Why: "A run can only be tied to the code that made it when the code is versioned."}
	switch {
	case !sum.Git.Present:
		vc.State, vc.Detail = "todo", "No version control: there is no .git in the project folder."
	case sum.Git.Dirty != nil && *sum.Git.Dirty > 0:
		vc.State, vc.Detail = "warn", fmt.Sprintf("Commit %s with %d changed or untracked file(s); runs started now will not match a commit.", orUnknown(sum.Git.Commit), *sum.Git.Dirty)
	case sum.Git.Dirty == nil && sum.Git.Commit != "":
		vc.State, vc.Detail = "ok", fmt.Sprintf("Commit %s (whether the work tree is clean is unknown here).", sum.Git.Commit)
	default:
		vc.State, vc.Detail = "ok", fmt.Sprintf("Commit %s, work tree clean.", orUnknown(sum.Git.Commit))
	}
	out = append(out, vc)

	rr := healthItem{ID: "run_records", Label: "A run record per run", Why: "A record is what says how a run was made; without one its settings are only what you remember."}
	recorded := sum.Counts.Runs - sum.Counts.ManualRuns
	switch {
	case sum.Counts.Runs == 0 && len(sum.ResultLike) == 0:
		rr.State, rr.Detail = "na", "No runs found."
	case sum.Counts.Runs == 0:
		rr.State, rr.Detail = "todo", fmt.Sprintf("%d folder(s) look like results but have no run record.", len(sum.ResultLike))
	case len(sum.ResultLike) > 0 || recorded < sum.Counts.Runs:
		rr.State, rr.Detail = "warn", fmt.Sprintf("%d of %d run(s) have a run record; %d results-like folder(s) have none.", recorded, sum.Counts.Runs, len(sum.ResultLike))
	default:
		rr.State, rr.Detail = "ok", fmt.Sprintf("All %d run(s) have a run record.", sum.Counts.Runs)
	}
	out = append(out, rr)

	rd := healthItem{ID: "raw_apart", Label: "Raw data kept apart from results", Why: "Inputs that sit among outputs are easy to overwrite or to mistake for results."}
	switch {
	case sum.Counts.Data == 0:
		rd.State, rd.Detail = "na", "No data files found."
	case sum.Counts.RoleRaw > 0:
		rd.State, rd.Detail = "ok", fmt.Sprintf("%d file(s) are in a raw or external folder, treated as read-only inputs.", sum.Counts.RoleRaw)
	default:
		rd.State, rd.Detail = "todo", "No data/raw or data/external folder: nothing marks which files are original inputs."
	}
	out = append(out, rd)

	un := healthItem{ID: "units_declared", Label: "Units declared", Why: "Values without a declared unit cannot be compared honestly."}
	open, unknown := 0, 0
	for _, n := range ns {
		switch {
		case n.Kind != "missing_bunit":
		case n.State == "open":
			open += n.Scope.Count
		case n.Answer != nil && n.Answer.Answer == "unknown":
			unknown += n.Scope.Count
		}
	}
	fitsN := sum.Counts.ByFormat["FITS"]
	switch {
	case fitsN == 0:
		un.State, un.Detail = "na", "No FITS files."
	case open > 0:
		un.State, un.Detail = "warn", fmt.Sprintf("%d FITS file(s) have no BUNIT and no unit declared by you.", open)
	case unknown > 0:
		un.State, un.Detail = "ok", fmt.Sprintf("%d FITS file(s) have no unit; you chose to leave it unknown, which is a valid answer.", unknown)
	default:
		un.State, un.Detail = "ok", "Every registered FITS file declares a unit or one was given by you."
	}
	out = append(out, un)

	pf := healthItem{ID: "project_file", Label: "Project file", Why: "Your layout and answers live in .vesqira/project.toml. Keep it out of version control (add .vesqira/ to .gitignore)."}
	if sum.ProjectFile.Exists {
		pf.State, pf.Detail = "ok", "Present."
		if sum.ProjectFile.Error != "" {
			pf.State, pf.Detail = "warn", sum.ProjectFile.Error
		}
	} else {
		pf.State, pf.Detail = "todo", "Not written yet; a proposed file is ready to review."
	}
	out = append(out, pf)
	return out
}

func orUnknown(s string) string {
	if s == "" {
		return "unknown"
	}
	return s
}

// ---- answering ----

type answerIn struct {
	Need   string `json:"need"`
	Answer string `json:"answer"`
	Value  string `json:"value"`
	Write  bool   `json:"write"`
}

var errAnswer = errors.New("answer")

func validText(v string, max int) bool {
	if v == "" || len(v) > max {
		return false
	}
	for _, c := range v {
		if c < 0x20 || c == 0x7f {
			return false
		}
	}
	return true
}

func (n need) allows(a string) *needOption {
	for i := range n.Answers {
		if n.Answers[i].ID == a {
			return &n.Answers[i]
		}
	}
	return nil
}

// validate checks an answer against what the need allows; a declared value is never guessed or defaulted.
func (n need) validate(in answerIn) error {
	opt := n.allows(in.Answer)
	if opt == nil {
		return fmt.Errorf("%w: %q is not an answer to this question", errAnswer, in.Answer)
	}
	switch {
	case n.Kind == "missing_bunit" && in.Answer == "declare":
		if !catalog.ValidUnit(in.Value) {
			return fmt.Errorf("%w: %s", errAnswer, catalog.ErrInvalidUnit)
		}
	case n.Kind == "host_repo" && in.Answer == "declare":
		if !validText(in.Value, 500) || !strings.HasPrefix(in.Value, "/") || strings.Contains(in.Value, "..") {
			return fmt.Errorf("%w: give an absolute path on the host", errAnswer)
		}
	case n.Kind == "axial_unconfirmed" && in.Answer == "confirm":
		if in.Value != "" && in.Value != "deg" && in.Value != "rad" {
			return fmt.Errorf("%w: the unit must be deg or rad", errAnswer)
		}
	case in.Value != "":
		return fmt.Errorf("%w: this answer takes no value", errAnswer)
	}
	return nil
}

// applyAnswer carries out what an answer means beyond recording it. Confirming an orientation map goes through the same
// declaration the dataset page uses, as the user's (an adapter's proposal never becomes the user's by itself). A declared
// unit becomes the unit declaration of each registered file in the question that still has none; a file that has its own
// declaration keeps it, and a file that states its BUNIT is never changed.
func (s *Server) applyAnswer(ws workspace.ID, n need, in answerIn) error {
	if n.Kind == "missing_bunit" && in.Answer == "declare" {
		targets := append([]int64{}, n.datasetIDs...)
		if n.previousUnit != "" && n.previousUnit != in.Value {
			targets = append(targets, n.governedIDs...) // the files that followed the folder's old unit follow its new one
		}
		ds := make([]catalog.Dataset, 0, len(targets))
		for _, id := range targets {
			d, err := s.Store.Get(ws, id)
			if err != nil {
				return err
			}
			ds = append(ds, d)
		}
		applied, err := s.Store.SetUnitDeclarations(ds, in.Value, "user") // all or none; files that state a unit are skipped
		if err != nil {
			return err
		}
		for _, d := range applied {
			s.Audit.Record(ws, "unit declared", "dataset", d.ID, d.Name+": "+in.Value+" (from the project's needs list)")
		}
		return nil
	}
	if n.Kind != "axial_unconfirmed" || n.DatasetID == nil {
		return nil
	}
	d, err := s.Store.Get(ws, *n.DatasetID)
	if err != nil {
		return err
	}
	switch in.Answer {
	case "confirm":
		if d.Interpretation == nil {
			return fmt.Errorf("%w: the dataset no longer has a proposed interpretation", errAnswer)
		}
		unit := in.Value
		if unit == "" {
			unit = d.Interpretation.Unit
		}
		return s.Store.SetInterpretation(d, catalog.Interpretation{Kind: "axial_orientation", Unit: unit, ReliabilityID: d.Interpretation.ReliabilityID,
			Frame: d.Interpretation.Frame, Quantity: d.Interpretation.Quantity, DeclaredBy: "user"})
	case "not_axial":
		return s.Store.ClearInterpretation(d.ID)
	}
	return nil
}

// recordAnswer writes the answer to the project file when asked (or when the file exists), then to the catalog.
// It returns whether the answer reached the file and a note when it did not.
func (s *Server) recordAnswer(ctx context.Context, p projects.Project, n need, in answerIn) (written bool, note string, err error) {
	a := projects.Answer{Need: in.Need, Answer: in.Answer, Value: in.Value, DeclaredBy: "you", Date: time.Now().Format("2006-01-02")}
	sum := s.storedSummaryOf(p)
	block := discover.AnswerBlock(discover.Answer{Need: a.Need, Answer: a.Answer, Value: a.Value, DeclaredBy: a.DeclaredBy, Date: a.Date})
	if n.Kind == "result_folder" && in.Answer == "mark_run" {
		block = discover.ManualRunBlock(strings.TrimPrefix(n.ID, "results:")) + block
	}
	if p.HostID != 0 {
		// on a host the file is written through the host's ssh connection, under the rules of ADR 0039
		written, note, err = s.appendOnHost(ctx, p, block, in.Write, sum.Proposed)
		if err != nil {
			return false, "", err
		}
	} else {
		_, file := projectFilePaths(p.Path)
		_, statErr := os.Lstat(file)
		exists := statErr == nil
		if in.Write || exists {
			base := ""
			if exists {
				data, rerr := os.ReadFile(file)
				if rerr != nil {
					return false, "", rerr
				}
				base = string(data)
			} else {
				base = sum.Proposed
			}
			out, aerr := discover.Append([]byte(base), block)
			if aerr != nil {
				return false, "", aerr
			}
			if werr := writeProjectFile(p.Path, out); werr != nil {
				return false, "", werr
			}
			written = true
		} else {
			note = "not written to the project: there is no .vesqira/project.toml yet; the answer is kept in the catalog"
		}
	}
	a.Written = written
	return written, note, s.Projects.SetAnswer(p.ID, a)
}

// proposalSource says who proposed an orientation declaration nobody has confirmed: a run import or an analysis.
func proposalSource(by string) string {
	if n, ok := discover.RuleName(by); ok {
		return "your import rule " + n + ": declared by the rule, not by you; confirm it to make it your own declaration"
	}
	if by == analysisAuthor {
		return "the analysis that made this map (" + by + "): a proposal, not your declaration"
	}
	return "the run import (" + by + "): a proposal, not your declaration"
}

// authorText names who made a declaration that is not the researcher's own: a rule of theirs, or an adapter or analysis by its label.
func authorText(by string) string {
	if n, ok := discover.RuleName(by); ok {
		return "your rule " + n
	}
	return by
}
