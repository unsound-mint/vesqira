package jobs

import (
	"regexp"
	"strconv"
)

// Requested is what a batch script asks the scheduler for, read from its #SBATCH lines. A value the script does not state is
// zero: unknown, not a default.
type Requested struct {
	CPUs        int   `json:"cpus"`
	MemBytes    int64 `json:"memBytes"`
	TimeSeconds int64 `json:"timeSeconds"`
}

var (
	sbCPUs = regexp.MustCompile(`(?m)^#SBATCH --cpus-per-task=([0-9]{1,4})\s*$`)
	sbMem  = regexp.MustCompile(`(?m)^#SBATCH --mem=([0-9]{1,4}[MG])\s*$`)
	sbTime = regexp.MustCompile(`(?m)^#SBATCH --time=(\S+)\s*$`)
)

// RequestedOf reads the resources a script requests. Only the forms the templates write are understood; anything else
// stays unknown (zero).
func RequestedOf(script string) Requested {
	var r Requested
	if m := sbCPUs.FindStringSubmatch(script); m != nil {
		r.CPUs, _ = strconv.Atoi(m[1])
	}
	if m := sbMem.FindStringSubmatch(script); m != nil {
		if b, _, _, ok := parseSize(m[1], 1<<20); ok {
			r.MemBytes = b
		}
	}
	if m := sbTime.FindStringSubmatch(script); m != nil {
		if d, ok := parseDuration(m[1]); ok {
			r.TimeSeconds = int64(d)
		}
	}
	return r
}

// CreateDrafts stores several drafts in one transaction: all of them or none. Each is validated as CreateDraft does.
func (s *Store) CreateDrafts(js []Job) ([]Job, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	ids := make([]int64, 0, len(js))
	for _, j := range js {
		id, err := insertDraft(tx, j)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	out := make([]Job, 0, len(ids))
	for _, id := range ids {
		j, err := s.Get(id)
		if err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, nil
}

// DeleteDrafts removes drafts that nothing refers to yet (a sweep whose record could not be written). A job that is not a
// draft is never touched.
func (s *Store) DeleteDrafts(ids []int64) {
	for _, id := range ids {
		_, _ = s.db.Exec(`DELETE FROM jobs WHERE id=? AND state=?`, id, StateDraft)
	}
}
