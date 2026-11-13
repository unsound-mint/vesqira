package relay

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"
)

// The key holder's grants (ADR 0074): local-only tables, like the rest of this side.

// Grant states on a mirrored request. Besides these, a request that waits for the key holder holds the Grant* code of why (the
// code is what crosses to the group server with the review; the mirror's GrantNote is the sentence shown here).
const (
	GrantStateWaiting = "waiting" // the grant's jobs-at-once limit is full; tried again each pass
	GrantStatePending = "pending" // the group server did not answer the claim; tried again each pass
	GrantStateUsed    = "used"    // submitted under the grant
)

// GrantFallback reports a grant state that is a code: the request waits for the key holder.
func GrantFallback(state string) bool { _, ok := GrantCodeText[state]; return ok }

// ErrGrantNotFound is a grant id this computer does not have.
var ErrGrantNotFound = errors.New("no such standing approval here")

const grantCols = `id, origin, requester_id, requester_name, label, host_id, template, profile, commit_sha, commit_basis, keys, roots, files,
	max_jobs, max_core_minutes, max_cpus, max_mem_gib, max_minutes, max_concurrent, note, created_at, expires_at, revoked_at,
	(SELECT COUNT(*) FROM relay_grant_uses u WHERE u.grant_id=relay_grants.id),
	(SELECT COALESCE(SUM(u.core_minutes), 0) FROM relay_grant_uses u WHERE u.grant_id=relay_grants.id),
	(SELECT COUNT(*) FROM relay_grant_uses u WHERE u.grant_id=relay_grants.id AND u.ended_at IS NULL)`

func scanGrant(row interface{ Scan(...any) error }) (Grant, error) {
	var g Grant
	var keys, roots, files string
	var coreMin int
	var created, expires int64
	var revoked sql.NullInt64
	err := row.Scan(&g.ID, &g.origin, &g.RequesterID, &g.RequesterName, &g.Label, &g.HostID, &g.Template, &g.Profile, &g.Commit, &g.CommitBasis,
		&keys, &roots, &files, &g.MaxJobs, &coreMin, &g.PerJob.MaxCPUs, &g.PerJob.MaxMemGiB, &g.PerJob.MaxTimeMinutes, &g.MaxAtOnce, &g.Note,
		&created, &expires, &revoked, &g.JobsUsed, &g.CoreMinutesUsed, &g.AtOnce)
	if err != nil {
		return g, err
	}
	g.MaxCoreHours = coreMin / 60
	_ = json.Unmarshal([]byte(keys), &g.Keys)
	_ = json.Unmarshal([]byte(roots), &g.Roots)
	_ = json.Unmarshal([]byte(files), &g.Files)
	for _, p := range []*[]string{&g.Roots, &g.Files} {
		if *p == nil {
			*p = []string{}
		}
	}
	if g.Keys == nil {
		g.Keys = []Bound{}
	}
	g.CreatedAt, g.ExpiresAt = time.Unix(created, 0).UTC(), time.Unix(expires, 0).UTC()
	g.RevokedAt = ptime(revoked)
	return g, nil
}

// Origin is the group server the grant was made for.
func (g Grant) Origin() string { return g.origin }

