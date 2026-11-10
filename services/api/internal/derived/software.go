package derived

import (
	"encoding/json"
	"regexp"
)

// The software that produced a derived file is recorded as declared by the process that ran it, and for an analysis run on
// a host that process is a file written on that host. It therefore goes through a fixed shape before it is stored or shown:
// the four tools the analysis reports, each a short version string. Anything else is dropped, not repaired, so provenance
// never carries text an account on a cluster chose, and a value that does not look like a version is left out (unknown),
// not guessed.
var softwareKeys = []string{"python", "astropy", "numpy", "reproject"}

var versionRe = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+_-]{0,39}$`)

// CleanSoftware returns the canonical JSON object of the known tools in raw whose versions look like versions, and {}
// when raw is anything else.
func CleanSoftware(raw json.RawMessage) json.RawMessage {
	out := map[string]string{}
	var in map[string]json.RawMessage
	if len(raw) <= 4096 && json.Unmarshal(raw, &in) == nil {
		for _, k := range softwareKeys {
			var v string
			if json.Unmarshal(in[k], &v) == nil && versionRe.MatchString(v) {
				out[k] = v
			}
		}
	}
	b, _ := json.Marshal(out)
	return b
}
