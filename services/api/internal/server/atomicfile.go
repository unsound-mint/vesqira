package server

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
)

// writeFileAtomic replaces rel (slash-separated, below rootDir) with data so a reader sees the old file or the new one,
// never half of one: the content goes to a temporary file in the same folder (the same filesystem, so the rename is
// atomic), is flushed to disk, takes the requested permission bits, and is renamed over the target. The folder is flushed
// afterwards so the rename survives a crash. Owner and extended attributes of an existing file are not carried over.
//
// Every step goes through an os.Root opened on rootDir, so a folder swapped for a symbolic link between the caller's
// check and this write cannot send the temporary file or the rename outside rootDir: the kernel-level lookup refuses it.
func writeFileAtomic(rootDir, rel string, data []byte, perm os.FileMode) error {
	root, err := os.OpenRoot(rootDir)
	if err != nil {
		return err
	}
	defer root.Close()
	dir, name := path.Split(rel)
	dir = path.Clean(dir) // "" becomes "."
	if len(name) > 100 {
		name = name[:100] // a long name plus the temporary suffix must stay under the 255-byte limit
	}
	var tmp *os.File
	var tname string
	for range 8 {
		var rnd [8]byte
		if _, err := rand.Read(rnd[:]); err != nil {
			return err
		}
		tname = path.Join(dir, "."+name+"."+hex.EncodeToString(rnd[:])+".tmp")
		tmp, err = root.OpenFile(tname, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			break
		}
		if !errors.Is(err, fs.ErrExist) {
			return err
		}
	}
	if tmp == nil {
		return err
	}
	defer root.Remove(tname) // a no-op once the rename has happened
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := root.Rename(tname, filepath.ToSlash(rel)); err != nil {
		return err
	}
	if d, err := root.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}
