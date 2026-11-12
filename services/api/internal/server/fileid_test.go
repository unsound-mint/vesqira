package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

// idServer is a server over one root with a fake plane worker, returning the store so a test can look at what was recorded.
func idServer(t *testing.T) (*httptest.Server, *catalog.Store, *fakePlanes, string) {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "data")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	rs, err := sourceroot.New([]string{root})
	if err != nil {
		t.Fatal(err)
	}
	st, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	fp := &fakePlanes{reply: worker.Reply{Head: json.RawMessage(`{"ok":true,"nbytes":0}`)}}
	ts := httptest.NewServer((&Server{Roots: rs, Store: st, Worker: &fakeWorker{}, Planes: fp, Cache: NewPlaneCache(8 << 20)}).Handler())
	t.Cleanup(ts.Close)
	return ts, st, fp, root
}

func register(t *testing.T, ts *httptest.Server, file string) int {
	t.Helper()
	var d catalog.Dataset
	resp := post(t, ts, file, nil)
	if err := json.NewDecoder(resp.Body).Decode(&d); err != nil || resp.StatusCode != 201 {
		t.Fatalf("register: %d %v", resp.StatusCode, err)
	}
	return int(d.ID)
}

// replaceKeepingSizeAndTime puts another file under the name with the same size and the same modification time to the nanosecond:
// the case the size and time comparison cannot see.
func replaceKeepingSizeAndTime(t *testing.T, file string) {
	t.Helper()
	fi, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	other := file + ".new"
	if err := os.WriteFile(other, []byte(strings.Repeat("y", int(fi.Size()))), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(other, time.Now(), fi.ModTime()); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(other, file); err != nil {
		t.Fatal(err)
	}
}

func devIno(t *testing.T, file string) (int64, int64) {
	t.Helper()
	fi, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	st := fi.Sys().(*syscall.Stat_t)
	return int64(st.Dev), int64(st.Ino)
}

func TestARegisteredFileReplacedWithEqualSizeAndTimeIsRefusedWithASentenceAndNotRegisteredAgain(t *testing.T) {
	ts, st, fp, root := idServer(t)
	file := filepath.Join(root, "a.fits")
	if err := os.WriteFile(file, []byte("xxxx"), 0o644); err != nil {
		t.Fatal(err)
	}
	id := register(t, ts, file)
	u := ts.URL + "/api/datasets/" + itoa(id) + "/plane?channel=0"

	if r, b := get(t, u); r.StatusCode != 200 {
		t.Fatalf("the registered file: %d %s", r.StatusCode, b)
	}
	dev, ino := devIno(t, file)
	if got := fp.calls[len(fp.calls)-1]["expect"]; got == nil || got.([]int64)[0] != dev || got.([]int64)[1] != ino {
		t.Fatalf("the worker must be told the device and inode just checked: %v, want [%d %d]", got, dev, ino)
	}

	replaceKeepingSizeAndTime(t, file)
	before := len(fp.calls)
	for i := 0; i < 2; i++ { // twice: nothing about the refusal registers or records the new file
		r, b := get(t, u)
		if r.StatusCode != 409 || !strings.Contains(string(b), "different file") || !strings.Contains(string(b), "register it again") {
			t.Fatalf("a replaced file: %d %s", r.StatusCode, b)
		}
	}
	if len(fp.calls) != before {
		t.Fatal("the worker must not run for another file")
	}
	d, _ := st.Get(int64(id))
	if d.FileIno != ino {
		t.Fatalf("the recorded inode must stay the registered file's: %d, want %d", d.FileIno, ino)
	}

	// the person registers it again on purpose: only then is the file under the name the dataset's file
	if r := post(t, ts, file, nil); r.StatusCode != 201 {
		t.Fatalf("register again: %d", r.StatusCode)
	}
	if r, b := get(t, u); r.StatusCode != 200 {
		t.Fatalf("after registering again: %d %s", r.StatusCode, b)
	}
}

func TestALocationFromBeforeSchema27IsRecordedOnceItMatchesAndThenRefusesAnotherFile(t *testing.T) {
	ts, st, _, root := idServer(t)
	file := filepath.Join(root, "old.fits")
	if err := os.WriteFile(file, []byte("xxxx"), 0o644); err != nil {
		t.Fatal(err)
	}
	id := register(t, ts, file)
	if _, err := st.DB().Exec(`UPDATE dataset_locations SET file_dev=0, file_ino=0`); err != nil { // as a catalog upgraded from schema 26 has it
		t.Fatal(err)
	}
	u := ts.URL + "/api/datasets/" + itoa(id) + "/plane?channel=0"
	if r, b := get(t, u); r.StatusCode != 200 {
		t.Fatalf("a location with no recorded identity is read as before: %d %s", r.StatusCode, b)
	}
	if d, _ := st.Get(int64(id)); d.FileIno == 0 {
		t.Fatal("the identity must be recorded once the size and time agree")
	}
	replaceKeepingSizeAndTime(t, file)
	if r, b := get(t, u); r.StatusCode != 409 || !strings.Contains(string(b), "different file") {
		t.Fatalf("after the swap: %d %s", r.StatusCode, b)
	}
}

func TestAnEditThatChangesSizeOrTimeStillSaysTheFileChanged(t *testing.T) {
	ts, _, _, root := idServer(t)
	file := filepath.Join(root, "a.fits")
	if err := os.WriteFile(file, []byte("xxxx"), 0o644); err != nil {
		t.Fatal(err)
	}
	id := register(t, ts, file)
	if err := os.WriteFile(file, []byte("a longer file"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, b := get(t, ts.URL+"/api/datasets/"+itoa(id)+"/plane?channel=0")
	if r.StatusCode != 409 || !strings.Contains(string(b), "changed since it was registered") {
		t.Fatalf("%d %s", r.StatusCode, b)
	}
}
