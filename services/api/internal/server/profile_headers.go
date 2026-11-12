package server

import (
	"context"
	"log"

	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// RefreshProfileHeaders re-reads, once at start, in the local single-user workspace, the stored headers that have VGT.jl's cards but no orientation reading (stored before
// ADR 0068 slice 2), so a declaration made from the file's header does not turn stale and a map does not read as unresolved only because
// it was registered earlier. A file is read again only when its size and modification time are what was recorded: a file that changed
// or went missing keeps its stored header, and registering it again is the person's decision. Returns how many headers were replaced.
func (s *Server) RefreshProfileHeaders(ctx context.Context) int {
	list, err := s.Store.PreProfileHeaders(workspace.Local)
	if err != nil {
		log.Printf("refresh profile headers: %v", err)
		return 0
	}
	n := 0
	for _, h := range list {
		if ctx.Err() != nil {
			break
		}
		info, err := statFile(h.Path)
		if err != nil || info.Size() != h.SizeBytes || info.ModTime().UnixNano() != h.ModifiedAt {
			continue
		}
		hdr, err := s.Worker.Header(ctx, h.Path)
		if err != nil {
			continue
		}
		if s.Store.ReplaceHeader(workspace.Local, h.ID, hdr) == nil {
			n++
		}
	}
	return n
}
