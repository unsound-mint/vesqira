//go:build linux

package main

import "golang.org/x/sys/unix"

// networkFS reports whether dir is on a network file system, where SQLite's locking is unsafe (ADR 0050 section 1).
func networkFS(dir string) (bool, string) {
	var st unix.Statfs_t
	if err := unix.Statfs(dir, &st); err != nil {
		return false, ""
	}
	switch uint32(st.Type) {
	case 0x6969:
		return true, "NFS"
	case 0xFF534D42, 0xFE534D42:
		return true, "CIFS/SMB"
	case 0x517B:
		return true, "SMB"
	case 0x564C:
		return true, "NCP"
	case 0x5346414F:
		return true, "AFS"
	case 0x00C36400:
		return true, "Ceph"
	case 0x47504653:
		return true, "GPFS"
	case 0x0BD00BD0:
		return true, "Lustre"
	case 0x65735546: // FUSE: sshfs and others, and some local ones; refused rather than guessed safe
		return true, "FUSE"
	}
	return false, ""
}
