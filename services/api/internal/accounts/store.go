// Package accounts keeps the group server's accounts and what proves who they are (ADR 0050 section 2, slice 2): password
// sign-in with argon2id, a TOTP second factor with single-use recovery codes, server-side sessions, personal access tokens and
// the one-time setup link of the first admin. It knows nothing of HTTP or of internal/authz; the server turns what it answers
// into an actor.
//
// Every secret it hands out (session id, token, recovery code, setup link) is 256 random bits or, for recovery codes, 50, and
// only its SHA-256 is stored; passwords are stored as argon2id hashes; the TOTP secret is sealed under the server key.
package accounts

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/schema"
)

// LocalIssuer is the issuer of a password identity; its sub is the username. OIDC identities use the provider's issuer URL.
const LocalIssuer = "local"

// Timeouts (ADR 0050 section 2). A session that has not finished the second factor lasts PendingTTL from the password.
const (
	IdleTimeout     = 12 * time.Hour
	AbsoluteTimeout = 7 * 24 * time.Hour
	RecentWindow    = 15 * time.Minute
	PendingTTL      = 10 * time.Minute
	SetupTTL        = 30 * time.Minute
	TokenDefaultTTL = 30 * 24 * time.Hour
	TokenMaxTTL     = 365 * 24 * time.Hour
	touchEvery      = time.Minute // last use is written at most this often per session or token
	recoveryCodes   = 10
)

// Session stages: the password was right and the second factor is owed (or must be enrolled first), or signed in fully.
const (
	StageSecondFactor = "second_factor"
	StageEnrol        = "enrol"
	StageFull         = "full"
)

var (
	ErrNotFound       = errors.New("not found")
	ErrSetupInvalid   = errors.New("this setup link is not valid, was already used, or has expired")
	ErrUsernameTaken  = errors.New("that username is taken")
	ErrBadUsername    = errors.New("a username is 1 to 64 characters: letters, digits, dot, underscore or hyphen")
	ErrBadName        = errors.New("a name is 1 to 100 characters")
	ErrNoEnrolment    = errors.New("no second-factor enrolment is waiting")
	ErrAlreadyEnroled = errors.New("the second factor is already enrolled")
	ErrBadScopes      = errors.New("choose at least one known scope")
	// ErrBusy: every argon2 slot is taken and the short queue behind them is full; the caller answers 503.
	ErrBusy = errors.New("password hashing is busy")
)

type Account struct {
	ID       string    `json:"id"`
	Name     string    `json:"name"`
	Username string    `json:"username"`
	Admin    bool      `json:"admin"`
	Created  time.Time `json:"createdAt"`
	Disabled bool      `json:"disabled"`
	// SecondFactor is true once a TOTP enrolment is confirmed.
	SecondFactor bool `json:"secondFactor"`
}

type Session struct {
	Hash       string
	AccountID  string
	Stage      string
	Created    time.Time
	LastSeen   time.Time
	SignedInAt time.Time // zero until the second factor
	ClientAddr string
	UserAgent  string
}

// Recent reports whether the session signed in fully within RecentWindow of now.
func (s Session) Recent(now time.Time) bool {
	return s.Stage == StageFull && !s.SignedInAt.IsZero() && now.Sub(s.SignedInAt) < RecentWindow
}

type Token struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Scopes    []string   `json:"scopes"`
	Created   time.Time  `json:"createdAt"`
	Expires   time.Time  `json:"expiresAt"`
	LastUsed  *time.Time `json:"lastUsedAt"`
	Revoked   *time.Time `json:"revokedAt"`
	AccountID string     `json:"-"`
}

type Store struct {
	db  *sql.DB // the writer (catalog.Open's single connection)
	rdb *sql.DB // read-only pool for lookups on every request; db when none is set
	key []byte  // server key that seals TOTP secrets

	// Params is the argon2id cost for new hashes; tests lower it. Verification always uses what the hash itself states.
	Params Argon2Params
	Now    func() time.Time
	// KnownScopes says which token scopes exist (authz.Scopes, handed in by the server, since this package does not import authz).
	KnownScopes []string

	hashSlots chan struct{} // at most this many argon2 computations at once (64 MiB each)
	hashQueue atomic.Int32  // computations running or waiting for a slot
	// MaxHashQueue bounds hashQueue (default 16): beyond it a request is refused with ErrBusy rather than queued.
	MaxHashQueue int32
	dummyOnce    sync.Once
	dummy        string
	verifies     atomic.Int64 // password verifications run, real or dummy (tests check an unknown name is verified too)
	totps        atomic.Int64 // TOTP codes checked against an enrolled secret
}

