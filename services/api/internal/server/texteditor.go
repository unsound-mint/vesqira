package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/unsound-mint/vesqira/services/api/internal/discover"
)

// The editor for text files on this computer (ADR 0029, step 1). It exists only when the server was started with
// --enable-editor, and only on the loopback owner listener (the review listener is a different package that cannot
// reach it). A file is editable only when the same check that gates reading (s.roots().Resolve) accepts it, it is a
// plain text file of a known kind, and it is small. A save must name the file as it was opened and is refused with the
// current content when the file has changed since.

// maxTextFile is the largest file the editor opens or saves.
const maxTextFile = 2 << 20

// editableExt are the extensions the editor will open. A file with no extension is allowed too; everything else
// (FITS, images, archives, binaries, unknown formats) is not, however it looks inside.
var editableExt = map[string]bool{
	".toml": true, ".yaml": true, ".yml": true, ".json": true, ".jl": true, ".py": true, ".sh": true, ".slurm": true,
	".sbatch": true, ".md": true, ".txt": true, ".cfg": true, ".ini": true, ".conf": true, ".log": true,
}

// textVersion is a file as the editor saw it. Mtime is nanoseconds since the epoch as a decimal string (a JSON number
// loses the low digits in a browser). Sha256 is the hash of the bytes; all three must still match for a save.
type textVersion struct {
	Size   int64  `json:"size"`
	Mtime  string `json:"mtime"`
	Sha256 string `json:"sha256"`
}

type textFile struct {
	Path    string `json:"path"` // the canonical path, symbolic links resolved
	Name    string `json:"name"`
	Content string `json:"content"`
	textVersion
	HostID int64  `json:"hostId,omitempty"` // the file is on this host (hosttext.go), not on this computer
	Host   string `json:"host,omitempty"`   // that host's name
}

type textSave struct {
	Path    string      `json:"path"`
	Content string      `json:"content"`
	Base    textVersion `json:"base"`
	HostID  int64       `json:"hostId,omitempty"`
}

type textConflict struct {
	Error   string   `json:"error"`
	Current textFile `json:"current"`
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// fitsMagic is how every FITS primary header starts.
var fitsMagic = []byte("SIMPLE  =")

// checkTextBytes refuses content that is not plain text the editor may handle: FITS by magic, NUL bytes, invalid UTF-8.
func checkTextBytes(b []byte) (int, string) {
	switch {
	case bytes.HasPrefix(b, fitsMagic):
		return 415, "that is a FITS file; the editor never opens or writes FITS"
	case bytes.IndexByte(b, 0) >= 0:
		return 415, "that file has binary content; the editor opens text only"
	case !utf8.Valid(b):
		return 415, "that file is not valid UTF-8 text; the editor opens text only"
	}
	return 0, ""
}

// checkTextName refuses a file name the editor does not open: FITS and every extension outside the text allowlist.
func checkTextName(name string) (int, string) {
	ext := strings.ToLower(filepath.Ext(name))
	switch {
	case ext == ".fits" || ext == ".fit" || ext == ".fts" || ext == ".fz":
		return 415, "that is a FITS file; the editor never opens or writes FITS"
	case ext != "" && !editableExt[ext]:
		return 415, "the editor opens text files only (toml, yaml, json, jl, py, sh, slurm, md, txt, cfg, ini) or files with no extension"
	}
	return 0, ""
}

// readOnlyRole says whether real sits under a data/raw or data/external folder of any root that holds it: source data
// the project explorer marks read-only (discover.RoleOf is the scanner's own rule, so the two cannot disagree). The check
// is made on the path below each root, so a root that itself lives under a folder called data/raw is not locked by it.
func (s *Server) readOnlyRole(real string) (string, bool) {
	for _, pl := range s.roots().Under(real) {
		if role := discover.RoleOf(pl.Rel); discover.RoleIsReadOnly(role) {
			return role, true
		}
	}
	return "", false
}

// openText authorizes p and reads the file. status 0 is success; any other status comes with the message to show.
func (s *Server) openText(p string) (real string, fi os.FileInfo, data []byte, status int, msg string) {
	real, err := s.roots().Resolve(p)
	if err != nil || holdsCredentials(real) {
		return "", nil, nil, 404, "that file is not inside a folder Vesqira may read"
	}
	if role, ro := s.readOnlyRole(real); ro {
		return "", nil, nil, 403, "that file is " + role + " source data (data/" + role + "), which Vesqira treats as read-only; the editor does not open it"
	}
	if st, m := checkTextName(real); st != 0 {
		return "", nil, nil, st, m
	}
	f, err := os.Open(real)
	if err != nil {
		return "", nil, nil, 404, "that file is not inside a folder Vesqira may read"
	}
	defer f.Close()
	fi, err = f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return "", nil, nil, 404, "that file is not inside a folder Vesqira may read"
	}
	if fi.Size() > maxTextFile {
		return "", nil, nil, 413, "that file is larger than the editor's 2 MiB limit"
	}
	data, err = io.ReadAll(io.LimitReader(f, maxTextFile+1)) // the file may have grown since Stat
	if err != nil {
		return "", nil, nil, 502, "could not read that file"
	}
	if len(data) > maxTextFile {
		return "", nil, nil, 413, "that file is larger than the editor's 2 MiB limit"
	}
	if st, m := checkTextBytes(data); st != 0 {
		return "", nil, nil, st, m
	}
	return real, fi, data, 0, ""
}