// CreateGrant stores a grant the caller has validated, with a fresh random id, and returns it.
func (l *Local) CreateGrant(origin string, g Grant) (Grant, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return Grant{}, err
	}
	g.ID = hex.EncodeToString(b[:])
	keys, _ := json.Marshal(g.Keys)
	roots, _ := json.Marshal(g.Roots)
	files, _ := json.Marshal(g.Files)
	_, err := l.db.Exec(`INSERT INTO relay_grants(id, origin, requester_id, requester_name, label, host_id, template, profile, commit_sha, commit_basis,
		keys, roots, files, max_jobs, max_core_minutes, max_cpus, max_mem_gib, max_minutes, max_concurrent, note, created_at, expires_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		g.ID, origin, g.RequesterID, g.RequesterName, g.Label, g.HostID, g.Template, g.Profile, g.Commit, g.CommitBasis, string(keys), string(roots),
		string(files), g.MaxJobs, g.MaxCoreHours*60, g.PerJob.MaxCPUs, g.PerJob.MaxMemGiB, g.PerJob.MaxTimeMinutes, g.MaxAtOnce, g.Note, l.now(), g.ExpiresAt.Unix())
	if err != nil {
		return Grant{}, err
	}
	_ = l.Unpublish(origin)
	return l.GrantByID(g.ID)
}

// GrantByID returns one grant with its usage.
func (l *Local) GrantByID(id string) (Grant, error) {
	g, err := scanGrant(l.db.QueryRow(`SELECT `+grantCols+` FROM relay_grants WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return g, ErrGrantNotFound
	}
	return g, err
}