// Open creates the tables when they are missing (a fresh catalog; schema 30 adds them to an upgraded one).
func Open(db *sql.DB) (*Store, error) {
	if _, err := db.Exec(schema.AccountsTables); err != nil {
		return nil, err
	}
	// memberships reference accounts and workspaces; the catalog creates them too, this keeps the store whole on its own
	if _, err := db.Exec(schema.WorkspaceTables); err != nil {
		return nil, err
	}
	if err := schema.SeedLocalWorkspace(db); err != nil {
		return nil, err
	}
	return &Store{db: db, rdb: db, Params: DefaultArgon2, Now: time.Now, hashSlots: make(chan struct{}, 4)}, nil
}

// UseReadPool sends the per-request lookups (sessions, tokens) to a read-only pool, so they never queue behind a write.
func (s *Store) UseReadPool(rdb *sql.DB) { s.rdb = rdb }

// SetKey sets the server key (LoadOrCreateKey).
func (s *Store) SetKey(k []byte) { s.key = k }

// Verifications is the number of password verifications run so far.
func (s *Store) Verifications() int64 { return s.verifies.Load() }

// TOTPChecks is the number of codes checked against an enrolled secret so far.
func (s *Store) TOTPChecks() int64 { return s.totps.Load() }

func (s *Store) now() time.Time { return s.Now() }

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand does not fail on supported systems; a predictable secret is worse than a crash
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// Hash is the stored form of a high-entropy secret: hex SHA-256.
func Hash(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

var usernameShape = regexp.MustCompile(`^[a-z0-9._-]{1,64}$`)

// NormalizeUsername lowercases and trims; the result is the identity's sub.
func NormalizeUsername(u string) string { return strings.ToLower(strings.TrimSpace(u)) }

// ---- setup link (first admin) ----

// CreateSetupToken mints a single-use setup link secret, valid for SetupTTL. Only its hash is kept.
func (s *Store) CreateSetupToken() (string, time.Time, error) {
	t, now := randomToken(32), s.now()
	exp := now.Add(SetupTTL)
	_, err := s.db.Exec(`INSERT INTO setup_tokens(token_hash,created_at,expires_at) VALUES(?,?,?)`, Hash(t), now.Unix(), exp.Unix())
	return t, exp, err
}

// HasAdmin reports whether an enabled admin account exists.
func (s *Store) HasAdmin() (bool, error) {
	var n int
	err := s.rdb.QueryRow(`SELECT COUNT(*) FROM accounts WHERE admin=1 AND disabled_at IS NULL`).Scan(&n)
	return n > 0, err
}

// CreateAdminFromSetup spends a setup secret and creates an admin account with a password, in one transaction: the secret is
// marked used only if it was unused and unexpired, so two uses race to one winner. The first admin is also the Owner of
// workspace 1, the server's main workspace (and, on an adopted local catalog, the one holding its work): admin alone is not a
// workspace role (ADR 0050 section 3), and without a membership the first person could see nothing.
func (s *Store) CreateAdminFromSetup(secret, name, username, password string) (Account, error) {
	name, username, hash, err := s.newCredentials(name, username, password)
	if err != nil {
		return Account{}, err
	}
	now := s.now()
	tx, err := s.db.Begin()
	if err != nil {
		return Account{}, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE setup_tokens SET used_at=? WHERE token_hash=? AND used_at IS NULL AND expires_at>?`, now.Unix(), Hash(secret), now.Unix())
	if err != nil {
		return Account{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return Account{}, ErrSetupInvalid
	}
	a, err := createAccount(tx, name, username, hash, true, now)
	if err != nil {
		return Account{}, err
	}
	if _, err := tx.Exec(`INSERT OR IGNORE INTO memberships(workspace_id,account_id,role,created_at) VALUES(1,?,'owner',?)`, a.ID, now.Unix()); err != nil {
		return Account{}, err
	}
	return a, tx.Commit()
}

// newCredentials checks a new account's name, username and password and hashes the password (outside any transaction: argon2
// is slow and the writer connection is shared).
func (s *Store) newCredentials(name, username, password string) (string, string, string, error) {
	name, username = strings.TrimSpace(name), NormalizeUsername(username)
	if name == "" || len([]rune(name)) > 100 {
		return "", "", "", ErrBadName
	}
	if !usernameShape.MatchString(username) {
		return "", "", "", ErrBadUsername
	}
	if err := CheckNewPassword(password, username); err != nil {
		return "", "", "", err
	}
	hash, err := s.hash(password)
	return name, username, hash, err
}

// createAccount writes an account, its local identity and its password hash inside tx.
func createAccount(tx *sql.Tx, name, username, hash string, admin bool, now time.Time) (Account, error) {
	var taken int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM identities WHERE issuer=? AND sub=?`, LocalIssuer, username).Scan(&taken); err != nil {
		return Account{}, err
	}
	if taken > 0 {
		return Account{}, ErrUsernameTaken
	}
	a := Account{ID: "acct-" + randomHex(8), Name: name, Username: username, Admin: admin, Created: time.Unix(now.Unix(), 0).UTC()}
	flag := 0
	if admin {
		flag = 1
	}
	for _, q := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO accounts(id,name,admin,created_at) VALUES(?,?,?,?)`, []any{a.ID, a.Name, flag, now.Unix()}},
		{`INSERT INTO identities(issuer,sub,account_id,created_at) VALUES(?,?,?,?)`, []any{LocalIssuer, username, a.ID, now.Unix()}},
		{`INSERT INTO password_credentials(account_id,hash,changed_at) VALUES(?,?,?)`, []any{a.ID, hash, now.Unix()}},
	} {
		if _, err := tx.Exec(q.sql, q.args...); err != nil {
			return Account{}, err
		}
	}
	return a, nil
}

// ---- passwords ----

// slot takes an argon2 slot, waiting only while the queue is short; release gives it back.
func (s *Store) slot() (release func(), err error) {
	max := s.MaxHashQueue
	if max <= 0 {
		max = 16
	}
	if s.hashQueue.Add(1) > max {
		s.hashQueue.Add(-1)
		return nil, ErrBusy
	}
	s.hashSlots <- struct{}{}
	return func() { <-s.hashSlots; s.hashQueue.Add(-1) }, nil
}

func (s *Store) hash(pw string) (string, error) {
	release, err := s.slot()
	if err != nil {
		return "", err
	}
	defer release()
	return hashPassword(pw, s.Params)
}

func (s *Store) verify(pw, stored string) (bool, error) {
	release, err := s.slot()
	if err != nil {
		return false, err
	}
	defer release()
	s.verifies.Add(1)
	return verifyPassword(pw, stored), nil
}

// dummyHash is a hash of a random password with the current settings, verified for a name no account has, so an unknown
// name costs the same argon2 work as a wrong password.
func (s *Store) dummyHash() string {
	s.dummyOnce.Do(func() {
		h, err := hashPassword(randomToken(18), s.Params)
		if err != nil {
			panic(err)
		}
		s.dummy = h
	})
	return s.dummy
}

// CheckPassword returns the account whose username and password these are. An unknown name, a disabled account and a wrong
// password all answer ok=false after the same argon2 verification, so neither the answer nor its timing says which.
func (s *Store) CheckPassword(username, password string) (Account, bool, error) {
	if len(password) > MaxPasswordBytes {
		password = password[:MaxPasswordBytes] // still verified, never matches a stored hash of a longer one
	}
	var a Account
	var stored string
	var disabled sql.NullInt64
	err := s.db.QueryRow(`SELECT a.id, a.name, i.sub, a.admin, a.created_at, a.disabled_at, p.hash
		FROM identities i JOIN accounts a ON a.id=i.account_id JOIN password_credentials p ON p.account_id=a.id
		WHERE i.issuer=? AND i.sub=?`, LocalIssuer, NormalizeUsername(username)).Scan(&a.ID, &a.Name, &a.Username, &a.Admin, new(int64), &disabled, &stored)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := s.verify(password, s.dummyHash()); err != nil {
			return Account{}, false, err
		}
		return Account{}, false, nil
	}
	if err != nil {
		return Account{}, false, err
	}
	ok, err := s.verify(password, stored)
	if err != nil {
		return Account{}, false, err
	}
	if !ok || disabled.Valid {
		return Account{}, false, nil
	}
	full, err := s.Get(a.ID)
	return full, err == nil, err
}

// SetPassword replaces an account's password after checking it.
func (s *Store) SetPassword(accountID, username, password string) error {
	if err := CheckNewPassword(password, username); err != nil {
		return err
	}
	h, err := s.hash(password)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`UPDATE password_credentials SET hash=?, changed_at=? WHERE account_id=?`, h, s.now().Unix(), accountID)
	return err
}

// ---- accounts ----

const accountCols = `a.id, a.name, COALESCE((SELECT sub FROM identities WHERE account_id=a.id AND issuer='local'),''), a.admin,
	a.created_at, a.disabled_at IS NOT NULL, EXISTS(SELECT 1 FROM totp_credentials t WHERE t.account_id=a.id AND t.secret IS NOT NULL)`

func scanAccount(sc interface{ Scan(...any) error }) (Account, error) {
	var a Account
	var created int64
	err := sc.Scan(&a.ID, &a.Name, &a.Username, &a.Admin, &created, &a.Disabled, &a.SecondFactor)
	a.Created = time.Unix(created, 0).UTC()
	return a, err
}

// Get returns one account.
func (s *Store) Get(id string) (Account, error) {
	a, err := scanAccount(s.rdb.QueryRow(`SELECT `+accountCols+` FROM accounts a WHERE a.id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	return a, err
}

// ByUsername returns the account with this local username.
func (s *Store) ByUsername(username string) (Account, error) {
	var id string
	err := s.db.QueryRow(`SELECT account_id FROM identities WHERE issuer=? AND sub=?`, LocalIssuer, NormalizeUsername(username)).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return Account{}, ErrNotFound
	}
	if err != nil {
		return Account{}, err
	}
	return s.Get(id)
}

