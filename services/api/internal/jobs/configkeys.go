package jobs

import (
	"fmt"
	"regexp"
	"strings"
)

type configKey struct {
	Section string
	Kind    string // number | enum | path | int | posint (a whole number of at least 1)
	Enum    []string
}

var configKeys = map[string]configKey{
	"noise":                  {"noise", "number", nil},
	"noise_mode":             {"noise", "enum", []string{"scalar", "channel", "spatial", "separable", "auto"}},
	"noise_tile_size":        {"noise", "int", nil},
	"noise_sigma_multiplier": {"noise", "number", nil},
	"dn":                     {"vgt", "int", nil},
	"ker":                    {"vgt", "int", nil},
	"min_valid":              {"vgt", "int", nil},
	"angle_frame":            {"vgt", "enum", []string{"pixel", "galactic", "icrs"}}, // the PSIFRAME card of the outputs
	"output_bin":             {"vgt", "posint", nil},
	"input_file":             {"input", "path", nil},
	"output_dir":             {"input", "path", nil},
}

var (
	numRe    = regexp.MustCompile(`^-?[0-9]+(\.[0-9]+)?([eE][-+]?[0-9]+)?$`)
	intRe    = regexp.MustCompile(`^[0-9]{1,9}$`)
	posIntRe = regexp.MustCompile(`^[1-9][0-9]{0,8}$`)
)

// SetConfigValues returns config text with the given keys set. Only the keys in the table above are accepted, each
// with a value of its own type, so the config can be adjusted from the UI without free-form text reaching a script.
func SetConfigValues(text string, values map[string]string) (string, error) {
	lines := strings.Split(text, "\n")
	for key, val := range values {
		spec, ok := configKeys[key]
		if !ok {
			return "", fmt.Errorf("%w: %q is not an editable setting", ErrInvalid, key)
		}
		var rendered string
		switch spec.Kind {
		case "number":
			if !numRe.MatchString(val) {
				return "", fmt.Errorf("%w: %s must be a number", ErrInvalid, key)
			}
			rendered = val
		case "int":
			if !intRe.MatchString(val) {
				return "", fmt.Errorf("%w: %s must be a whole number", ErrInvalid, key)
			}
			rendered = val
		case "posint":
			if !posIntRe.MatchString(val) {
				return "", fmt.Errorf("%w: %s must be a whole number, 1 or more", ErrInvalid, key)
			}
			rendered = val
		case "enum":
			found := false
			for _, e := range spec.Enum {
				found = found || e == val
			}
			if !found {
				return "", fmt.Errorf("%w: %s must be one of %s", ErrInvalid, key, strings.Join(spec.Enum, ", "))
			}
			rendered = `"` + val + `"`
		case "path":
			if !ValidPath(val) {
				return "", fmt.Errorf("%w: %s must be an absolute path without spaces or quotes", ErrInvalid, key)
			}
			rendered = `"` + val + `"`
		}
		lines = setLine(lines, spec.Section, key, rendered)
	}
	return strings.Join(lines, "\n"), nil
}

// CheckSetting says whether value is acceptable for the editable setting key, by the rules SetConfigValues applies.
func CheckSetting(key, value string) error {
	_, err := SetConfigValues("", map[string]string{key: value})
	return err
}

// setLine replaces `key = ...` inside [section] or adds it at the end of that section (or a new section).
func setLine(lines []string, section, key, rendered string) []string {
	inSection, secEnd := false, -1
	keyRe := regexp.MustCompile(`^\s*` + regexp.QuoteMeta(key) + `\s*=`)
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
