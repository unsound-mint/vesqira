package jobs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/BurntSushi/toml"
)

// Run a recorded configuration on another input cube (ADR 0014 amendment, issue 121). The draft is rendered from the
// recorded settings, never from the script text (ADR 0048): the config text a job started from, its overrides and its
// resources are kept, and the one thing that changes is the input file. This file holds what that needs from the jobs
// side: the record kept with the draft, the configuration hash, and the two places an input path lives in a config.

// ConfigHashVersion names the definition of ConfigHash, so a change to it is a new value and never a silent redefinition.
const ConfigHashVersion = "vesqira-config/1"

// InputRerun is kept in the request of a job drafted by "run this configuration on another cube". It says which run the
// configuration came from and what the new input was when the draft was made.
type InputRerun struct {
	SourceRunID      int64  `json:"sourceRunId"`
	SourceConfigHash string `json:"sourceConfigHash"`
	// ConfigHash is the new draft's own hash. It equals SourceConfigHash by construction (only the input path differs, and
	// the hash leaves it out); it is stored so a later reader does not have to re-derive it.
	ConfigHash    string             `json:"configHash"`
	PreviousInput string             `json:"previousInput"` // the input path in the source run's config, as written there
	Input         RerunInputIdentity `json:"input"`
	// Notes are the facts a person should read before submitting (a different pixel scale, a unit that could not be compared).
	Notes []string `json:"notes"`
}

// RerunInputIdentity is what the catalog knew about the chosen cube when the draft was made (ADR 0053). Sampled and full are
// empty when none was computed; neither is ever computed for the draft, because a full hash reads the whole file.
type RerunInputIdentity struct {
	DatasetID  int64  `json:"datasetId"`
	Name       string `json:"name"`
	HostID     int64  `json:"hostId"`
	Path       string `json:"path"`
	SizeBytes  int64  `json:"sizeBytes"`
	ModifiedAt int64  `json:"modifiedAt"` // seconds since 1970, as the catalog recorded it
	SampledFP  string `json:"sampledFp"`  // sampled/1, not a hash of the whole file
	FullSHA256 string `json:"fullSha256"` // SHA-256 of every byte, when the catalog has one for this size and time
}

// InputKey is the config key that names the input cube: "input_file" in a VGT config, "input" in a ROHSA config (the
// pipeline's two config formats differ here).
func InputKey(template string) string {
	if template == "rohsa" {
		return "input"
	}
	return "input_file"
}

// InputPathOf is the input cube path a config names, or "" when it names none that is a string.
func InputPathOf(template, config string) string {
	var cfg struct {
		Input map[string]any `toml:"input"`
	}
	if _, err := toml.Decode(config, &cfg); err != nil {
		return ""
	}
	s, _ := cfg.Input[InputKey(template)].(string)
	return s
}

// SetInputFile returns config text with the input path replaced and nothing else changed (the other lines stay byte for byte).
// The result is read back and must name the path, so a layout this line editor cannot follow is refused, not guessed at.
func SetInputFile(template, text, path string) (string, error) {
	if !ValidPath(path) {
		return "", fmt.Errorf("%w: the input file must be an absolute path without spaces or quotes", ErrInvalid)
	}
	var out string
	if template == "rohsa" {
		out = strings.Join(setROHSALine(strings.Split(text, "\n"), "input", "input", `"`+path+`"`), "\n")
	} else {
		var err error
		if out, err = SetConfigValues(text, map[string]string{"input_file": path}); err != nil {
			return "", err
		}
	}
	if InputPathOf(template, out) != path {
		return "", fmt.Errorf("%w: the input line of the config could not be replaced (is it written over several lines?)", ErrInvalid)
	}
	return out, nil
}

// pathKeys are config keys that name where a run reads or writes, not what it computes. They are left out of the hash: two
// runs that differ only in them used the same configuration.
var pathKeys = map[string]bool{"input_file": true, "input": true, "output_dir": true}

// ConfigHash identifies the configuration a run used, apart from where its input and output were: the SHA-256 of the whole
// config (every table, list and key, not only the settings Vesqira edits) after the request's overrides are applied, with the
// input and output paths dropped, written as canonical JSON (sorted keys). Resources (CPUs, memory, time) and the code folder
// are not part of it: they say how the run was carried out, and the run's environment says which code it was.
// The second result is false when the config cannot be read, so no hash is given and none is guessed.
func ConfigHash(template string, req VGTRequest) (string, bool) {
	if strings.TrimSpace(req.BaseConfigText) == "" {
		return "", false
	}
	set := map[string]string{}
	for k, v := range req.Set {
		set[k] = v
	}
	var text string
	var err error
	switch template {
	case "vgt":
		delete(set, "output_dir") // Vesqira's own per job; never part of the configuration
		delete(set, "input_file")
		text, err = SetConfigValues(req.BaseConfigText, set)
	case "rohsa":
		text, err = SetROHSAConfig(req.BaseConfigText, set, "")
	default:
		return "", false
	}
	if err != nil {
		return "", false
	}
	var m map[string]any
	if _, err := toml.Decode(text, &m); err != nil {
		return "", false
	}
	dropPaths(m, 0)
	b, err := json.Marshal(m)
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256([]byte(ConfigHashVersion + "\n" + template + "\n" + string(b)))
	return hex.EncodeToString(sum[:]), true
}

func dropPaths(m map[string]any, depth int) {
	for k, v := range m {
		if t, ok := v.(map[string]any); ok {
			if depth < 3 {
				dropPaths(t, depth+1)
			}
			continue
		}
		if pathKeys[k] {
			delete(m, k)
		}
	}
}

// InputRerunOf is the record kept with a job drafted from another run's configuration, or nil.
func (j Job) InputRerunOf() *InputRerun {
	r, _ := j.configRequestAny()
	return r.InputRerun
}
