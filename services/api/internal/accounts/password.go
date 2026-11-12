package accounts

import (
	"bufio"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Argon2Params are the argon2id cost settings (RFC 9106). They are written into every stored hash, so a hash is always
// verified with the settings it was made with and raising them later only affects new passwords.
type Argon2Params struct {
	MemoryKiB uint32
	Time      uint32
	Threads   uint8
}

// DefaultArgon2 is ADR 0050 section 2: 64 MiB, three passes, one lane, a 16-byte salt per password and a 32-byte key.
var DefaultArgon2 = Argon2Params{MemoryKiB: 64 * 1024, Time: 3, Threads: 1}

const (
	saltLen = 16
	keyLen  = 32
	// MinPasswordRunes is the shortest password accepted; MaxPasswordBytes bounds the work one request can ask for.
	MinPasswordRunes = 12
	MaxPasswordBytes = 1024
)

// hashPassword returns the PHC string of an argon2id hash: $argon2id$v=19$m=65536,t=3,p=1$<salt>$<key>, base64 without padding.
func hashPassword(pw string, p Argon2Params) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key := argon2.IDKey([]byte(pw), salt, p.Time, p.MemoryKiB, p.Threads, keyLen)
	b := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.MemoryKiB, p.Time, p.Threads, b.EncodeToString(salt), b.EncodeToString(key)), nil
}

var errBadHash = errors.New("stored password hash is not argon2id")

// parseHash reads a PHC string back into its settings, salt and key. Settings far outside what this program writes are
// refused rather than run, so a damaged row cannot make one sign-in take minutes or gigabytes.
func parseHash(s string) (Argon2Params, []byte, []byte, error) {
	parts := strings.Split(s, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return Argon2Params{}, nil, nil, errBadHash
	}
	var v int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &v); err != nil || v != argon2.Version {
		return Argon2Params{}, nil, nil, errBadHash
	}
	var p Argon2Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.MemoryKiB, &p.Time, &p.Threads); err != nil {
		return Argon2Params{}, nil, nil, errBadHash
	}
	if p.MemoryKiB < 8 || p.MemoryKiB > 1<<20 || p.Time < 1 || p.Time > 16 || p.Threads < 1 || p.Threads > 16 {
		return Argon2Params{}, nil, nil, errBadHash
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[4])
	key, err2 := base64.RawStdEncoding.DecodeString(parts[5])
	if err1 != nil || err2 != nil || len(salt) < 8 || len(key) < 16 {
		return Argon2Params{}, nil, nil, errBadHash
	}
	return p, salt, key, nil
}

// verifyPassword recomputes the key with the stored settings and compares in constant time.
func verifyPassword(pw, stored string) bool {
	p, salt, key, err := parseHash(stored)
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(pw), salt, p.Time, p.MemoryKiB, p.Threads, uint32(len(key)))
	return subtle.ConstantTimeCompare(got, key) == 1
}

//go:embed common_passwords.txt
var commonText string

var common = func() map[string]bool {
	m := map[string]bool{}
	sc := bufio.NewScanner(strings.NewReader(commonText))
	for sc.Scan() {
		if w := strings.TrimSpace(sc.Text()); w != "" && !strings.HasPrefix(w, "#") {
			m[strings.ToLower(w)] = true
		}
	}
	return m
}()

// ErrWeakPassword carries the reason a new password was refused, in words for the person choosing it.
type ErrWeakPassword struct{ Reason string }

func (e ErrWeakPassword) Error() string { return e.Reason }

// CheckNewPassword applies the rules for a password being set (ADR 0050 section 2): at least 12 characters, at most 1024
// bytes, not the username, and not on the bundled list of common passwords (compared without case).
func CheckNewPassword(pw, username string) error {
	switch {
	case !utf8.ValidString(pw):
		return ErrWeakPassword{"the password is not valid text"}
	case utf8.RuneCountInString(pw) < MinPasswordRunes:
		return ErrWeakPassword{fmt.Sprintf("use at least %d characters", MinPasswordRunes)}
	case len(pw) > MaxPasswordBytes:
		return ErrWeakPassword{fmt.Sprintf("use at most %d bytes", MaxPasswordBytes)}
	case strings.EqualFold(pw, username):
		return ErrWeakPassword{"the password must not be the username"}
	case common[strings.ToLower(pw)]:
		return ErrWeakPassword{"this password is on a list of commonly used passwords; choose another"}
	case strings.Count(pw, string([]rune(pw)[0])) == utf8.RuneCountInString(pw):
		return ErrWeakPassword{"the password must not repeat one character"}
	}
	return nil
}
