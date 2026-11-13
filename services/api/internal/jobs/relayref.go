package jobs

import "fmt"

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