// List returns every account, oldest first.
func (s *Store) List() ([]Account, error) {
	rows, err := s.db.Query(`SELECT ` + accountCols + ` FROM accounts a ORDER BY a.created_at, a.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Account{}
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ---- second factor ----

// StartEnrolment creates a new TOTP secret as a pending enrolment (replacing an earlier pending one) and returns it. An
// enrolled secret stays in force until a code from the new one confirms it.
func (s *Store) StartEnrolment(accountID string) ([]byte, error) {
	secret := make([]byte, totpSecretN)
	if _, err := rand.Read(secret); err != nil {
		return nil, err
	}
	sealed, err := seal(s.key, secret, accountID)
	if err != nil {
		return nil, err
	}
	_, err = s.db.Exec(`INSERT INTO totp_credentials(account_id,pending) VALUES(?,?)
		ON CONFLICT(account_id) DO UPDATE SET pending=excluded.pending`, accountID, sealed)
	return secret, err
}

// ConfirmEnrolment checks a code against the pending secret (replacing an enrolled one: a new phone); on a match the pending secret becomes the enrolled one, the
// step is spent, and a fresh set of recovery codes replaces any earlier set. It returns those codes, once.
func (s *Store) ConfirmEnrolment(accountID, code string) ([]string, bool, error) {
	return s.confirmEnrolment(accountID, code, false)
}

// ConfirmFirstEnrolment is ConfirmEnrolment for an account with no second factor yet: it succeeds only while no secret is
// enrolled, so a session that signed in with the password alone can never replace one, even racing another sign-in.
func (s *Store) ConfirmFirstEnrolment(accountID, code string) ([]string, bool, error) {
	return s.confirmEnrolment(accountID, code, true)
}

func (s *Store) confirmEnrolment(accountID, code string, first bool) ([]string, bool, error) {
	var pending []byte
	err := s.db.QueryRow(`SELECT pending FROM totp_credentials WHERE account_id=?`, accountID).Scan(&pending)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && pending == nil) {
		return nil, false, ErrNoEnrolment
	}
	if err != nil {
		return nil, false, err
	}
	secret, err := open(s.key, pending, accountID)
	if err != nil {
		return nil, false, err
	}
	step := matchStep(secret, code, s.now())
	if step == 0 {
		return nil, false, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	q := `UPDATE totp_credentials SET secret=pending, pending=NULL, last_step=?, confirmed_at=? WHERE account_id=? AND pending=?`
	if first {
		q += ` AND secret IS NULL`
	}
	res, err := tx.Exec(q, step, s.now().Unix(), accountID, pending)
	if err != nil {
		return nil, false, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, false, nil // a concurrent confirmation or a new enrolment won
	}
	codes, err := replaceRecoveryCodes(tx, accountID)
	if err != nil {
		return nil, false, err
	}
	return codes, true, tx.Commit()
}

// VerifyTOTP checks a code against the enrolled secret and spends its step: the update succeeds only if the step is later
// than the last one accepted, so a code (or any earlier one) works once, even when two requests race.
func (s *Store) VerifyTOTP(accountID, code string) (bool, error) {
	var sealed []byte
	var last int64
	err := s.db.QueryRow(`SELECT secret, last_step FROM totp_credentials WHERE account_id=? AND secret IS NOT NULL`, accountID).Scan(&sealed, &last)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	secret, err := open(s.key, sealed, accountID)
	if err != nil {
		return false, err
	}
	s.totps.Add(1)
	step := matchStep(secret, code, s.now())
	if step == 0 || step <= last {
		return false, nil
	}
	res, err := s.db.Exec(`UPDATE totp_credentials SET last_step=? WHERE account_id=? AND last_step<?`, step, accountID, step)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

func normalizeRecovery(c string) string {
	return strings.ToLower(strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(c)))
}

// UseRecoveryCode spends one recovery code; each works once.
func (s *Store) UseRecoveryCode(accountID, code string) (bool, error) {
	res, err := s.db.Exec(`UPDATE recovery_codes SET used_at=? WHERE account_id=? AND code_hash=? AND used_at IS NULL`, s.now().Unix(), accountID, Hash(normalizeRecovery(code)))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// RecoveryCodesLeft counts the unused recovery codes.
func (s *Store) RecoveryCodesLeft(accountID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM recovery_codes WHERE account_id=? AND used_at IS NULL`, accountID).Scan(&n)
	return n, err
}

