package server

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/discover"
	"github.com/unsound-mint/vesqira/services/api/internal/projects"
)

// treeAnswerIn answers every open missing_bunit question under a folder at once. Without Confirm it only counts what the
// answer would reach.
type treeAnswerIn struct {
	Folder  string `json:"folder"` // slash-separated, relative to the project folder; "." is the whole project
	Answer  string `json:"answer"` // declare | unknown
	Value   string `json:"value"`
	Confirm bool   `json:"confirm"`
	Write   bool   `json:"write"`
}

type treeAnswerOut struct {
	Folder  string   `json:"folder"`
	Applied bool     `json:"applied"`
	Folders int      `json:"folders"` // open questions (folders) the answer reaches
	Files   int      `json:"files"`
	Sample  []string `json:"sample"` // the first folders, at most maxNeedPaths
	// Answered counts the questions under the folder that already have an answer: they are left as they are.
	Answered         int      `json:"answered"`
	WrittenToProject bool     `json:"writtenToProject"`
	Note             string   `json:"note,omitempty"`
	Open             int      `json:"open"`
	Failed           []string `json:"failed,omitempty"`
}

// treeScope is how a folder-tree answer is recorded: the answer is the researcher's, given for the tree, not for each file.
func treeScope(folder string) string { return "you, for the folder tree " + folder }

func underFolder(f, ancestor string) bool {
	return ancestor == "." || f == ancestor || strings.HasPrefix(f, ancestor+"/")
}

func validTreeFolder(f string) bool {
	if f == "." {
		return true
	}
	if !validText(f, 1000) || f != path.Clean(f) || path.IsAbs(f) || f == ".." || strings.HasPrefix(f, "../") {
		return false
	}
	return true
}

// answerTree answers each open missing_bunit question under an ancestor folder with the same answer. The questions are
// derived here, from the catalog, never taken from the request, so the request cannot reach a file the list would not
// ask about. A question that already has an answer is not touched; every answer is recorded as the researcher's, with
// the folder tree as its scope. A declared unit goes through the same step as a per-folder answer.
func (s *Server) answerTree(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectFrom(w, r)
	if !ok {
		return
	}
	var in treeAnswerIn
	if !body(w, r, 8<<10, &in, true) {
		return
	}
	if !validTreeFolder(in.Folder) {
		fail(w, 422, "give a folder inside the project, written relative to it (\".\" is the whole project)")
		return
	}
	if in.Answer != "declare" && in.Answer != "unknown" {
		fail(w, 422, "a folder tree can be answered with a declared unit or left unknown")
		return
	}
	ws := wsOf(r)
	ns, err := s.computeNeeds(ws, p)
	if err != nil {
		log.Printf("projects: %v", err)
		fail(w, 500, "internal error")
		return
	}
	stored, err := s.Projects.Answers(p.ID)
	if err != nil {
		log.Printf("projects: %v", err)
		fail(w, 500, "internal error")
		return
	}
	var match []need
	out := treeAnswerOut{Folder: in.Folder, Sample: []string{}}
	for _, n := range ns {
		if n.Kind != "missing_bunit" || !underFolder(n.Scope.Folder, in.Folder) {
			continue
		}
		if _, has := stored[n.ID]; has { // answered before, even if some of its files have lost the unit since: never overwritten
			out.Answered++
			continue
		}
		if n.State != "open" || len(n.datasetIDs) == 0 {
			continue
		}
		if err := n.validate(answerIn{Need: n.ID, Answer: in.Answer, Value: in.Value}); err != nil {
			fail(w, 422, strings.TrimPrefix(err.Error(), "answer: "))
			return
		}
		match = append(match, n)
		out.Folders++
		out.Files += len(n.datasetIDs)
		if len(out.Sample) < maxNeedPaths {
			out.Sample = append(out.Sample, n.Scope.Folder)
		}
	}
	out.Open = openCount(ns)
	if !in.Confirm || len(match) == 0 {
		writeJSON(w, 200, out)
		return
	}

	// one write for the whole tree (on a host that is one ssh write, not one per folder); the file first: a refused write changes nothing
	date := time.Now().Format("2006-01-02")
	var block strings.Builder
	for _, n := range match {
		block.WriteString(discover.AnswerBlock(discover.Answer{Need: n.ID, Answer: in.Answer, Value: in.Value, DeclaredBy: treeScope(in.Folder), Date: date}))
	}
	written, note, err := s.writeProjectBlock(r.Context(), p, block.String(), in.Write)
	if err != nil {
		if errors.Is(err, errProjectFile) || strings.Contains(err.Error(), "project.toml") {
			fail(w, 409, err.Error())
			return
		}
		log.Printf("projects: %v", err)
		fail(w, 500, "internal error")
		return
	}
	done := 0
	for _, n := range match {
		ain := answerIn{Need: n.ID, Answer: in.Answer, Value: in.Value}
		if err := s.applyAnswer(ws, n, ain); err != nil {
			log.Printf("projects: tree answer %s: %v", n.ID, err)
			out.Failed = append(out.Failed, n.Scope.Folder)
			continue
		}
		a := projects.Answer{Need: n.ID, Answer: in.Answer, Value: in.Value, DeclaredBy: treeScope(in.Folder), Date: date, Written: written}
		if err := s.Projects.SetAnswer(p.ID, a); err != nil {
			log.Printf("projects: tree answer %s: %v", n.ID, err)
			out.Failed = append(out.Failed, n.Scope.Folder)
			continue
		}
		done++
	}
	s.Audit.RecordCtx(r.Context(), "project questions answered for a folder tree", "project", p.ID, fmt.Sprintf("missing_bunit under %s: %d of %d", in.Folder, done, len(match)))
	if len(out.Failed) > 0 && done == 0 {
		fail(w, 500, "internal error")
		return
	}
	ns, _ = s.computeNeeds(ws, p)
	out.Applied, out.WrittenToProject, out.Note, out.Open = true, written, note, openCount(ns)
	out.Folders = done
	writeJSON(w, 200, out)
}
