package runs

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// Profiles from a project folder (ADR 0068, slice 6). A researcher puts one JSON file per tool in the project's
// .vesqira/profiles/. A file is held to every check a shipped profile passes (unknown fields refused, closed vocabularies, character
// sets, bounded patterns) and to three more: it cannot take the name or the run kind of a profile of this program, it cannot carry a
// job template, and it must declare something to recognise or read. What it supplies is declared by the researcher's own file, never
// verified.

// MaxProjectProfileBytes bounds one profile file.
const MaxProjectProfileBytes = 64 << 10

// projectProfileDoc is what a project's profile file holds: one profile.
type projectProfileDoc struct {
	Format  int             `json:"format"`
	About   string          `json:"about"`
	Profile json.RawMessage `json:"profile"`
}

// WithProject is this registry plus the profile in a project's file, read from file (project-relative, for messages and the
// profile's origin). r holds the profiles of this program and the project's earlier files; the result is a new registry and r is
// not changed. A refusal says what is wrong with the file.
func (r *Registry) WithProject(data []byte, file string) (*Registry, *Profile, error) {
	if len(data) > MaxProjectProfileBytes {
		return nil, nil, fmt.Errorf("%s is larger than %d KiB", file, MaxProjectProfileBytes>>10)
	}
	var doc projectProfileDoc
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", file, err)
	}
	if doc.Format != 1 || len(doc.Profile) == 0 {
		return nil, nil, fmt.Errorf(`%s: expected {"format": 1, "profile": {...}}`, file)
	}
	var p Profile
	pd := json.NewDecoder(bytes.NewReader(doc.Profile))
	pd.DisallowUnknownFields()
	if err := pd.Decode(&p); err != nil {
		return nil, nil, fmt.Errorf("%s: profile: %w", file, err)
	}
	if p.Template != nil {
		return nil, nil, fmt.Errorf("%s: a job template cannot come from a project folder: jobs are written from the templates this program ships (ADR 0068, slice 6)", file)
	}
	if p.Orientation.Worker != "" {
		return nil, nil, fmt.Errorf("%s: orientation names an entry of this program's header-card table (%q), which a project profile cannot reach; declare the convention itself as an object with frame, unit and statistics", file, p.Orientation.Worker)
	}
	if d := p.Orientation.Decl; d != nil {
		if err := d.Check(); err != nil {
			return nil, nil, fmt.Errorf("%s: %w", file, err)
		}
		if !p.hasRole(RoleOrientation) {
			return nil, nil, fmt.Errorf("%s: the profile declares an orientation but no output with the role %q to apply it to", file, RoleOrientation)
		}
	}
	if p.Record == nil && len(p.Files) == 0 && p.Outputs == nil && p.Decomposition == nil {
		return nil, nil, fmt.Errorf("%s: the profile declares nothing to recognise or read (no record, files, outputs or decomposition)", file)
	}
	for _, q := range r.profiles {
		if q.Origin != nil {
			continue
		}
		switch {
		case q.Name == p.Name:
			return nil, nil, fmt.Errorf("%s: the name %q belongs to a profile of this program; a project profile cannot redefine it, pick another name", file, p.Name)
		case q.Kind == p.Kind:
			return nil, nil, fmt.Errorf("%s: the run kind %q belongs to a profile of this program (%s); pick another kind", file, p.Kind, q.Label())
		}
	}
	sum := sha256.Sum256(data)
	p.Origin = &Origin{File: file, SHA: hex.EncodeToString(sum[:])}
	one, err := newRegistry(r.roles, []*Profile{&p}, r)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", file, err)
	}
	if r.ByLabel(p.Label()) != nil {
		return nil, nil, fmt.Errorf("%s: the profile %s is already defined", file, p.Label())
	}
	return &Registry{roles: r.roles, profiles: append(append([]*Profile{}, r.profiles...), one.profiles...)}, &p, nil
}

// FromProject says whether the profile was read from a project folder.
func (p *Profile) FromProject() bool { return p != nil && p.Origin != nil }
