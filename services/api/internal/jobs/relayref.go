package jobs

import (
	"fmt"

	"github.com/unsound-mint/vesqira/services/api/internal/jobtemplate"
)

// FixedByVesqira reports whether key of a shipped template is one Vesqira sets or reads as a path: the output folder it makes
// per job, or any key of the path kind. A request can neither set nor sweep such a key.
func FixedByVesqira(template, key string) bool {
	t := ShippedTemplate(template)
	if t == nil {
		return false
	}
	k := t.Key(key)
	return k != nil && (k.Internal || k.Kind == jobtemplate.KindPath)
}

// OverlapIn reports whether two settings of a shipped template change a common config key (a tie and one of its keys do).
func OverlapIn(template, a, b string) bool {
	t := ShippedTemplate(template)
	return t != nil && Overlap(t, a, b)
}

// CarriedSet is the settings of a stored request as a new draft takes them over: the keys Vesqira sets per job are dropped.
// A request stored before VGT drafts refused them could hold an output folder the job never used (its own folder always won),
// so dropping it changes nothing the earlier job ran with.
func CarriedSet(template string, set map[string]string) map[string]string {
	out := make(map[string]string, len(set))
	t := ShippedTemplate(template)
	for k, v := range set {
		if t != nil {
			if spec := t.Key(k); spec != nil && spec.Internal {
				continue
			}
		}
		out[k] = v
	}
	return out
}

// SetRequestRef records which relayed run request a draft was rendered for (ADR 0066 section 3). Only a draft takes one; the
// text is a reference for people, never read back as a path or an id of anything on this computer.
func (s *Store) SetRequestRef(id int64, ref string) error {
	res, err := s.db.Exec(`UPDATE jobs SET request_ref=? WHERE id=? AND state=?`, ref, id, StateDraft)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("%w: only a draft can be tied to a request", ErrInvalid)
	}
	return nil
}

// RequestRef returns the relayed request reference of a job ("" for a job of the owner's own).
func (s *Store) RequestRef(id int64) (string, error) {
	var ref string
	err := s.db.QueryRow(`SELECT request_ref FROM jobs WHERE id=?`, id).Scan(&ref)
	return ref, err
}

// SettingKind is the kind of an editable setting of a shipped template ("number", "int", "enum", "path", ...), and false for a key
// the template does not let a request set. A relayed request may never set a path-kind setting (ADR 0066 section 3). Relayed
// requests take the tools Vesqira ships a template for (vgt, rohsa).
func SettingKind(template, key string) (string, bool) {
	t := ShippedTemplate(template)
	if t == nil {
		return "", false
	}
	return KindOf(t, key)
}

// CheckTemplateSetting validates one setting with the template's own validator.
func CheckTemplateSetting(template, key, value string) error {
	t := ShippedTemplate(template)
	if t == nil {
		return fmt.Errorf("%w: template must be vgt or rohsa", ErrInvalid)
	}
	return CheckKey(t, key, value)
}