func versionOf(fi os.FileInfo, data []byte) textVersion {
	return textVersion{Size: int64(len(data)), Mtime: strconv.FormatInt(fi.ModTime().UnixNano(), 10), Sha256: sha256Hex(data)}
}

func asTextFile(real string, fi os.FileInfo, data []byte) textFile {
	return textFile{Path: real, Name: filepath.Base(real), Content: string(data), textVersion: versionOf(fi, data)}
}

func (s *Server) getText(w http.ResponseWriter, r *http.Request) {
	if id := r.URL.Query().Get("hostId"); id != "" {
		s.getHostText(w, r, id)
		return
	}
	real, fi, data, status, msg := s.openText(r.URL.Query().Get("path"))
	if status != 0 {
		fail(w, status, msg)
		return
	}
	// like a save: hash first, path last (the detail is cut at 300 characters); never the content.
	s.Audit.Record("file opened for editing", "file", 0, "sha256 "+sha256Hex(data)+" "+real)
	writeJSON(w, 200, asTextFile(real, fi, data))
}

func (s *Server) putText(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20)) // JSON escaping can make 2 MiB of text larger on the wire
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			fail(w, 413, "that is larger than the editor's 2 MiB limit")
			return
		}
		fail(w, 400, "bad request")
		return
	}
	// encoding/json replaces invalid UTF-8 with U+FFFD without an error, which would write different bytes than the ones
	// the researcher sent; the raw body is the only place the invalid bytes are still visible.
	if !utf8.Valid(raw) {
		fail(w, 415, "the content is not valid UTF-8 text; nothing was written")
		return
	}
	var in textSave
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		fail(w, 400, "bad request")
		return
	}
	if len(in.Content) > maxTextFile {
		fail(w, 413, "that is larger than the editor's 2 MiB limit")
		return
	}
	if st, m := checkTextBytes([]byte(in.Content)); st != 0 { // FITS magic and NUL bytes; the UTF-8 case is covered above
		fail(w, st, m)
		return
	}
	if in.HostID != 0 {
		s.putHostText(w, r, in)
		return
	}
	if in.Base.Sha256 == "" || in.Base.Mtime == "" {
		fail(w, 400, "base size, mtime and sha256 of the opened file are required")
		return
	}
	// One save at a time: the re-read, the comparison and the rename must not interleave with another save from this
	// process. Another program writing the file in that window is the researcher's own race; the rename never tears.
	s.editMu.Lock()
	defer s.editMu.Unlock()
	real, fi, cur, status, msg := s.openText(in.Path) // never creates a file: it must exist, inside the roots
	if status != 0 {
		fail(w, status, msg)
		return
	}
	if v := versionOf(fi, cur); v != in.Base {
		writeJSON(w, 409, textConflict{Error: "the file changed on disk since you opened it; nothing was written", Current: asTextFile(real, fi, cur)})
		return
	}
	// Confined to the root that holds the file: a folder swapped for a link after the check above cannot redirect the write.
	pls := s.roots().Under(real)
	if len(pls) == 0 {
		fail(w, 404, "that file is not inside a folder Vesqira may read")
		return
	}
	if err := writeFileAtomic(pls[0].Root, pls[0].Rel, []byte(in.Content), fi.Mode().Perm()); err != nil {
		log.Printf("editor save: %v", err)
		fail(w, 500, "could not save the file (is its folder writable?)")
		return
	}
	nfi, err := os.Stat(real)
	if err != nil {
		fail(w, 500, "saved, but could not read the file back")
		return
	}
	nv := versionOf(nfi, []byte(in.Content))
	// hashes first and the path last: the audit detail is cut at 300 characters. Never the content.
	s.Audit.Record("file saved", "file", 0, "sha256 "+in.Base.Sha256+" -> "+nv.Sha256+" "+real)
	writeJSON(w, 200, asTextFile(real, nfi, []byte(in.Content)))
}
