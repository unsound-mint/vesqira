package hosts

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// ErrHostWorkerOld is a host whose deployed program does not know the review operations (hostrun.py answers an unknown operation with the
// plain "bad request" the older versions used). It wraps ErrInvalid: a refusal with a sentence, not an unreachable host.
var ErrHostWorkerOld = fmt.Errorf("%w: the worker deployed on this host is older than this version of Vesqira and cannot review a decomposition; deploy the current worker to it (Hosts, Check) and try again", ErrInvalid)

// HostReview runs one decomposition review on the host (ADR 0068): op is rohsa_sigmav, rohsa_cellmask or rohsa_classmaps, argv the analysis
// program's options (--input, --params, --declared-unit, --corrections; never a cache folder: the host fixes its own). The fit is read there,
// whole, by the host's own copy of the program under the same memory cap as a local review, and only the program's summary comes back. Class
// maps are the one output with files: name is their stem, and they are written in the host's derived folder, all or none, like a subset.
// timeout is the same budget a local analysis has. The program stops its pass when the request ends (cancelled, timed out, connection lost),
// because its progress line can no longer be written (hostProgram).
func (m *Manager) HostReview(ctx context.Context, h Host, op, name string, argv []string, timeout time.Duration) ([]byte, error) {
	if len(h.Roots) == 0 {
		return nil, fmt.Errorf("%w: the host has no source root", ErrInvalid)
	}
	derived := h.DerivedFolder()
	if unsafeText(derived) {
		return nil, fmt.Errorf("%w: the host's first source root has a character a remote command cannot carry", ErrInvalid)
	}
	if err := m.Deploy(ctx, h); err != nil {
		return nil, err
	}
	body := map[string]any{"op": op, "argv": argv}
	if name != "" {
		body["name"] = name
	}
	out, err := m.hostProgram(ctx, h, body, derived, true, timeout)
	if err != nil {
		return nil, err
	}
	var probe struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if json.Unmarshal(out, &probe) == nil && !probe.OK && probe.Error == "bad request" {
		return nil, ErrHostWorkerOld
	}
	return out, nil
}
