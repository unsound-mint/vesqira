package server

import (
	"os"

	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
)

func statFile(p string) (os.FileInfo, error) { return os.Stat(p) }

// msgOtherFile is what a person reads when the name still exists with the recorded size and time but the device or inode is not
// the one recorded (ADR 0050 section 8): another file was put under the name, or the drive was mounted again with another
// device number. Nothing is registered again by itself; registering is the person's decision.
const msgOtherFile = "this name now belongs to a different file than the one that was registered (it was replaced, or its drive was mounted again); check it is the file you mean, then register it again"

// differentFile reports whether a stat result is another file than the one recorded as (dev, ino). A location with no recorded
// identity (made before schema 27) matches; the caller records it once the size and time agree (Store.NoteFileID).
func differentFile(info os.FileInfo, dev, ino int64) bool {
	if ino == 0 {
		return false
	}
	id, ok := sourceroot.IDOf(info)
	return ok && (id.Dev != dev || id.Ino != ino)
}

// fileIDOf is the device and inode of a stat result, 0, 0 when the platform gives none.
func fileIDOf(info os.FileInfo) (dev, ino int64) {
	id, _ := sourceroot.IDOf(info)
	return id.Dev, id.Ino
}
