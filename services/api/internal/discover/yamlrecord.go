package discover

import (
	"errors"

	"go.yaml.in/yaml/v4"
)

// maxYAMLDepth bounds nesting so a hostile record cannot exhaust the stack while it is walked or decoded.
const maxYAMLDepth = 64

var (
	errYAMLAlias    = errors.New("uses YAML anchors or aliases")
	errYAMLDeep     = errors.New("is nested too deeply")
	errYAMLNotMap   = errors.New("is not a mapping of settings")
	errYAMLNotValid = errors.New("is not valid YAML")
)

// parseYAMLRecord decodes a YAML run record (already capped at runs.MaxMetadataBytes by the caller) into the same
// shape json.Unmarshal gives fromJSON: a map of string keys to string, bool, number, time.Time, map or slice.
// Anchors and aliases are refused outright, so no document can expand beyond its own size (the "billion laughs"
// shape); the decoder only builds plain Go values from tags and never runs code.
func parseYAMLRecord(data []byte) (map[string]any, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, errYAMLNotValid
	}
	if doc.Kind == 0 {
		return nil, errYAMLNotMap // an empty file
	}
	if err := checkYAMLNode(&doc, 0); err != nil {
		return nil, err
	}
	var m map[string]any
	if err := doc.Decode(&m); err != nil || m == nil {
		return nil, errYAMLNotMap
	}
	return normalizeYAML(m).(map[string]any), nil
}

func checkYAMLNode(n *yaml.Node, depth int) error {
	if depth > maxYAMLDepth {
		return errYAMLDeep
	}
	if n.Kind == yaml.AliasNode || n.Anchor != "" {
		return errYAMLAlias
	}
	for _, c := range n.Content {
		if err := checkYAMLNode(c, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// normalizeYAML maps the integer kinds the YAML decoder produces onto int64, the one integer kind the settings
// rules know (JSON numbers arrive as float64, TOML integers as int64).
func normalizeYAML(v any) any {
	switch x := v.(type) {
	case int:
		return int64(x)
	case uint64:
		if x <= 1<<63-1 {
			return int64(x)
		}
		return float64(x)
	case map[string]any:
		for k, e := range x {
			x[k] = normalizeYAML(e)
		}
		return x
	case []any:
		for i, e := range x {
			x[i] = normalizeYAML(e)
		}
		return x
	}
	return v
}
