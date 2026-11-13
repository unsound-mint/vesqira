package discover

import (
	"path"
	"strings"

	"github.com/unsound-mint/vesqira/services/api/internal/runs"
)

// readProfileRecord fills a run from the record a tool profile nominated (ADR 0068, item 1). It returns false when the content
// is not the profile's: the file has the profile's name and none of its keys, so the folder is not a run of that tool and the
// next candidate is tried. The profile only says which reader to use and what the outputs are called; the readers validate.
func (s *scanner) readProfileRecord(r *Run, prof *runs.Profile, data []byte) bool {
	if !prof.Claims(data) {
		return false
	}
	switch prof.Record.Reader {
	case runs.ReaderRunMetadata:
		p, err := runs.ParseRecord(prof, data, path.Base(r.Path))
		if err != nil {
			r.Notes = append(r.Notes, r.Record+" could not be read as a "+prof.ToolName()+" run record: "+err.Error())
			return true
		}
		if p.GeneratedAt != "" {
			r.Fields = append(r.Fields, Field{"startedAt", p.GeneratedAt, "declared", r.Record})
		}
		if p.GitCommit != "" {
			r.Fields = append(r.Fields, Field{"commit", p.GitCommit, "declared", r.Record})
		}
		for _, p := range p.Params {
			r.Settings = append(r.Settings, Setting{p.Key, p.Value})
		}
		if len(p.OutputFiles) > 0 {
			r.OutputRoles, r.OutputProducts = map[string]string{}, map[string]string{}
			for product, name := range p.OutputFiles {
				r.OutputRoles[name], r.OutputProducts[name] = p.OutputRoles[product], product
			}
		}
	case runs.ReaderROHSATrace:
		stem := prof.Stem(r.Record)
		p, err := runs.ParseROHSA(data, nil, nil, stem, path.Base(r.Path))
		if err != nil {
			r.Notes = append(r.Notes, r.Record+" could not be read as a ROHSA run trace: "+err.Error())
			return true
		}
		for _, p := range p.Params {
			r.Settings = append(r.Settings, Setting{p.Key, p.Value})
		}
		r.Notes = append(r.Notes, "a ROHSA fit: its raw output is "+stem+".dat; other fits in this folder share this run record's folder")
		// the folder may also hold the record Vesqira wrote for the job that made the fit: the same run, named once
		if rec, trunc, err := s.fs.ReadFile(joinRel(r.Path, "run.json"), runs.MaxMetadataBytes); err == nil && !trunc {
			if id, tpl, ok := jobOf(rec); ok && tpl == "rohsa" {
				r.jobID, r.jobTemplate = id, tpl
				addJob(r, id, "run.json")
			}
		}
	}
	return true
}

// RunRolePrefix starts the built-in role of a file a run's profile places: run_orientation, run_component_maps, ... The prefix keeps
// these apart from the roles a researcher's rule gives (orientation is one of those).
const RunRolePrefix = "run_"

// profileFileRole is the built-in role and reason of a file inside a run folder whose record a profile read, or "" when the
// profile does not place it.
func profileFileRole(reg *runs.Registry, run *Run, rel, format string) (role, why string) {
	prof := reg.ByLabel(run.Profile)
	if prof == nil {
		return "", ""
	}
	base := path.Base(rel)
	if product, ok := run.OutputProducts[base]; ok {
		r := run.OutputRoles[base]
		return RunRolePrefix + r, prof.ToolName() + " output: " + run.Record + " names " + base + " as the run's " + strings.ReplaceAll(product, "_", " ") + " map"
	}
	if prof.Record == nil {
		return "", ""
	}
	vars := map[string]string{"stem": prof.Stem(run.Record)}
	if prof.IsFile(runs.RoleComponentTable, base, vars) {
		return RunRolePrefix + runs.RoleComponentTable, prof.ToolName() + " fit: the raw output of the run " + run.Record + " names (" + base + ")"
	}
	if format == "FITS" || fitsName(strings.ToLower(base)) {
		stem := strings.TrimSuffix(base, path.Ext(base))
		for _, seg := range strings.Split(path.Dir(rel), "/") {
			for _, dir := range prof.FolderDirs() {
				if seg == dir {
					// a file the profile gives a role is that role; any other product of its tree is a product of the run
					role := prof.FolderRoleOfFile(dir, stem)
					if role == "" {
						role = "product"
					}
					return RunRolePrefix + role, prof.ToolName() + " product: a FITS file under " + seg + "/ in a " + prof.ToolName() + " run folder"
				}
			}
		}
	}
	return "", ""
}
