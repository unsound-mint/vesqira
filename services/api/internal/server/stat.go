package server

import (
	"os"

	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
)

func statFile(p string) (os.FileInfo, error) { return os.Stat(p) }

// msgOtherFile is what a person reads when the name still exists with the recorded size and time but the inode is not the one
// recorded (ADR 0050 section 8): another file was put under the name. Nothing is registered again by itself; registering is the
// person's decision.
const msgOtherFile = "this name now belongs to a different file than the one that was registered (it was replaced or copied over); check it is the file you mean, then register it again"

// differentFile reports whether a stat result is another file than the one recorded with inode ino. Only the inode is compared:
// the device number of a removable or network drive changes when it is mounted again, and the same inode with the same size and
// nanosecond modification time is the same file on a remounted drive (the caller then records the new device, Store.NoteFileID).
// A location with no recorded identity (made before schema 27) matches; the caller records it once the size and time agree.
func differentFile(info os.FileInfo, _ int64, ino int64) bool {
	if ino == 0 {
		return false
	}
	id, ok := sourceroot.IDOf(info)
	return ok && id.Ino != ino
}

// fileIDOf is the device and inode of a stat result, 0, 0 when the platform gives none.
func fileIDOf(info os.FileInfo) (dev, ino int64) {
	id, _ := sourceroot.IDOf(info)
	return id.Dev, id.Ino
}
