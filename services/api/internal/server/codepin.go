package server

import (
	"strings"

	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
)

// codePin says which commit a draft's script insists on, and why. It is the one sentence every approval view shows (the rerun
// check, a sweep's review, a relayed request's approval), computed from the request the draft was rendered from.
//
// Two things can ask for a pin (ADR 0054 and ADR 0068): the commit of the run being repeated (a strict rerun, or a relayed run
// request's draft), and the commit the tool's profile was written against, which a person asks for with pinProfileCommit. When
// both apply the run's commit wins, because it is the stronger claim (it describes the code that made the result), and the
// profile's pin is reported as not used.
type codePin struct {
	// Commit is the revision the script stops unless the code folder is at, or "" when the script insists on none.
	Commit string `json:"commit"`
	// Basis is "run" (the commit of the run being repeated), "profile" (the revision the profile was written against) or "".
	Basis string `json:"basis"`
	// Why is the sentence a person reads.
	Why string `json:"why"`
}

func (s *Server) codePinOf(template string, req jobs.VGTRequest) codePin {
	profile := ""
	if t, label := s.profiles().TemplateProfile(template); t != nil {
		profile = label
	}
	const stop = " The job stops before the program starts unless the code folder on the host is at that commit with no changed tracked file."
	switch {
	case req.ExpectCommit != "":
		why := "The script insists on commit " + req.ExpectCommit + ", the commit of the run it repeats." + stop
		if req.PinProfile {
			if t := jobs.ShippedTemplate(template); t != nil && t.Repo.Commit != req.ExpectCommit {
				why += " The profile's own commit (" + t.Repo.Commit + ") was also asked for; the run's commit is the stronger claim, so it is the one used."
			}
		}
		return codePin{Commit: req.ExpectCommit, Basis: "run", Why: why}
	case req.PinProfile:
		t := jobs.ShippedTemplate(template)
		if t == nil {
			return codePin{}
		}
		from := "the revision the tool's profile was written against"
		if profile != "" {
			from = "the revision the profile " + profile + " was written against"
		}
		return codePin{Commit: t.Repo.Commit, Basis: "profile", Why: "The script insists on commit " + t.Repo.Commit + ", " + from + ", because that was asked for. It is not the commit of any earlier run." + stop}
	}
	return codePin{Why: "The script insists on no commit: the job runs the code as it is on the host when it starts."}
}

// pinNote is codePinOf as one line for a list of notes, empty when nothing is pinned.
func (s *Server) pinNote(template string, req jobs.VGTRequest) string {
	p := s.codePinOf(template, req)
	if p.Commit == "" {
		return ""
	}
	return strings.TrimSpace(p.Why)
}
