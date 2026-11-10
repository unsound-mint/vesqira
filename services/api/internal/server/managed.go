package server

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	"github.com/unsound-mint/vesqira/services/api/internal/derived"
)

const maxManagedCopy = 64 << 30 // 64 GiB: a copy larger than this is refused rather than filling the disk

// previewFile tells the researcher what registering a file would mean, before anything is recorded: its shape,
// what the header declares about the sky and the spectral axis, and what reading it costs. Nothing is stored.
func (s *Server) previewFile(w http.ResponseWriter, r *http.Request) {
	p, _ := url.QueryUnescape(r.URL.Query().Get("path"))
	real, err := s.roots().Resolve(p)
	if err != nil {
		fail(w, 403, err.Error())
		return
	}
	hdr, err := s.Worker.Header(r.Context(), real)
	if err != nil {
		fail(w, 422, err.Error())
		return
	}
	info, err := statFile(real)
	if err != nil {
		fail(w, 404, "file not found")
		return
	}
	var h struct {
		Bitpix int `json:"bitpix"`
		Axes   []struct {
			Length int      `json:"length"`
			CType  *string  `json:"ctype"`
			CRVal  *float64 `json:"crval"`
			CRPix  *float64 `json:"crpix"`
			CDelt  *float64 `json:"cdelt"`
		} `json:"axes"`
	}
	_ = json.Unmarshal(hdr, &h)
	declared := func(i int) bool {
		return i < len(h.Axes) && h.Axes[i].CType != nil && h.Axes[i].CRVal != nil && h.Axes[i].CRPix != nil && h.Axes[i].CDelt != nil
	}
	planes, plane := 1, int64(0)
	if len(h.Axes) >= 2 {
		plane = int64(h.Axes[0].Length) * int64(h.Axes[1].Length) * 4
		for _, a := range h.Axes[2:] {
			planes *= max(a.Length, 1)
		}
	}
	writeJSON(w, 200, map[string]any{
		"name": filepath.Base(real), "sizeBytes": info.Size(), "header": json.RawMessage(hdr),
		"axes": len(h.Axes), "planes": planes, "planeBytes": plane,
		"celestialWcs": declared(0) && declared(1), "spectralAxis": len(h.Axes) > 2 && declared(2),
		"access": "A local file is read one plane at a time (about the plane size above); the cube is never loaded whole. Registering keeps a reference to this file; nothing is copied unless you ask for a managed copy.",
	})
}

// copyDataset makes a byte-for-byte managed copy of a local dataset under the data directory and registers it. The
// original stays the reference and is never touched; the copy's SHA-256 is recorded as its identity.
func (s *Server) copyDataset(w http.ResponseWriter, r *http.Request) {
	d, real, _, ok := s.source(w, r)
	if !ok {
		return
	}
	if d.HostID != 0 {
		fail(w, 409, "a file on another host is not copied here; analyse it there or keep it as a reference")
		return
	}
	if s.ManagedDir == "" || s.Derived == nil {
		fail(w, 503, "managed copies are not configured")
		return
	}
	in, err := os.Open(real)
	if err != nil {
		fail(w, 404, "file not found")
		return
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil || st.Size() > maxManagedCopy {
		fail(w, 413, "the file is too large to copy")
		return
	}
	tmp, err := os.CreateTemp(s.ManagedDir, ".copy-*")
	if err != nil {
		fail(w, 500, "internal error")
		return
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), in)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil || n != st.Size() {
		fail(w, 500, "the copy failed or the file changed while it was being copied")
		return
	}
	sum := hex.EncodeToString(h.Sum(nil))
	var b [4]byte
	_, _ = rand.Read(b[:])
	dst := filepath.Join(s.ManagedDir, fmt.Sprintf("%s-%s.fits", sum[:12], hex.EncodeToString(b[:])))
	if err := os.Rename(tmp.Name(), dst); err != nil {
		fail(w, 500, "internal error")
		return
	}
	_ = os.Chmod(dst, 0o444) // a managed copy is evidence: read-only
	nd, code, msg := s.registerPath(r.Context(), dst)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	summary, _ := json.Marshal(map[string]any{"sha256": sum, "bytes": n, "policy": "A byte-for-byte copy of the original at the time of copying. The original is untouched and remains the reference; the SHA-256 identifies this copy."})
	if err := s.Derived.Save(derived.Record{DatasetID: nd.ID, Op: "managed-copy", Params: json.RawMessage(`{}`), Sources: []int64{d.ID}, Summary: summary}); err != nil {
		log.Printf("derived: %v", err)
	}
	s.Audit.Record("managed copy made", "dataset", nd.ID, "copy of dataset "+strconv.FormatInt(d.ID, 10)+", sha256 "+sum[:12])
	writeJSON(w, 201, map[string]any{"dataset": nd, "sha256": sum})
}
