package accounts

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// Workspaces, memberships, invitations and data roots (ADR 0050 slice 3). Roles are the strings of authz.Role; this package does
// not import authz (internal/arch), so it checks only that a role is one of the four.

// InvitationTTL is how long an invitation can be accepted (ADR 0050 section 2: 7 days, single use, revocable).
const InvitationTTL = 7 * 24 * time.Hour

var (
	ErrLastOwner         = errors.New("a workspace keeps at least one Owner: make someone else an Owner first")
	ErrBadRole           = errors.New("a role is viewer, researcher, maintainer or owner")
	ErrInvitationInvalid = errors.New("this invitation is not valid, was already used, was revoked, or has expired")
	ErrAlreadyMember     = errors.New("you are already a member of this workspace")
	ErrBadWorkspaceName  = errors.New("a workspace name is 1 to 100 characters")
)

func knownRole(r string) bool {
	switch r {
	case "viewer", "researcher", "maintainer", "owner":
		return true
	}
	return false
}

// Workspace is one access unit.
type Workspace struct {
	ID         workspace.ID `json:"id"`
	Name       string       `json:"name"`
	Created    time.Time    `json:"createdAt"`
	QuotaBytes int64        `json:"quotaBytes"` // managed copies and derived outputs it may keep (ADR 0050 section 8)
}

// Membership is an account's role in a workspace.
type Membership struct {
	Workspace     workspace.ID `json:"workspaceId"`
	WorkspaceName string       `json:"workspaceName"`
	AccountID     string       `json:"accountId"`
	Name          string       `json:"name"`
	Username      string       `json:"username"`
	Role          string       `json:"role"`
	Since         time.Time    `json:"since"`
}

// Invitation is a pending (or spent) invitation. The secret itself is shown once, at creation, and never stored.
type Invitation struct {
	ID        int64        `json:"id"`
	Workspace workspace.ID `json:"workspaceId"`
	Role      string       `json:"role"`
	Note      string       `json:"note"`
	CreatedBy string       `json:"createdBy"`
	Created   time.Time    `json:"createdAt"`
	Expires   time.Time    `json:"expiresAt"`
}

// CreateWorkspace creates a workspace and makes owner its first Owner, in one transaction.
func (s *Store) CreateWorkspace(name, owner, by string) (Workspace, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 100 {
		return Workspace{}, ErrBadWorkspaceName
	}
	now := s.now().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return Workspace{}, err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM accounts WHERE id=? AND disabled_at IS NULL`, owner).Scan(&exists); err != nil {
		return Workspace{}, err
	}
	if exists == 0 {
		return Workspace{}, ErrNotFound
	}
	res, err := tx.Exec(`INSERT INTO workspaces(name, created_at, created_by) VALUES(?,?,?)`, name, now, by)
	if err != nil {
		return Workspace{}, err
	}
	id, _ := res.LastInsertId()
	if _, err := tx.Exec(`INSERT INTO memberships(workspace_id,account_id,role,created_at) VALUES(?,?,'owner',?)`, id, owner, now); err != nil {
		return Workspace{}, err
	}
	if err := tx.Commit(); err != nil {
		return Workspace{}, err
	}
	return s.Workspace(workspace.ID(id))
}

// Workspace returns one workspace.
func (s *Store) Workspace(id workspace.ID) (Workspace, error) {
	var w Workspace
	var created int64
	err := s.rdb.QueryRow(`SELECT id, name, created_at, managed_quota_bytes FROM workspaces WHERE id=?`, id).Scan(&w.ID, &w.Name, &created, &w.QuotaBytes)
	if errors.Is(err, sql.ErrNoRows) {
		return Workspace{}, ErrNotFound
	}
	w.Created = time.Unix(created, 0).UTC()
	return w, err
}

// AllWorkspaces lists every workspace (the admin's view).
func (s *Store) AllWorkspaces() ([]Workspace, error) {
	rows, err := s.rdb.Query(`SELECT id, name, created_at, managed_quota_bytes FROM workspaces ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Workspace{}
	for rows.Next() {
		var w Workspace
		var created int64
		if err := rows.Scan(&w.ID, &w.Name, &created, &w.QuotaBytes); err != nil {
			return nil, err
		}
		w.Created = time.Unix(created, 0).UTC()
		out = append(out, w)
	}
	return out, rows.Err()
}