// NewRecoveryCodes replaces the account's recovery codes with a fresh set, returned once.
func (s *Store) NewRecoveryCodes(accountID string) ([]string, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	codes, err := replaceRecoveryCodes(tx, accountID)
	if err != nil {
		return nil, err
	}
	return codes, tx.Commit()
}

// replaceRecoveryCodes makes ten codes of ten base32 characters (50 bits each), shown as xxxxx-xxxxx; only hashes are stored.
func replaceRecoveryCodes(tx *sql.Tx, accountID string) ([]string, error) {
	if _, err := tx.Exec(`DELETE FROM recovery_codes WHERE account_id=?`, accountID); err != nil {
		return nil, err
	}
	out := make([]string, recoveryCodes)
	for i := range out {
		b := make([]byte, 7)
		if _, err := rand.Read(b); err != nil {
			return nil, err
		}
		c := strings.ToLower(b32.EncodeToString(b))[:10]
		out[i] = c[:5] + "-" + c[5:]
		if _, err := tx.Exec(`INSERT INTO recovery_codes(account_id,code_hash) VALUES(?,?)`, accountID, Hash(c)); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ResetSecondFactor is the admin reset path: it removes the account's TOTP secret and recovery codes, ends its sessions and
// revokes its personal access tokens (whoever lost the phone may have lost the laptop holding them too), in one transaction,
// and says how many sessions and tokens there were. The next sign-in enrols again.
func (s *Store) ResetSecondFactor(accountID string) (sessions, tokens int64, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	for _, q := range []string{`DELETE FROM totp_credentials WHERE account_id=?`, `DELETE FROM recovery_codes WHERE account_id=?`} {
		if _, err := tx.Exec(q, accountID); err != nil {
			return 0, 0, err
		}
	}
	res, err := tx.Exec(`DELETE FROM sessions WHERE account_id=?`, accountID)
	if err != nil {
		return 0, 0, err
	}
	sessions, _ = res.RowsAffected()
	res, err = tx.Exec(`UPDATE access_tokens SET revoked_at=? WHERE account_id=? AND revoked_at IS NULL`, s.now().Unix(), accountID)
	if err != nil {
		return 0, 0, err
	}
	tokens, _ = res.RowsAffected()
	return sessions, tokens, tx.Commit()
}

// LiveTokens counts the account's tokens that still work.
func (s *Store) LiveTokens(accountID string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM access_tokens WHERE account_id=? AND revoked_at IS NULL AND expires_at>?`, accountID, s.now().Unix()).Scan(&n)
	return n, err
}

// ---- sessions ----

// CreateSession starts a session at the given stage and returns its id, which only the cookie holds. Every sign-in creates a
// new one; an id a browser already had is never adopted.
func (s *Store) CreateSession(accountID, stage, addr, ua string) (string, error) {
	id, now := randomToken(32), s.now().Unix()
	signed := int64(0)
	if stage == StageFull {
		signed = now
	}
	if len(ua) > 200 {
		ua = ua[:200]
	}
	_, err := s.db.Exec(`INSERT INTO sessions(id_hash,account_id,stage,created_at,last_seen_at,signed_in_at,client_addr,user_agent) VALUES(?,?,?,?,?,?,?,?)`,
		Hash(id), accountID, stage, now, now, signed, addr, ua)
	return id, err
}

// Session looks up a session by the id the cookie carries and applies the timeouts: idle IdleTimeout, absolute
// AbsoluteTimeout from the full sign-in, and PendingTTL for one still owing its second factor. An expired session is deleted.
func (s *Store) Session(id string) (Session, error) {
	var se Session
	var created, seen, signed int64
	err := s.rdb.QueryRow(`SELECT s.id_hash, s.account_id, s.stage, s.created_at, s.last_seen_at, s.signed_in_at, s.client_addr, s.user_agent
		FROM sessions s JOIN accounts a ON a.id=s.account_id WHERE s.id_hash=? AND a.disabled_at IS NULL`, Hash(id)).
		Scan(&se.Hash, &se.AccountID, &se.Stage, &created, &seen, &signed, &se.ClientAddr, &se.UserAgent)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, err
	}
	se.Created, se.LastSeen = time.Unix(created, 0), time.Unix(seen, 0)
	if signed > 0 {
		se.SignedInAt = time.Unix(signed, 0)
	}
	now := s.now()
	expired := now.Sub(se.LastSeen) >= IdleTimeout || now.Sub(se.Created) >= AbsoluteTimeout
	if se.Stage != StageFull {
		expired = now.Sub(se.Created) >= PendingTTL
	}
	if expired {
		_, _ = s.db.Exec(`DELETE FROM sessions WHERE id_hash=?`, se.Hash)
		return Session{}, ErrNotFound
	}
	if now.Sub(se.LastSeen) >= touchEvery {
		_, _ = s.db.Exec(`UPDATE sessions SET last_seen_at=? WHERE id_hash=?`, now.Unix(), se.Hash)
	}
	return se, nil
}

// Promote ends a session and starts a fully signed-in one for the same account, returning the new id: the id changes when the
// second factor is proven (or proven again for a recent sign-in), so an id seen before that point is worth nothing after it.
// keepCreated keeps the original start for the absolute timeout (a re-confirmation does not extend a session's life).
func (s *Store) Promote(old Session, addr, ua string, keepCreated bool) (string, error) {
	id, now := randomToken(32), s.now().Unix()
	created := now
	if keepCreated && old.Stage == StageFull {
		created = old.Created.Unix()
	}
	if len(ua) > 200 {
		ua = ua[:200]
	}
	tx, err := s.db.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`DELETE FROM sessions WHERE id_hash=?`, old.Hash)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return "", ErrNotFound // ended meanwhile (signed out, or promoted by a racing request)
	}
	if _, err := tx.Exec(`INSERT INTO sessions(id_hash,account_id,stage,created_at,last_seen_at,signed_in_at,client_addr,user_agent) VALUES(?,?,?,?,?,?,?,?)`,
		Hash(id), old.AccountID, StageFull, created, now, now, addr, ua); err != nil {
		return "", err
	}
	return id, tx.Commit()
}

// SetStage moves a pending session to another pending stage (the second factor was reset meanwhile).
func (s *Store) SetStage(se Session, stage string) error {
	_, err := s.db.Exec(`UPDATE sessions SET stage=? WHERE id_hash=? AND stage<>'full'`, stage, se.Hash)
	return err
}

// EndSession deletes one session.
func (s *Store) EndSession(hash string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE id_hash=?`, hash)
	return err
}

// EndSessions deletes every session of an account ("sign out everywhere") and says how many there were.
func (s *Store) EndSessions(accountID string) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM sessions WHERE account_id=?`, accountID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ---- personal access tokens ----

// TokenPrefix marks a Vesqira token, so a leaked one is recognisable to secret scanners and to a person.
const TokenPrefix = "vsq_"

// CreateToken mints a token for the account: 256 random bits, returned once, stored as its hash, expiring after ttl (0 means
// TokenDefaultTTL; longer than TokenMaxTTL is refused).
func (s *Store) CreateToken(accountID, name string, scopes []string, ttl time.Duration) (string, Token, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 100 {
		return "", Token{}, ErrBadName
	}
	if ttl == 0 {
		ttl = TokenDefaultTTL
	}
	if ttl < time.Hour || ttl > TokenMaxTTL {
		return "", Token{}, fmt.Errorf("a token lasts from one hour to %d days", int(TokenMaxTTL.Hours()/24))
	}
	clean, err := s.checkScopes(scopes)
	if err != nil {
		return "", Token{}, err
	}
	raw := TokenPrefix + randomToken(32)
	now := s.now()
	t := Token{ID: "tok-" + randomHex(8), Name: name, Scopes: clean, Created: time.Unix(now.Unix(), 0).UTC(), Expires: time.Unix(now.Add(ttl).Unix(), 0).UTC(), AccountID: accountID}
	_, err = s.db.Exec(`INSERT INTO access_tokens(id,account_id,name,token_hash,scopes,created_at,expires_at) VALUES(?,?,?,?,?,?,?)`,
		t.ID, accountID, t.Name, Hash(raw), strings.Join(clean, " "), now.Unix(), t.Expires.Unix())
	return raw, t, err
}

func (s *Store) checkScopes(scopes []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, sc := range scopes {
		ok := false
		for _, k := range s.KnownScopes {
			ok = ok || sc == k
		}
		if !ok {
			return nil, ErrBadScopes
		}
		if !seen[sc] {
			seen[sc] = true
			out = append(out, sc)
		}
	}
	if len(out) == 0 {
		return nil, ErrBadScopes
	}
	return out, nil
}

func scanToken(sc interface{ Scan(...any) error }) (Token, error) {
	var t Token
	var scopes string
	var created, expires int64
	var used, revoked sql.NullInt64
	if err := sc.Scan(&t.ID, &t.AccountID, &t.Name, &scopes, &created, &expires, &used, &revoked); err != nil {
		return Token{}, err
	}
	t.Scopes = strings.Fields(scopes)
	t.Created, t.Expires = time.Unix(created, 0).UTC(), time.Unix(expires, 0).UTC()
	if used.Valid {
		u := time.Unix(used.Int64, 0).UTC()
		t.LastUsed = &u
	}
	if revoked.Valid {
		r := time.Unix(revoked.Int64, 0).UTC()
		t.Revoked = &r
	}
	return t, nil
}

const tokenCols = `id, account_id, name, scopes, created_at, expires_at, last_used_at, revoked_at`

// Tokens lists an account's tokens, newest first (never their values, which are not kept).
func (s *Store) Tokens(accountID string) ([]Token, error) {
	rows, err := s.db.Query(`SELECT `+tokenCols+` FROM access_tokens WHERE account_id=? ORDER BY created_at DESC, id`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Token{}
	for rows.Next() {
		t, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// RevokeToken revokes one of the account's tokens; it stops working at once. A token of another account is ErrNotFound.
func (s *Store) RevokeToken(accountID, id string) (Token, error) {
	res, err := s.db.Exec(`UPDATE access_tokens SET revoked_at=? WHERE id=? AND account_id=? AND revoked_at IS NULL`, s.now().Unix(), id, accountID)
	if err != nil {
		return Token{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return Token{}, ErrNotFound
	}
	return scanToken(s.db.QueryRow(`SELECT `+tokenCols+` FROM access_tokens WHERE id=?`, id))
}

// LookupToken returns the live token a bearer value names: not revoked, not expired, its account enabled.
func (s *Store) LookupToken(raw string) (Token, error) {
	if !strings.HasPrefix(raw, TokenPrefix) || len(raw) > 100 {
		return Token{}, ErrNotFound
	}
	now := s.now()
	t, err := scanToken(s.rdb.QueryRow(`SELECT t.id, t.account_id, t.name, t.scopes, t.created_at, t.expires_at, t.last_used_at, t.revoked_at
		FROM access_tokens t JOIN accounts a ON a.id=t.account_id
		WHERE t.token_hash=? AND t.revoked_at IS NULL AND t.expires_at>? AND a.disabled_at IS NULL`, Hash(raw), now.Unix()))
	if errors.Is(err, sql.ErrNoRows) {
		return Token{}, ErrNotFound
	}
	if err != nil {
		return Token{}, err
	}
	if t.LastUsed == nil || now.Sub(*t.LastUsed) >= touchEvery {
		_, _ = s.db.Exec(`UPDATE access_tokens SET last_used_at=? WHERE id=?`, now.Unix(), t.ID)
	}
	return t, nil
}
