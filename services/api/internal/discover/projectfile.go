package discover

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"
)

// ProjectFilePath is where a project keeps its own layout and the answers its researcher gave, inside the folder.
const ProjectFilePath = ".vesqira/project.toml"

// Answer is something the researcher said, recorded in project.toml. DeclaredBy is "you", or "you, for the folder tree X" when
// one answer was given for every folder under X: an answer is never written on anyone's behalf.
type Answer struct {
	Need       string `toml:"need" json:"need"`
	Answer     string `toml:"answer" json:"answer"`
	Value      string `toml:"value,omitempty" json:"value"`
	DeclaredBy string `toml:"declared_by" json:"declaredBy"`
	Date       string `toml:"date" json:"date"`
}

type manualRunToml struct {
	Path      string `toml:"path"`
	Name      string `toml:"name"`
	Commit    string `toml:"commit"`
	StartedAt string `toml:"started_at"`
}

// ProjectFile is what project.toml may contain. Unknown keys are ignored, so a file written by hand keeps working.
type ProjectFile struct {
	Project struct {
		Name string `toml:"name"`
	} `toml:"project"`
	Layout struct {
		Data    []string `toml:"data"`
		Runs    []string `toml:"runs"`
		Configs []string `toml:"configs"`
		Jobs    []string `toml:"jobs"`
		Skip    []string `toml:"skip"`
	} `toml:"layout"`
	Runs struct {
		Manual []manualRunToml `toml:"manual"`
	} `toml:"runs"`
	Hosts map[string]struct {
		Repo string `toml:"repo"`
	} `toml:"hosts"`
	Answers []Answer `toml:"answers"`
	// Import holds the researcher's import rules (ADR 0058): the first matching rule wins.
	Import struct {
		Rules []Rule `toml:"rules"`
	} `toml:"import"`
}

// MaxProjectFile bounds what is read of project.toml.
const MaxProjectFile = 256 << 10

func ParseProjectFile(data []byte) (ProjectFile, error) {
	var p ProjectFile
	if len(data) > MaxProjectFile {
		return p, errors.New("project.toml is too large")
	}
	if _, err := toml.Decode(string(data), &p); err != nil {
		return ProjectFile{}, fmt.Errorf("project.toml is not valid TOML: %v", err)
	}
	return p, nil
}

// Scan options this file asks for.
func (p ProjectFile) ScanLayout() Layout {
	return Layout{Data: p.Layout.Data, Runs: p.Layout.Runs, Configs: p.Layout.Configs, Jobs: p.Layout.Jobs, Skip: p.Layout.Skip}
}

func (p ProjectFile) ManualRuns() []ManualRun {
	out := make([]ManualRun, 0, len(p.Runs.Manual))
	for _, m := range p.Runs.Manual {
		out = append(out, ManualRun{Path: m.Path, Name: m.Name, Commit: m.Commit, StartedAt: m.StartedAt})
	}
	return out
}

// HostRepo is the code folder on a host the researcher declared, from [hosts.<alias>] or from an answer.
func (p ProjectFile) HostRepo(alias string) string {
	repo := p.Hosts[alias].Repo
	for _, a := range p.Answers {
		if a.Need == "host-repo:"+alias && a.Answer == "declare" {
			repo = a.Value
		}
	}
	return repo
}

func tomlString(s string) string {
	var b strings.Builder
	_ = toml.NewEncoder(&b).Encode(map[string]string{"v": s})
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(b.String()), "v = "))
}

// AnswerBlock is the TOML text of one answer, appended to the file (history is kept: the last answer for a need wins).
func AnswerBlock(a Answer) string {
	var b strings.Builder
	b.WriteString("\n[[answers]]\nneed = " + tomlString(a.Need) + "\nanswer = " + tomlString(a.Answer) + "\n")
	if a.Value != "" {
		b.WriteString("value = " + tomlString(a.Value) + "\n")
	}
	b.WriteString("declared_by = " + tomlString(a.DeclaredBy) + "\ndate = " + tomlString(a.Date) + "\n")
	return b.String()
}

// ManualRunBlock marks a folder as a run, as the researcher's own statement.
func ManualRunBlock(path string) string {
	return "\n[[runs.manual]]\npath = " + tomlString(path) + "\n"
}

// Append adds a block to the end of the file text and checks that the result is still valid TOML, so an append can
// never leave the researcher with a file that does not load. The original text is otherwise untouched (comments kept).
func Append(existing []byte, block string) ([]byte, error) {
	out := append(bytes.TrimRight(existing, "\n"), '\n')
	out = append(out, block...)
	if _, err := ParseProjectFile(out); err != nil {
		return nil, fmt.Errorf("the answer could not be added without breaking the file (%v); edit project.toml by hand", err)
	}
	return out, nil
}
