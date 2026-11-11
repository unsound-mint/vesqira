package server

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/transfers"
)

// On many clusters the home folder is a link to the real file system, so a host's source root as the owner typed it is not the
// path the host resolves it to. The transfer must work through it, in both directions, and be verified (the hash calls name
// the folder the way the owner did and the host resolves it again).
func TestAHostRootThatIsASymbolicLinkWorksBothWays(t *testing.T) {
	e := newXferEnv(t)
	link := filepath.Join(e.base, "home-link")
	if err := os.Symlink(e.remote, link); err != nil {
		t.Fatal(err)
	}
	h, err := e.srv.HostDB.Create(hosts.Host{Name: "Linked", Dest: "login3", Roots: []string{link}})
	if err != nil {
		t.Fatal(err)
	}
	want := e.file(e.remote, "cube.fits", 4000)
	d := e.mustCreate(map[string]any{"direction": "download", "hostId": h.ID, "sourcePath": filepath.Join(link, "cube.fits"), "destDir": e.local})
	e.run()
	if g := e.get(d.ID); g.State != transfers.StateDone || g.Verif.Status != transfers.Verified || g.Verif.DestSHA256 != sha(want) {
		t.Fatalf("%+v", g)
	}
	up := e.mustCreate(map[string]any{"direction": "upload", "hostId": h.ID, "sourcePath": filepath.Join(e.local, "cube.fits"), "destDir": link, "name": "back.fits"})
	e.run()
	g := e.get(up.ID)
	got, _ := os.ReadFile(filepath.Join(e.remote, "back.fits"))
	if g.State != transfers.StateDone || g.Verif.Status != transfers.Verified || string(got) != string(want) {
		t.Fatalf("%+v", g)
	}
	// a conflict through the link is found, not missed
	again := e.mustCreate(map[string]any{"direction": "upload", "hostId": h.ID, "sourcePath": filepath.Join(e.local, "cube.fits"), "destDir": link, "name": "back.fits"})
	e.run()
	if g := e.get(again.ID); g.State != transfers.StateDone || g.Detail == "" {
		t.Fatalf("an identical file behind a linked root must be seen as already there: %+v", g)
	}
	if e.sshCalls("rsync --server") != 2 {
		t.Fatalf("the identical file was sent again: %d rsync runs", e.sshCalls("rsync --server"))
	}
}