// SetQuota sets how many bytes of managed copies and derived outputs a workspace may keep.
func (s *Store) SetQuota(id workspace.ID, bytes int64) error {
	if bytes < 0 {
		return errors.New("a quota is zero or more bytes")
	}
	res, err := s.db.Exec(`UPDATE workspaces SET managed_quota_bytes=? WHERE id=?`, bytes, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

const membershipCols = `m.workspace_id, w.name, m.account_id, a.name, COALESCE((SELECT sub FROM identities i WHERE i.account_id=a.id AND i.issuer='local'),''), m.role, m.created_at`

func scanMemberships(rows *sql.Rows) ([]Membership, error) {
	defer rows.Close()
	out := []Membership{}
	for rows.Next() {
		var m Membership
		var since int64
		if err := rows.Scan(&m.Workspace, &m.WorkspaceName, &m.AccountID, &m.Name, &m.Username, &m.Role, &since); err != nil {
			return nil, err
		}
		m.Since = time.Unix(since, 0).UTC()
		out = append(out, m)
	}
	return out, rows.Err()
}

// Memberships lists the workspaces an account belongs to, oldest membership first (the first is the account's default).
func (s *Store) Memberships(accountID string) ([]Membership, error) {
	rows, err := s.rdb.Query(`SELECT `+membershipCols+` FROM memberships m JOIN workspaces w ON w.id=m.workspace_id JOIN accounts a ON a.id=m.account_id
		WHERE m.account_id=? ORDER BY m.created_at, m.workspace_id`, accountID)
	if err != nil {
		return nil, err
	}
	return scanMemberships(rows)
}

// Role returns the account's role in a workspace, or ErrNotFound when it is not a member. Read on every request, so a change
// takes effect at once.
func (s *Store) Role(accountID string, ws workspace.ID) (string, error) {
	var role string
	err := s.rdb.QueryRow(`SELECT role FROM memberships WHERE account_id=? AND workspace_id=?`, accountID, ws).Scan(&role)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return role, err
}

// Members lists a workspace's members, Owners first.
func (s *Store) Members(ws workspace.ID) ([]Membership, error) {
	rows, err := s.rdb.Query(`SELECT `+membershipCols+` FROM memberships m JOIN workspaces w ON w.id=m.workspace_id JOIN accounts a ON a.id=m.account_id
		WHERE m.workspace_id=? ORDER BY CASE m.role WHEN 'owner' THEN 0 WHEN 'maintainer' THEN 1 WHEN 'researcher' THEN 2 ELSE 3 END, a.name`, ws)
	if err != nil {
		return nil, err
	}
	return scanMemberships(rows)
}

// AddMember gives an existing account a role in a workspace (the admin's way in, and accepting an invitation as a signed-in
// account).
func (s *Store) AddMember(ws workspace.ID, accountID, role string) error {
	if !knownRole(role) {
		return ErrBadRole
	}
	res, err := s.db.Exec(`INSERT OR IGNORE INTO memberships(workspace_id,account_id,role,created_at) SELECT ?,?,?,? WHERE EXISTS(SELECT 1 FROM accounts WHERE id=?) AND EXISTS(SELECT 1 FROM workspaces WHERE id=?)`,
		ws, accountID, role, s.now().Unix(), accountID, ws)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var member int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM memberships WHERE workspace_id=? AND account_id=?`, ws, accountID).Scan(&member); err != nil {
			return err
		}
		if member > 0 {
			return ErrAlreadyMember
		}
		return ErrNotFound
	}
	return nil
}

// otherOwners counts the Owners of ws other than accountID, inside tx.
func otherOwners(tx *sql.Tx, ws workspace.ID, accountID string) (int, error) {
	var n int
	err := tx.QueryRow(`SELECT COUNT(*) FROM memberships m JOIN accounts a ON a.id=m.account_id
		WHERE m.workspace_id=? AND m.role='owner' AND m.account_id<>? AND a.disabled_at IS NULL`, ws, accountID).Scan(&n)
	return n, err
}

// SetRole changes a member's role. Demoting the last Owner is refused (ErrLastOwner), checked in the same transaction as the
// change. It returns the previous role.
func (s *Store) SetRole(ws workspace.ID, accountID, role string) (string, error) {
	if !knownRole(role) {
		return "", ErrBadRole
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var old string
	if err := tx.QueryRow(`SELECT role FROM memberships WHERE workspace_id=? AND account_id=?`, ws, accountID).Scan(&old); errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	} else if err != nil {
		return "", err
	}
	if old == "owner" && role != "owner" {
		if n, err := otherOwners(tx, ws, accountID); err != nil {
			return "", err
		} else if n == 0 {
			return "", ErrLastOwner
		}
	}
	if _, err := tx.Exec(`UPDATE memberships SET role=? WHERE workspace_id=? AND account_id=?`, role, ws, accountID); err != nil {
		return "", err
	}
	return old, tx.Commit()
}

// RemoveMember takes an account out of a workspace. Removing the last Owner is refused (ErrLastOwner). Records the account
// created stay, attributed to it (ADR 0050 section 2). It returns the role the member had.
func (s *Store) RemoveMember(ws workspace.ID, accountID string) (string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	var old string
	if err := tx.QueryRow(`SELECT role FROM memberships WHERE workspace_id=? AND account_id=?`, ws, accountID).Scan(&old); errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	} else if err != nil {
		return "", err
	}
	if old == "owner" {
		if n, err := otherOwners(tx, ws, accountID); err != nil {
			return "", err
		} else if n == 0 {
			return "", ErrLastOwner
		}
	}
	if _, err := tx.Exec(`DELETE FROM memberships WHERE workspace_id=? AND account_id=?`, ws, accountID); err != nil {
		return "", err
	}
	// the push and relay tokens bound to this workspace go with the membership (ADR 0066 section 3, account changes); the role
	// re-read on every request already stops them, this also takes them off the account's list
	if _, err := tx.Exec(`DELETE FROM access_tokens WHERE account_id=? AND workspace_id=?`, accountID, ws); err != nil {
		return "", err
	}
	return old, tx.Commit()
}

// CreateInvitation makes a single-use invitation into ws with a role, valid for InvitationTTL. The secret (256 bits) is
// returned once and stored only as its SHA-256.
func (s *Store) CreateInvitation(ws workspace.ID, role, note, by string) (string, Invitation, error) {
	if !knownRole(role) {
		return "", Invitation{}, ErrBadRole
	}
	note = strings.TrimSpace(note)
	if len([]rune(note)) > 200 {
		return "", Invitation{}, errors.New("a note is at most 200 characters")
	}
	secret := randomToken(32)
	now := s.now()
	exp := now.Add(InvitationTTL)
	res, err := s.db.Exec(`INSERT INTO invitations(workspace_id, role, note, token_hash, created_by, created_at, expires_at) VALUES(?,?,?,?,?,?,?)`,
		ws, role, note, Hash(secret), by, now.Unix(), exp.Unix())
	if err != nil {
		return "", Invitation{}, err
	}
	id, _ := res.LastInsertId()
	return secret, Invitation{ID: id, Workspace: ws, Role: role, Note: note, CreatedBy: by, Created: time.Unix(now.Unix(), 0).UTC(), Expires: time.Unix(exp.Unix(), 0).UTC()}, nil
}

// Invitations lists a workspace's invitations that can still be accepted.
func (s *Store) Invitations(ws workspace.ID) ([]Invitation, error) {
	rows, err := s.rdb.Query(`SELECT id, workspace_id, role, note, created_by, created_at, expires_at FROM invitations
		WHERE workspace_id=? AND used_at IS NULL AND revoked_at IS NULL AND expires_at>? ORDER BY id DESC`, ws, s.now().Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Invitation{}
	for rows.Next() {
		var in Invitation
		var created, exp int64
		if err := rows.Scan(&in.ID, &in.Workspace, &in.Role, &in.Note, &in.CreatedBy, &created, &exp); err != nil {
			return nil, err
		}
		in.Created, in.Expires = time.Unix(created, 0).UTC(), time.Unix(exp, 0).UTC()
		out = append(out, in)
	}
	return out, rows.Err()
}

// RevokeInvitation revokes an unused invitation of ws. Another workspace's id is ErrNotFound.
func (s *Store) RevokeInvitation(ws workspace.ID, id int64) (Invitation, error) {
	var in Invitation
	var created, exp int64
	err := s.db.QueryRow(`SELECT id, workspace_id, role, note, created_by, created_at, expires_at FROM invitations WHERE id=? AND workspace_id=? AND used_at IS NULL AND revoked_at IS NULL`, id, ws).
		Scan(&in.ID, &in.Workspace, &in.Role, &in.Note, &in.CreatedBy, &created, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return Invitation{}, ErrNotFound
	} else if err != nil {
		return Invitation{}, err
	}
	in.Created, in.Expires = time.Unix(created, 0).UTC(), time.Unix(exp, 0).UTC()
	_, err = s.db.Exec(`UPDATE invitations SET revoked_at=? WHERE id=? AND workspace_id=?`, s.now().Unix(), id, ws)
	return in, err
}

// PeekInvitation says what an invitation is for (the workspace's name and the role), without spending it. A used, revoked,
// expired or unknown secret is ErrInvitationInvalid, all alike.
func (s *Store) PeekInvitation(secret string) (Invitation, string, error) {
	var in Invitation
	var name string
	var created, exp int64
	err := s.rdb.QueryRow(`SELECT i.id, i.workspace_id, i.role, i.note, i.created_by, i.created_at, i.expires_at, w.name FROM invitations i JOIN workspaces w ON w.id=i.workspace_id
		WHERE i.token_hash=? AND i.used_at IS NULL AND i.revoked_at IS NULL AND i.expires_at>?`, Hash(secret), s.now().Unix()).
		Scan(&in.ID, &in.Workspace, &in.Role, &in.Note, &in.CreatedBy, &created, &exp, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return Invitation{}, "", ErrInvitationInvalid
	} else if err != nil {
		return Invitation{}, "", err
	}
	in.Created, in.Expires = time.Unix(created, 0).UTC(), time.Unix(exp, 0).UTC()
	return in, name, nil
}

// spendInvitation marks the invitation used by accountID inside tx, only if it is still unused, unrevoked and unexpired, so two
// acceptances race to one winner. It returns the invitation.
func (s *Store) spendInvitation(tx *sql.Tx, secret, accountID string, now time.Time) (Invitation, error) {
	var in Invitation
	err := tx.QueryRow(`SELECT id, workspace_id, role FROM invitations WHERE token_hash=? AND used_at IS NULL AND revoked_at IS NULL AND expires_at>?`, Hash(secret), now.Unix()).
		Scan(&in.ID, &in.Workspace, &in.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return Invitation{}, ErrInvitationInvalid
	} else if err != nil {
		return Invitation{}, err
	}
	res, err := tx.Exec(`UPDATE invitations SET used_at=?, used_by=? WHERE token_hash=? AND used_at IS NULL AND revoked_at IS NULL AND expires_at>?`, now.Unix(), accountID, Hash(secret), now.Unix())
	if err != nil {
		return Invitation{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return Invitation{}, ErrInvitationInvalid
	}
	return in, nil
}

// AcceptInvitation spends an invitation and creates a new (non-admin) account with a password, member of the invitation's
// workspace with its role, in one transaction. The account still has to enrol its second factor before it is signed in.
func (s *Store) AcceptInvitation(secret, name, username, password string) (Account, Invitation, error) {
	if _, _, err := s.PeekInvitation(secret); err != nil { // fail before the slow hash when the link is already dead
		return Account{}, Invitation{}, err
	}
	name, username, hash, err := s.newCredentials(name, username, password)
	if err != nil {
		return Account{}, Invitation{}, err
	}
	now := s.now()
	tx, err := s.db.Begin()
	if err != nil {
		return Account{}, Invitation{}, err
	}
	defer tx.Rollback()
	a, err := createAccount(tx, name, username, hash, false, now)
	if err != nil {
		return Account{}, Invitation{}, err
	}
	in, err := s.spendInvitation(tx, secret, a.ID, now)
	if err != nil {
		return Account{}, Invitation{}, err
	}
	if _, err := tx.Exec(`INSERT INTO memberships(workspace_id,account_id,role,created_at) VALUES(?,?,?,?)`, in.Workspace, a.ID, in.Role, now.Unix()); err != nil {
		return Account{}, Invitation{}, err
	}
	return a, in, tx.Commit()
}

// AcceptInvitationAs spends an invitation for an existing, signed-in account, which joins the workspace with its role. An
// account that is already a member is refused without spending the invitation.
func (s *Store) AcceptInvitationAs(secret, accountID string) (Invitation, error) {
	now := s.now()
	tx, err := s.db.Begin()
	if err != nil {
		return Invitation{}, err
	}
	defer tx.Rollback()
	in, err := s.spendInvitation(tx, secret, accountID, now)
	if err != nil {
		return Invitation{}, err
	}
	res, err := tx.Exec(`INSERT OR IGNORE INTO memberships(workspace_id,account_id,role,created_at) VALUES(?,?,?,?)`, in.Workspace, accountID, in.Role, now.Unix())
	if err != nil {
		return Invitation{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Invitation{}, ErrAlreadyMember
	}
	return in, tx.Commit()
}

// Roots lists the folders an admin declared readable for a workspace (canonical paths).
func (s *Store) Roots(ws workspace.ID) ([]string, error) {
	rows, err := s.rdb.Query(`SELECT path FROM workspace_roots WHERE workspace_id=? ORDER BY path`, ws)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// AddRoot declares a canonical folder readable for a workspace. The caller has checked the folder (sourceroot and the ADR 0030
// rules); this only records it.
func (s *Store) AddRoot(ws workspace.ID, path, by string) error {
	_, err := s.db.Exec(`INSERT OR IGNORE INTO workspace_roots(workspace_id, path, added_at, added_by) SELECT ?,?,?,? WHERE EXISTS(SELECT 1 FROM workspaces WHERE id=?)`, ws, path, s.now().Unix(), by, ws)
	return err
}

// RemoveRoot withdraws a folder from a workspace. Datasets registered from it stay catalogued; reading them is refused until a
// root holds them again.
func (s *Store) RemoveRoot(ws workspace.ID, path string) error {
	res, err := s.db.Exec(`DELETE FROM workspace_roots WHERE workspace_id=? AND path=?`, ws, path)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
