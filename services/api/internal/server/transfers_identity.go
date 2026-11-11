package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	idty "github.com/unsound-mint/vesqira/services/api/internal/identity"
	"github.com/unsound-mint/vesqira/services/api/internal/transfers"
)

// linkDownloadedLocation records a verified download as a location of the dataset whose registered file (or verified
// location) on that host was the source (ADR 0053). The link is "verified" only on evidence: the transfer compared a SHA-256 on
// both ends, and the dataset's own location on the host has the same full SHA-256: either recorded earlier, or recorded now
// from the transfer's hash of the source when the source still has the registered size and modification time. A source that
// changed since registration proves nothing about the dataset's bytes, so then nothing is recorded or linked (audited) and the
// copy can still be registered or added by hand.
func (s *Server) linkDownloadedLocation(ctx context.Context, t transfers.Transfer, v transfers.Verif) {
	if s.Store == nil || t.Direction != hosts.Download || t.Kind != transfers.KindFile || v.Status != transfers.Verified || v.SourceSHA256 == "" || v.SourceSHA256 != v.DestSHA256 {
		return
	}
	dsID, found, err := s.Store.LocationOwner(t.HostID, t.SourcePath)
	if err != nil || !found {
		return
	}
	locs, err := s.Store.Locations(dsID)
	if err != nil {
		return
	}
	var src *catalog.Location
	for i := range locs {
		if locs[i].HostID == t.HostID && locs[i].Path == t.SourcePath && (locs[i].Primary || locs[i].Link == catalog.LinkVerified) {
			src = &locs[i]
		}
	}
	if src == nil {
		return
	}
	if src.Full == nil {
		// The transfer just hashed the whole source. That is the dataset's hash only if the file on the host is still the one
		// that was registered: same size and modification time (the host reports seconds).
		if v.Bytes != src.SizeBytes || v.SourceMTime == 0 || v.SourceMTime != src.ModifiedAt.Unix() {
			s.Audit.Record("downloaded copy not linked", "dataset", dsID, fmt.Sprintf("transfer %d: the source on the host no longer has the size and modification time it was registered with (registered %d bytes, %s; now %d bytes, mtime %d); the registered dataset may be stale",
				t.ID, src.SizeBytes, src.ModifiedAt.UTC().Format(time.RFC3339), v.Bytes, v.SourceMTime))
			return
		}
		by := fmt.Sprintf("full sha256 computed by transfer %d", t.ID)
		if err := s.Store.SetFingerprint(src.ID, "full", v.SourceSHA256, by, src.SizeBytes, src.ModifiedAt.UnixNano()); err != nil {
			return
		}
		s.Audit.Record("source full sha256 recorded", "dataset", dsID, fmt.Sprintf("transfer %d, sha256 %s", t.ID, v.SourceSHA256[:12]))
	} else if src.Full.Value != v.SourceSHA256 {
		s.Audit.Record("downloaded copy not linked", "dataset", dsID, fmt.Sprintf("transfer %d: the dataset's recorded full SHA-256 differs from the one compared", t.ID))
		return
	}
	real, err := s.roots().Resolve(t.DestPath)
	if err != nil {
		return
	}
	info, err := statFile(real)
	if err != nil || !info.Mode().IsRegular() || info.Size() != v.Bytes {
		return
	}
	loc := catalog.Location{Path: real, Kind: catalog.LocDownloaded, Link: catalog.LinkVerified, SizeBytes: info.Size(), ModifiedAt: info.ModTime(),
		Full: &catalog.Stamp{Value: v.DestSHA256, At: time.Now(), By: fmt.Sprintf("transfer %d, %s on both ends", t.ID, v.Method)}}
	added, err := s.Store.AddLocation(dsID, loc)
	if errors.Is(err, catalog.ErrLocationExists) {
		return
	}
	if err != nil {
		log.Printf("transfers: link location: %v", err)
		return
	}
	s.storeMeasure(added, "sampled", func() (measured, int, string) { return s.measureSampled(ctx, added) })
	s.Audit.Record("downloaded copy linked", "dataset", dsID, fmt.Sprintf("transfer %d, sha256 %s", t.ID, v.DestSHA256[:12]))
}

// sameContentVerified is sameContent for a file whose full SHA-256 was just verified: equal full hashes are "verified", equal
// size and sampled fingerprint "probable", exactly as for a file registered by hand.
func (s *Server) sameContentVerified(ctx context.Context, real, sum string) []catalog.Match {
	info, err := statFile(real)
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	probe := catalog.Location{Path: real, SizeBytes: info.Size(), ModifiedAt: info.ModTime()}
	ev := idty.Evidence{Size: info.Size(), Full: sum}
	if v, code, _ := s.measureSampled(ctx, probe); code == 0 {
		ev.Sampled = v.value
	}
	got, err := s.Store.FindSame(ev, 0)
	if err != nil {
		log.Printf("identity: %v", err)
		return nil
	}
	return got
}
