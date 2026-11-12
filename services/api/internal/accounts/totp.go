package accounts

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"hash"
	"net/url"
	"time"
)

// TOTP as RFC 6238 over HOTP (RFC 4226). The second factor uses the defaults every authenticator app understands: HMAC-SHA1,
// a 30-second step from the Unix epoch, six digits, a 20-byte secret, and one step of clock drift either way. SHA-256 and
// SHA-512 are only here so the RFC's own test vectors can be checked.

const (
	totpStep    = 30
	totpDigits  = 6
	totpSkew    = 1 // steps accepted before and after the current one
	totpSecretN = 20
)

// hotp is RFC 4226 section 5.3: HMAC of the big-endian counter, dynamic truncation, then the low decimal digits.
func hotp(secret []byte, counter uint64, digits int, h func() hash.Hash) string {
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], counter)
	m := hmac.New(h, secret)
	m.Write(msg[:])
	sum := m.Sum(nil)
	off := sum[len(sum)-1] & 0x0f
	code := binary.BigEndian.Uint32(sum[off:off+4]) & 0x7fffffff
	mod := uint32(1)
	for i := 0; i < digits; i++ {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", digits, code%mod)
}

// totpAt is the code for the step holding t.
func totpAt(secret []byte, t time.Time, digits int, h func() hash.Hash) string {
	return hotp(secret, uint64(t.Unix()/totpStep), digits, h)
}

// matchStep returns the step within the drift window whose code equals code, or 0. Every candidate is compared, in constant
// time, so how long it takes does not say which one matched.
func matchStep(secret []byte, code string, now time.Time) int64 {
	if len(code) != totpDigits {
		return 0
	}
	cur := now.Unix() / totpStep
	var found int64
	for d := int64(-totpSkew); d <= totpSkew; d++ {
		st := cur + d
		if st <= 0 {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(hotp(secret, uint64(st), totpDigits, sha1.New)), []byte(code)) == 1 && found == 0 {
			found = st
		}
	}
	return found
}

var b32 = base32.StdEncoding.WithPadding(base32.NoPadding)

// OTPAuthURI is the enrolment link authenticator apps read (as text or as a QR code of it): the issuer and the account
// label, the secret in base32, and the settings spelled out.
func OTPAuthURI(issuer, account string, secret []byte) string {
	label := url.PathEscape(issuer + ":" + account)
	q := url.Values{}
	q.Set("secret", b32.EncodeToString(secret))
	q.Set("issuer", issuer)
	q.Set("algorithm", "SHA1")
	q.Set("digits", fmt.Sprint(totpDigits))
	q.Set("period", fmt.Sprint(totpStep))
	return "otpauth://totp/" + label + "?" + q.Encode()
}
