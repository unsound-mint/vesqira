package server

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/derived"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/transfers"
)

// registerTransfer registers a downloaded file as a dataset. Only a file whose copy was verified (the same sha256 on both
// ends) is offered: registering is how a copy becomes evidence, and an unverified one has not earned that. The dataset keeps a
// derived record that names the host and path as an external reference and the hash that was compared; the file on the host
// may change or disappear, and the record says so (AGENTS.md: a path does not prove identity). The derived record's
// `policy` is the only part that travels with a figure, so no host path leaves this computer through it.
func (s *Server) registerTransfer(w http.ResponseWriter, r *http.Request) {
	t, ok := s.transferFrom(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<10)
	var in struct {
		// AsNew registers the copy as its own dataset although a dataset already holds the same (or probably the same) bytes.
		AsNew bool `json:"asNew"`
	}
	if body, _ := io.ReadAll(r.Body); len(body) > 0 {
		if err := json.Unmarshal(body, &in); err != nil {
			fail(w, 400, "invalid request body")
			return
		}
	}
	switch {
	case t.Direction != hosts.Download || t.Kind != transfers.KindFile:
		fail(w, 409, "only a downloaded file can be registered as a dataset")
		return
	case t.State != transfers.StateDone || t.Verif.Status != transfers.Verified:
		fail(w, 409, "only a verified download can be registered; this one has not been verified")
		return
	}
	if s.Store == nil || s.Worker == nil {
		fail(w, 503, "registering datasets is not configured")
		return
	}
	if t.DatasetID != nil {
		if d, err := s.Store.Get(*t.DatasetID); err == nil {
			writeJSON(w, 200, map[string]any{"dataset": d, "transfer": t})
			return
		}
	}
	real, err := s.roots().Resolve(t.DestPath)
	if err != nil {
		fail(w, 403, "the downloaded file is no longer inside the source roots on this computer")
		return
	}
	// the bytes on disk must still be the ones that were verified
	sum, n, err := hosts.HashFile(r.Context(), real)
	if err != nil || sum != t.Verif.DestSHA256 || n != t.Verif.Bytes {
		fail(w, 409, "the file on this computer changed since it was verified; copy it again before registering it")
		return
	}
	if !in.AsNew && !s.knownFile(real) {
		if m := s.sameContentVerified(r.Context(), real, sum); len(m) > 0 {
			failCode(w, 409, "same_content", "A dataset already holds this content. Add the file as another location of it, or register it as a separate dataset.", map[string]any{"matches": m})
			return
		}
	}
	d, code, msg := s.registerPath(waiting(r.Context()), real)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	s.finishTransferRegistration(w, t, d, sum, n)
}

// verifiedAt is when the hashes were compared (the transfer finished then), not when the dataset was registered.
func verifiedAt(t transfers.Transfer) string {
	if t.FinishedAt == nil {
		return ""
	}
	return t.FinishedAt.UTC().Format(time.RFC3339)
}

func (s *Server) finishTransferRegistration(w http.ResponseWriter, t transfers.Transfer, d catalog.Dataset, sum string, n int64) {
	hostName := ""
	if h, err := s.HostDB.Get(t.HostID); err == nil {
		hostName = h.Name
	}
	if s.Derived != nil {
		summary, _ := json.Marshal(map[string]any{
			"policy":   "A copy of a file that was on another host, downloaded by Vesqira. The copy and the host's file had the same SHA-256 when it was checked; the file on the host is an external reference and may have changed or been removed since.",
			"host":     hostName,
			"path":     t.SourcePath,
			"sha256":   sum,
			"bytes":    n,
			"method":   t.Verif.Method,
			"verified": verifiedAt(t),
			"status":   "external reference; verified at transfer time",
		})
		if err := s.Derived.Save(derived.Record{DatasetID: d.ID, Op: "transfer", Params: json.RawMessage(`{}`), Summary: summary}); err != nil {
			log.Printf("derived: %v", err)
		}
	}
	if err := s.Transfers.LinkDataset(t.ID, d.ID); err != nil && !errors.Is(err, transfers.ErrNotFound) {
		log.Printf("transfers: %v", err)
	}
	s.Audit.Record("dataset registered from transfer", "dataset", d.ID, "transfer "+strconv.FormatInt(t.ID, 10)+", sha256 "+sum[:12])
	out, _ := s.Transfers.Get(t.ID)
	writeJSON(w, 201, map[string]any{"dataset": d, "transfer": out})
}
