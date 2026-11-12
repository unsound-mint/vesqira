package accounts

import (
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"database/sql"
	"encoding/base64"
	"errors"
	"hash"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"
	_ "modernc.org/sqlite"
)

// cheap is used everywhere except the test of the stored parameters, so the suite and -race stay fast.
var cheap = Argon2Params{MemoryKiB: 64, Time: 1, Threads: 1}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func newStore(t *testing.T) (*Store, *clock) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "a.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	s, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	s.Now, s.Params, s.KnownScopes = c.now, cheap, []string{"read", "write", "push", "relay"}
	s.SetKey(make([]byte, 32))
	return s, c
}

// admin creates an admin through a setup link, as the bootstrap command and the setup page do.
func admin(t *testing.T, s *Store, username string) Account {
	t.Helper()
	tok, _, err := s.CreateSetupToken()
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateAdminFromSetup(tok, "Ada", username, "a long passphrase here")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// RFC 6238 Appendix B, every row: 8 digits, step 30, T0 0, with the 20-, 32- and 64-byte ASCII seeds of the RFC's reference code.
func TestTOTPMatchesTheRFC6238Vectors(t *testing.T) {
	seed := func(n int) []byte { return []byte(strings.Repeat("1234567890", 7)[:n]) }
	modes := []struct {
		h    func() hash.Hash
		seed []byte
	}{{sha1.New, seed(20)}, {sha256.New, seed(32)}, {sha512.New, seed(64)}}
	rows := []struct {
		t     int64
		codes [3]string
	}{
		{59, [3]string{"94287082", "46119246", "90693936"}},
		{1111111109, [3]string{"07081804", "68084774", "25091201"}},
		{1111111111, [3]string{"14050471", "67062674", "99943326"}},
		{1234567890, [3]string{"89005924", "91819424", "93441116"}},
		{2000000000, [3]string{"69279037", "90698825", "38618901"}},
		{20000000000, [3]string{"65353130", "77737706", "47863826"}},
	}
	for _, r := range rows {
		for i, m := range modes {
			if got := totpAt(m.seed, time.Unix(r.t, 0), 8, m.h); got != r.codes[i] {
				t.Errorf("T=%d mode %d: got %s, want %s", r.t, i, got, r.codes[i])
			}
		}
	}
	// the six-digit code the app shows is the low six digits of the same truncation
	if got := totpAt(seed(20), time.Unix(59, 0), 6, sha1.New); got != "287082" {
		t.Errorf("six digits: %s", got)
	}
}

func TestOTPAuthURICarriesTheSettings(t *testing.T) {
	u := OTPAuthURI("Vesqira (lab.example.edu)", "ada", []byte("12345678901234567890"))
	for _, want := range []string{"otpauth://totp/", "secret=GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ", "algorithm=SHA1", "digits=6", "period=30", "ada"} {
		if !strings.Contains(u, want) {
			t.Errorf("%s lacks %s", u, want)
		}
	}
}

// The hash written for a real password uses the ADR's parameters, and they are read back from the stored string.
func TestArgon2ParametersAsStored(t *testing.T) {
	s, _ := newStore(t)
	s.Params = DefaultArgon2
	a := admin(t, s, "ada")
	var stored string
	if err := s.db.QueryRow(`SELECT hash FROM password_credentials WHERE account_id=?`, a.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, "$argon2id$v=19$m=65536,t=3,p=1$") {
		t.Fatalf("stored hash %q does not state argon2id m=64 MiB t=3 p=1", stored)
	}
	p, salt, key, err := parseHash(stored)
	if err != nil || p != DefaultArgon2 || len(salt) != 16 || len(key) != 32 {
		t.Fatalf("parsed %+v salt %d key %d %v", p, len(salt), len(key), err)
	}
	if argon2.Version != 0x13 {
		t.Fatal("argon2 version")
	}
	if strings.Contains(stored, "a long passphrase here") {
		t.Fatal("the password is in the row")
	}
	if _, ok, _ := s.CheckPassword("ada", "a long passphrase here"); !ok {
		t.Fatal("the right password was refused")
	}
	if _, ok, _ := s.CheckPassword("ada", "a long passphrase herE"); ok {
		t.Fatal("a wrong password was accepted")
	}
	// a cheaper hash made earlier keeps verifying with the settings it states
	old, _ := hashPassword("another long one!", cheap)
	if !verifyPassword("another long one!", old) || verifyPassword("another long one?", old) {
		t.Fatal("verification does not follow the stored settings")
	}
	for _, bad := range []string{"", "$argon2i$v=19$m=65536,t=3,p=1$c2FsdHNhbHQ$a2V5a2V5a2V5a2V5a2V5", "$argon2id$v=19$m=99999999,t=3,p=1$c2FsdHNhbHQ$a2V5a2V5a2V5a2V5a2V5"} {
		if verifyPassword("x", bad) {
			t.Errorf("%q verified", bad)
		}
	}
}

func TestNewPasswordRules(t *testing.T) {
	for pw, ok := range map[string]bool{
		"short":                    false,
		"elevenchars":              false,
		"password1234":             false, // common list
		"PassWord1234":             false, // common, any case
		"aaaaaaaaaaaaaaa":          false,
		"ada-the-admin":            true,
		"a long passphrase here":   true,
		strings.Repeat("é", 12):    false, // one character repeated
		"ünïcödé pässwörd":         true,
		strings.Repeat("x", 1025):  false,
		"correct horse battery 42": true,
	} {
		if err := CheckNewPassword(pw, "ada"); (err == nil) != ok {
			t.Errorf("%q: %v, want ok=%v", pw, err, ok)
		}
	}
	if CheckNewPassword("adalovelace1", "AdaLovelace1") == nil {
		t.Error("the username was accepted as the password")
	}
}

// An unknown name and a known name with a wrong password give the same answer and both run the argon2 verification.
func TestUnknownNameIsVerifiedLikeAWrongPassword(t *testing.T) {
	s, _ := newStore(t)
	admin(t, s, "ada")
	before := s.Verifications()
	a1, ok1, err1 := s.CheckPassword("nobody", "a long passphrase here")
	a2, ok2, err2 := s.CheckPassword("ada", "not the passphrase")
	if ok1 || ok2 || err1 != nil || err2 != nil || a1 != a2 {
		t.Fatalf("answers differ: %+v %v %v / %+v %v %v", a1, ok1, err1, a2, ok2, err2)
	}
	if got := s.Verifications() - before; got != 2 {
		t.Fatalf("%d verifications for two attempts: an unknown name skipped the hash", got)
	}
	if _, ok, _ := s.CheckPassword(" ADA ", "a long passphrase here"); !ok {
		t.Fatal("the username is not case- and space-insensitive")
	}
}

func TestSetupLinkIsSingleUseAndExpires(t *testing.T) {
	s, c := newStore(t)
	tok, exp, err := s.CreateSetupToken()
	if err != nil || exp.Sub(c.now()) != SetupTTL {
		t.Fatal(err, exp)
	}
	var stored string
	_ = s.db.QueryRow(`SELECT token_hash FROM setup_tokens`).Scan(&stored)
	if stored == tok || stored != Hash(tok) {
		t.Fatal("the setup secret is stored in the clear")
	}
	if _, err := s.CreateAdminFromSetup(tok, "Ada", "ada", "short"); err == nil {
		t.Fatal("a weak password was accepted")
	}
	if _, err := s.CreateAdminFromSetup(tok, "Ada", "ada", "a long passphrase here"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateAdminFromSetup(tok, "Bob", "bob", "a long passphrase here"); !errors.Is(err, ErrSetupInvalid) {
		t.Fatalf("second use: %v", err)
	}
	tok2, _, _ := s.CreateSetupToken()
	c.add(SetupTTL)
	if _, err := s.CreateAdminFromSetup(tok2, "Bob", "bob", "a long passphrase here"); !errors.Is(err, ErrSetupInvalid) {
		t.Fatalf("expired link: %v", err)
	}
	tok3, _, _ := s.CreateSetupToken()
	if _, err := s.CreateAdminFromSetup(tok3, "Ada 2", "ADA", "a long passphrase here"); !errors.Is(err, ErrUsernameTaken) {
		t.Fatalf("taken name: %v", err)
	}
	if ok, _ := s.HasAdmin(); !ok {
		t.Fatal("no admin")
	}
}

func enrol(t *testing.T, s *Store, c *clock, id string) ([]byte, []string) {
	t.Helper()
	secret, err := s.StartEnrolment(id)
	if err != nil {
		t.Fatal(err)
	}
	codes, ok, err := s.ConfirmEnrolment(id, totpAt(secret, c.now(), 6, sha1.New))
	if err != nil || !ok || len(codes) != 10 {
		t.Fatalf("confirm: %v %v %d", ok, err, len(codes))
	}
	return secret, codes
}

func TestTOTPReplayIsRefused(t *testing.T) {
	s, c := newStore(t)
	a := admin(t, s, "ada")
	secret, _ := enrol(t, s, c, a.ID)
	// the enrolment spent the current step
	if ok, _ := s.VerifyTOTP(a.ID, totpAt(secret, c.now(), 6, sha1.New)); ok {
		t.Fatal("the code used to confirm enrolment was accepted again")
	}
	c.add(30 * time.Second)
	code := totpAt(secret, c.now(), 6, sha1.New)
	if ok, _ := s.VerifyTOTP(a.ID, code); !ok {
		t.Fatal("a fresh code was refused")
	}
	if ok, _ := s.VerifyTOTP(a.ID, code); ok {
		t.Fatal("the same code was accepted twice")
	}
	c.add(30 * time.Second)
	if ok, _ := s.VerifyTOTP(a.ID, code); ok {
		t.Fatal("the previous step's code was accepted after it was spent")
	}
	c.add(30 * time.Second)
	if ok, _ := s.VerifyTOTP(a.ID, "000000"); ok && totpAt(secret, c.now(), 6, sha1.New) != "000000" {
		t.Fatal("a wrong code was accepted")
	}
	// the secret is sealed: not in the row, and it does not open as another account's
	var sealed []byte
	_ = s.db.QueryRow(`SELECT secret FROM totp_credentials WHERE account_id=?`, a.ID).Scan(&sealed)
	if strings.Contains(string(sealed), string(secret)) {
		t.Fatal("the TOTP secret is stored in the clear")
	}
	if _, err := open(s.key, sealed, "acct-other"); err == nil {
		t.Fatal("a sealed secret opened for another account")
	}
}

// A first enrolment never replaces an enrolled secret; a replacement (a new phone, through a full session) does.
func TestFirstEnrolmentDoesNotReplaceAnEnrolledSecret(t *testing.T) {
	s, c := newStore(t)
	a := admin(t, s, "ada")
	enrol(t, s, c, a.ID)
	next, _ := s.StartEnrolment(a.ID)
	c.add(30 * time.Second)
	if _, ok, _ := s.ConfirmFirstEnrolment(a.ID, totpAt(next, c.now(), 6, sha1.New)); ok {
		t.Fatal("a first enrolment replaced the enrolled secret")
	}
	if _, ok, _ := s.ConfirmEnrolment(a.ID, totpAt(next, c.now(), 6, sha1.New)); !ok {
		t.Fatal("the replacement was refused")
	}
}

// Two requests carrying the same code at once: exactly one wins.
func TestTOTPReplayRace(t *testing.T) {
	s, c := newStore(t)
	a := admin(t, s, "ada")
	secret, _ := enrol(t, s, c, a.ID)
	c.add(30 * time.Second)
	code := totpAt(secret, c.now(), 6, sha1.New)
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, err := s.VerifyTOTP(a.ID, code); ok && err == nil {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d requests accepted one code", wins)
	}
}

func TestRecoveryCodesAreSingleUseAndHashed(t *testing.T) {
	s, c := newStore(t)
	a := admin(t, s, "ada")
	_, codes := enrol(t, s, c, a.ID)
	rows, _ := s.db.Query(`SELECT code_hash FROM recovery_codes`)
	for rows.Next() {
		var h string
		_ = rows.Scan(&h)
		for _, code := range codes {
			if strings.Contains(h, strings.ReplaceAll(code, "-", "")) {
				t.Fatal("a recovery code is stored in the clear")
			}
		}
	}
	rows.Close()
	if ok, _ := s.UseRecoveryCode(a.ID, strings.ToUpper(codes[0])); !ok {
		t.Fatal("a recovery code was refused")
	}
	if ok, _ := s.UseRecoveryCode(a.ID, codes[0]); ok {
		t.Fatal("a recovery code worked twice")
	}
	if n, _ := s.RecoveryCodesLeft(a.ID); n != 9 {
		t.Fatalf("%d left", n)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins := 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, _ := s.UseRecoveryCode(a.ID, codes[1]); ok {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatalf("%d concurrent uses of one recovery code succeeded", wins)
	}
	fresh, _ := s.NewRecoveryCodes(a.ID)
	if ok, _ := s.UseRecoveryCode(a.ID, codes[2]); ok {
		t.Fatal("an old code survived a new set")
	}
	if ok, _ := s.UseRecoveryCode(a.ID, fresh[0]); !ok {
		t.Fatal("a new code was refused")
	}
	other := admin(t, s, "bob")
	if ok, _ := s.UseRecoveryCode(other.ID, fresh[1]); ok {
		t.Fatal("one account's code worked for another")
	}
}

func TestResetSecondFactorEndsSessionsAndEnrolment(t *testing.T) {
	s, c := newStore(t)
	a := admin(t, s, "ada")
	enrol(t, s, c, a.ID)
	id, _ := s.CreateSession(a.ID, StageFull, "1.2.3.4", "ua")
	if err := s.ResetSecondFactor(a.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Get(a.ID); got.SecondFactor {
		t.Fatal("still enrolled")
	}
	if _, err := s.Session(id); !errors.Is(err, ErrNotFound) {
		t.Fatal("a session survived the reset")
	}
	if n, _ := s.RecoveryCodesLeft(a.ID); n != 0 {
		t.Fatal("recovery codes survived the reset")
	}
}

func TestSessionTimeoutsAndRotation(t *testing.T) {
	s, c := newStore(t)
	a := admin(t, s, "ada")
	id, _ := s.CreateSession(a.ID, StageFull, "1.2.3.4", "ua")
	var stored string
	_ = s.db.QueryRow(`SELECT id_hash FROM sessions`).Scan(&stored)
	if stored == id || stored != Hash(id) || len(base64Decode(t, id)) != 32 {
		t.Fatal("the session id is stored in the clear or is not 256 bits")
	}
	// idle: used every 11 hours it lives on; then 12 idle hours end it
	for i := 0; i < 3; i++ {
		c.add(11 * time.Hour)
		if _, err := s.Session(id); err != nil {
			t.Fatalf("active session ended after %d steps: %v", i, err)
		}
	}
	c.add(IdleTimeout)
	if _, err := s.Session(id); !errors.Is(err, ErrNotFound) {
		t.Fatal("an idle session survived")
	}
	// absolute: kept busy, it still ends seven days after sign-in
	id, _ = s.CreateSession(a.ID, StageFull, "1.2.3.4", "ua")
	for el := time.Duration(0); el < AbsoluteTimeout-time.Hour; el += 6 * time.Hour {
		c.add(6 * time.Hour)
		if _, err := s.Session(id); err != nil && el+6*time.Hour < AbsoluteTimeout {
			t.Fatalf("ended early at %v: %v", el, err)
		}
	}
	c.add(6 * time.Hour)
	if _, err := s.Session(id); !errors.Is(err, ErrNotFound) {
		t.Fatal("a session outlived the absolute timeout")
	}
	// a pending session lasts PendingTTL
	p, _ := s.CreateSession(a.ID, StageSecondFactor, "", "")
	c.add(PendingTTL)
	if _, err := s.Session(p); !errors.Is(err, ErrNotFound) {
		t.Fatal("a pending session outlived its ten minutes")
	}
	// promotion: a new id, the old one dead
	p, _ = s.CreateSession(a.ID, StageSecondFactor, "", "")
	se, _ := s.Session(p)
	if se.Recent(c.now()) {
		t.Fatal("a pending session counts as a recent sign-in")
	}
	full, err := s.Promote(se, "", "", false)
	if err != nil || full == p {
		t.Fatalf("promotion kept the id: %v", err)
	}
	if _, err := s.Session(p); !errors.Is(err, ErrNotFound) {
		t.Fatal("the pre-promotion id still works")
	}
	fs, _ := s.Session(full)
	if !fs.Recent(c.now()) || fs.Stage != StageFull {
		t.Fatalf("%+v", fs)
	}
	c.add(RecentWindow)
	if fs, _ = s.Session(full); fs.Recent(c.now()) {
		t.Fatal("recent after the window")
	}
	if _, err := s.Promote(se, "", "", false); !errors.Is(err, ErrNotFound) {
		t.Fatal("an ended session was promoted again")
	}
	// sign out everywhere
	_, _ = s.CreateSession(a.ID, StageFull, "", "")
	if n, _ := s.EndSessions(a.ID); n != 2 {
		t.Fatalf("ended %d", n)
	}
}

func base64Decode(t *testing.T, s string) []byte {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestTokensRevokedOrExpiredAreRefused(t *testing.T) {
	s, c := newStore(t)
	a := admin(t, s, "ada")
	raw, tok, err := s.CreateToken(a.ID, "laptop", []string{"read", "read", "push"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, TokenPrefix) || len(base64Decode(t, strings.TrimPrefix(raw, TokenPrefix))) != 32 {
		t.Fatal("not a 256-bit token")
	}
	if tok.Expires.Sub(tok.Created) != TokenDefaultTTL || strings.Join(tok.Scopes, " ") != "read push" {
		t.Fatalf("%+v", tok)
	}
	var stored string
	_ = s.db.QueryRow(`SELECT token_hash FROM access_tokens`).Scan(&stored)
	if stored == raw || stored != Hash(raw) {
		t.Fatal("the token is stored in the clear")
	}
	if got, err := s.LookupToken(raw); err != nil || got.ID != tok.ID {
		t.Fatal(err)
	}
	if _, err := s.LookupToken(raw + "x"); err == nil {
		t.Fatal("a different value worked")
	}
	other := admin(t, s, "bob")
	if _, err := s.RevokeToken(other.ID, tok.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("another account revoked the token")
	}
	if _, err := s.RevokeToken(a.ID, tok.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LookupToken(raw); !errors.Is(err, ErrNotFound) {
		t.Fatal("a revoked token works")
	}
	raw2, _, _ := s.CreateToken(a.ID, "short", []string{"write"}, 2*time.Hour)
	c.add(2 * time.Hour)
	if _, err := s.LookupToken(raw2); !errors.Is(err, ErrNotFound) {
		t.Fatal("an expired token works")
	}
	for _, bad := range []struct {
		scopes []string
		ttl    time.Duration
	}{{nil, 0}, {[]string{"admin"}, 0}, {[]string{"read"}, TokenMaxTTL + time.Hour}, {[]string{"read"}, time.Minute}} {
		if _, _, err := s.CreateToken(a.ID, "x", bad.scopes, bad.ttl); err == nil {
			t.Errorf("created a token with %v %v", bad.scopes, bad.ttl)
		}
	}
	if _, _, err := s.CreateToken(a.ID, "year", []string{"relay"}, TokenMaxTTL); err != nil {
		t.Fatal(err)
	}
}

func TestLimiterBacksOffWithoutSayingWhetherTheNameExists(t *testing.T) {
	c := &clock{t: time.Unix(1_800_000_000, 0)}
	l := &Limiter{Now: c.now}
	for i := 0; i < 5; i++ {
		if ok, _ := l.Allow("ada", "1.1.1.1"); !ok {
			t.Fatalf("refused attempt %d", i)
		}
		l.Fail("ada", "1.1.1.1")
	}
	ok, wait := l.Allow("ada", "2.2.2.2")
	if ok || wait != 30*time.Second {
		t.Fatalf("after five failures: %v %v (the name, not the address, is slowed)", ok, wait)
	}
	c.add(30 * time.Second)
	if ok, _ := l.Allow("ada", "2.2.2.2"); !ok {
		t.Fatal("still refused after the wait")
	}
	l.Fail("ada", "2.2.2.2")
	if _, wait := l.Allow("ada", "2.2.2.2"); wait != time.Minute {
		t.Fatalf("the wait did not double: %v", wait)
	}
	if ok, _ := l.Allow("bob", "3.3.3.3"); !ok {
		t.Fatal("another name was slowed")
	}
	l.Succeed("ada")
	if ok, _ := l.Allow("ada", "4.4.4.4"); !ok {
		t.Fatal("a success did not clear the name")
	}
	// per address: thirty failures for thirty different names stop that address
	for i := 0; i < 30; i++ {
		l.Fail(strings.Repeat("n", i+1), "9.9.9.9")
	}
	if ok, _ := l.Allow("fresh", "9.9.9.9"); ok {
		t.Fatal("a spraying address was not stopped")
	}
	c.add(15 * time.Minute)
	if ok, _ := l.Allow("fresh", "9.9.9.9"); !ok {
		t.Fatal("the address stayed stopped past the window")
	}
}
