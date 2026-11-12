package accounts

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// The TOTP secret is stored sealed under the server's own secret key (ADR 0050 sections 2 and 7): AES-256-GCM with the account
// id as associated data, so a sealed secret copied onto another account's row does not open. The key lives in a 0600 file in
// a 0700 folder that the export does not include; losing it means enrolling the second factor again, never losing data.

const keyBytes = 32

// LoadOrCreateKey reads the server key from path, or creates it (random, 0600, in a 0700 folder) when the file does not exist.
// A file of the wrong size or readable by others is refused, not repaired.
func LoadOrCreateKey(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, err
		}
		k := make([]byte, keyBytes)
		if _, err := rand.Read(k); err != nil {
			return nil, err
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, err
		}
		if _, err := f.Write(k); err != nil {
			f.Close()
			return nil, err
		}
		return k, f.Close()
	}
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%s is readable by other users (mode %o): chmod 600 it", path, st.Mode().Perm())
	}
	if len(b) != keyBytes {
		return nil, fmt.Errorf("%s is not a server key (%d bytes, want %d)", path, len(b), keyBytes)
	}
	return b, nil
}

func gcm(key []byte) (cipher.AEAD, error) {
	if len(key) != keyBytes {
		return nil, errors.New("the server key is not set")
	}
	blk, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(blk)
}

func seal(key, plain []byte, accountID string) ([]byte, error) {
	a, err := gcm(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, a.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return a.Seal(nonce, nonce, plain, []byte(accountID)), nil
}

func open(key, sealed []byte, accountID string) ([]byte, error) {
	a, err := gcm(key)
	if err != nil {
		return nil, err
	}
	if len(sealed) < a.NonceSize() {
		return nil, errors.New("sealed secret is too short")
	}
	return a.Open(nil, sealed[:a.NonceSize()], sealed[a.NonceSize():], []byte(accountID))
}
