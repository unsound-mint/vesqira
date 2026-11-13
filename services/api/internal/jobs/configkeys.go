package jobs

import (
	"regexp"
	"strings"

	"github.com/unsound-mint/vesqira/services/api/internal/jobtemplate"
)

var (
	numRe    = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?([eE][-+]?[0-9]+)?$`)
	intRe    = regexp.MustCompile(`^[0-9]{1,9}$`)
	posIntRe = regexp.MustCompile(`^[1-9][0-9]{0,8}$`)
)

// The VGT config editor. The keys, their types and ranges are the VGT.jl profile's template (internal/runs/run_profiles.json); the
// functions here keep the names the rest of the program has used, and golden_test.go holds their output to files taken before
// the template became data.

func vgtTemplate() *jobtemplate.Template { return ShippedTemplate("vgt") }

// SetConfigValues returns VGT config text with the given keys set. Only the keys of the profile's template are accepted, each
// with a value of its own type, so the config can be adjusted from the UI without free-form text reaching a script. The job's
// own output_dir may be given with the other values.
func SetConfigValues(text string, values map[string]string) (string, error) {
	out := ""
	rest := make(map[string]string, len(values))
	for k, v := range values {
		if k == "output_dir" {
			out = v
		} else {
			rest[k] = v
		}
	}
	return SetConfig(vgtTemplate(), text, rest, out)
}

// CheckSetting says whether value is acceptable for the editable VGT setting key.
func CheckSetting(key, value string) error { return CheckKey(vgtTemplate(), key, value) }

func keyLineRe(key string) *regexp.Regexp {
	return regexp.MustCompile(`^\s*` + regexp.QuoteMeta(key) + `\s*=`)
}

// setLine replaces `key = ...` inside [section] or adds it at the end of that section (or a new section).
func setLine(lines []string, section, key, rendered string) []string {
	inSection, secEnd := false, -1
	keyRe := keyLineRe(key)
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			if inSection && secEnd < 0 {
				secEnd = i
			}
			inSection = t == "["+section+"]"
			continue
		}
		if inSection && keyRe.MatchString(l) {
			lines[i] = key + " = " + rendered
			return lines
		}
	}
	if inSection && secEnd < 0 {
		secEnd = len(lines)
	}
	if secEnd >= 0 {
		return append(lines[:secEnd], append([]string{key + " = " + rendered}, lines[secEnd:]...)...)
	}
	return append(lines, "", "["+section+"]", key+" = "+rendered)
}
