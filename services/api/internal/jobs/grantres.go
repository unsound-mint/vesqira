package jobs

import (
	"fmt"
	"unicode/utf8"
)

// AddDraftNote puts a sentence in front of a draft's note (the whole cut to MaxNote characters, so the sentence survives a long
// requester's note), for example which standing approval let it be submitted (ADR 0074). Only a draft takes it.
func (s *Store) AddDraftNote(id int64, sentence string) error {
	var note string
	if err := s.db.QueryRow(`SELECT note FROM jobs WHERE id=? AND state=?`, id, StateDraft).Scan(&note); err != nil {
		return fmt.Errorf("%w: only a draft takes a note", ErrInvalid)
	}
	if note != "" {
		note = sentence + " " + note
	} else {
		note = sentence
	}
	if utf8.RuneCountInString(note) > MaxNote {
		note = string([]rune(note)[:MaxNote])
	}
	_, err := s.db.Exec(`UPDATE jobs SET note=? WHERE id=? AND state=?`, note, id, StateDraft)
	return err
}
