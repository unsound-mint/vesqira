package server

import (
	"os"
	"path/filepath"
)

// writeFileAtomic replaces dir/name with data so a reader sees the old file or the new one, never half of one: the
// content goes to a temporary file in the same folder (the same filesystem, so the rename is atomic), is flushed to disk,
// takes the requested permission bits, and is renamed over the target. The folder is flushed afterwards so the rename
// survives a crash. Owner and extended attributes of an existing file are not carried over.
func writeFileAtomic(dir, name string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(dir, "."+name+".*")
	if err != nil {
		return err
	}
	tname := tmp.Name()
	defer os.Remove(tname) // a no-op once the rename has happened
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
	if err := os.Rename(tname, filepath.Join(dir, name)); err != nil {
		return err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}