// Grants lists the grants made for a group server, newest first.
func (l *Local) Grants(origin string) ([]Grant, error) {
	rows, err := l.db.Query(`SELECT `+grantCols+` FROM relay_grants WHERE origin=? ORDER BY created_at DESC, id LIMIT 200`, origin)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Grant{}
	for rows.Next() {
		g, err := scanGrant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// LiveGrants counts the grants of a server that are neither revoked nor expired.
func (l *Local) LiveGrants(origin string) int {
	var n int
	_ = l.db.QueryRow(`SELECT COUNT(*) FROM relay_grants WHERE origin=? AND revoked_at IS NULL AND expires_at>?`, origin, l.now()).Scan(&n)
	return n
}

// RevokeGrant revokes a grant at once. It reports false for a grant already revoked.
func (l *Local) RevokeGrant(id string) (bool, error) {
	res, err := l.db.Exec(`UPDATE relay_grants SET revoked_at=? WHERE id=? AND revoked_at IS NULL`, l.now(), id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		var origin string
		if l.db.QueryRow(`SELECT origin FROM relay_grants WHERE id=?`, id).Scan(&origin) == nil {
			_ = l.Unpublish(origin)
		}
	}
	return n > 0, nil
}

// Unpublish marks the inbox for publishing again at the next pass (a grant was made, used or revoked, so the offers changed).
func (l *Local) Unpublish(origin string) error {
	_, err := l.db.Exec(`UPDATE relay_local SET published=0 WHERE id=1 AND origin=?`, origin)
	return err
}

// Use is one request submitted under a grant.
type Use struct {
	ID          int64      `json:"id"`
	GrantID     string     `json:"grantId"`
	RelayedID   int64      `json:"relayedId"`
	JobID       int64      `json:"jobId"`
	CPUs        int        `json:"cpus"`
	Minutes     int        `json:"minutes"`
	CoreMinutes int        `json:"coreMinutes"`
	ScriptHash  string     `json:"scriptHash"`
	At          time.Time  `json:"at"`
	EndedAt     *time.Time `json:"endedAt,omitempty"`
}

// Reserve takes one slot of a grant's budget for one request, in one statement, so two requests at once can never both take the
// last slot: the row is written only while the grant is not revoked, has not expired, has used fewer jobs than it allows, has room
// for this job's core-minutes, and has fewer jobs not yet ended than its jobs-at-once limit. UNIQUE(relayed_id) makes a second
// reservation of the same request take nothing. It reports whether the slot was taken.
func (l *Local) Reserve(grantID string, relayedID, jobID int64, cpus, minutes int, scriptHash string) (bool, error) {
	now := l.now()
	core := cpus * minutes
	res, err := l.db.Exec(`INSERT INTO relay_grant_uses(grant_id, relayed_id, job_id, cpus, minutes, core_minutes, script_hash, at)
		SELECT g.id, ?, ?, ?, ?, ?, ?, ? FROM relay_grants g
		WHERE g.id=? AND g.revoked_at IS NULL AND g.expires_at>?
		AND (SELECT COUNT(*) FROM relay_grant_uses u WHERE u.grant_id=g.id) < g.max_jobs
		AND (SELECT COALESCE(SUM(u.core_minutes), 0) FROM relay_grant_uses u WHERE u.grant_id=g.id) + ? <= g.max_core_minutes
		AND (SELECT COUNT(*) FROM relay_grant_uses u WHERE u.grant_id=g.id AND u.ended_at IS NULL) < g.max_concurrent
		ON CONFLICT(relayed_id) DO NOTHING`,
		relayedID, jobID, cpus, minutes, core, scriptHash, now, grantID, now, core)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// Release gives a slot back: only for a request whose claim the group server refused or did not answer, so nothing was submitted.
func (l *Local) Release(relayedID int64) error {
	_, err := l.db.Exec(`DELETE FROM relay_grant_uses WHERE relayed_id=?`, relayedID)
	return err
}

// Uses lists the uses of a grant, newest first; open=true only those whose job has not ended.
func (l *Local) Uses(grantID string, open bool) ([]Use, error) {
	q := `SELECT id, grant_id, relayed_id, job_id, cpus, minutes, core_minutes, script_hash, at, ended_at FROM relay_grant_uses WHERE grant_id=?`
	if open {
		q += ` AND ended_at IS NULL`
	}
	rows, err := l.db.Query(q+` ORDER BY id DESC LIMIT 1000`, grantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Use{}
	for rows.Next() {
		var u Use
		var at int64
		var ended sql.NullInt64
		if err := rows.Scan(&u.ID, &u.GrantID, &u.RelayedID, &u.JobID, &u.CPUs, &u.Minutes, &u.CoreMinutes, &u.ScriptHash, &at, &ended); err != nil {
			return nil, err
		}
		u.At, u.EndedAt = time.Unix(at, 0).UTC(), ptime(ended)
		out = append(out, u)
	}
	return out, rows.Err()
}

// OpenUses lists every use, of any grant, whose job has not ended (what the pass settles).
func (l *Local) OpenUses() ([]Use, error) {
	rows, err := l.db.Query(`SELECT grant_id FROM relay_grant_uses WHERE ended_at IS NULL GROUP BY grant_id`)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	out := []Use{}
	for _, id := range ids {
		us, err := l.Uses(id, true)
		if err != nil {
			return nil, err
		}
		out = append(out, us...)
	}
	return out, nil
}

// EndUse records that a use's job ended (or is gone), which frees a jobs-at-once slot; the job and core-hours stay counted.
func (l *Local) EndUse(id int64) error {
	_, err := l.db.Exec(`UPDATE relay_grant_uses SET ended_at=? WHERE id=? AND ended_at IS NULL`, l.now(), id)
	return err
}

// SetGrantState records what this computer decided about the grant a request named, and why it waits for the key holder.
func (l *Local) SetGrantState(id int64, grantID, state, note string) error {
	_, err := l.db.Exec(`UPDATE relayed_requests SET grant_id=?, grant_state=?, grant_note=?, updated_at=? WHERE id=?`, grantID, state, note, l.now(), id)
	return err
}

// Undecided are the mirrored requests that named a grant and have not been decided: drafted (or received without a draft) with no
// grant state, or waiting for a slot, or waiting for the server to answer a claim.
func (l *Local) Undecided(origin string) ([]Mirrored, error) {
	ms, err := l.query(`SELECT `+mirrorCols+` FROM relayed_requests WHERE origin=? AND local_state IN (?,?) AND grant_state IN ('',?,?)
		AND COALESCE(json_extract(request, '$.ask.grant'), '') != '' ORDER BY id LIMIT 200`,
		origin, LocalReceived, LocalDraft, GrantStateWaiting, GrantStatePending)
	if err != nil {
		return nil, err
	}
	out := ms[:0]
	for _, m := range ms {
		if m.Request.Ask.Grant != "" {
			out = append(out, m)
		}
	}
	return out, nil
}
