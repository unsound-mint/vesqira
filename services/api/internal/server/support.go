package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/schema"
	"github.com/unsound-mint/vesqira/services/api/internal/support"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// The support bundle (ADR 0056). Owner listener only. A bundle is built once by the preview route, kept in memory for ten
// minutes, and the download returns exactly the bytes whose files were previewed: what the person read is what is saved.

type supportFileView struct {
	Name  string `json:"name"`
	Bytes int    `json:"bytes"`
	Text  string `json:"text"`
}

// SupportPreview is the body of POST /api/support/preview.
type SupportPreview struct {
	ID    string            `json:"id"`
	Files []supportFileView `json:"files"`
	Bytes int               `json:"bytes"`
}

type supportKept struct {
	zip     []byte
	expires time.Time
}

type supportStore struct {
	mu   sync.Mutex
	kept map[string]supportKept
}

const supportKeep = 10 * time.Minute

func (st *supportStore) put(z []byte) string {
	st.mu.Lock()
	defer st.mu.Unlock()
	now := time.Now()
	if st.kept == nil {
		st.kept = map[string]supportKept{}
	}
	for k, v := range st.kept {
		if now.After(v.expires) {
			delete(st.kept, k)
		}
	}
	if len(st.kept) >= 4 { // a preview is cheap to make again
		for k := range st.kept {
			delete(st.kept, k)
			break
		}
	}
	var b [12]byte
	_, _ = rand.Read(b[:])
	id := hex.EncodeToString(b[:])
	st.kept[id] = supportKept{zip: z, expires: now.Add(supportKeep)}
	return id
}

func (st *supportStore) get(id string) ([]byte, bool) {
	st.mu.Lock()
	defer st.mu.Unlock()
	v, ok := st.kept[id]
	if !ok || time.Now().After(v.expires) {
		return nil, false
	}
	return v.zip, true
}

func (s *Server) supportPreview(w http.ResponseWriter, r *http.Request) {
	var in struct {
		IncludeDatasetNames bool `json:"includeDatasetNames"`
	}
	if r.ContentLength != 0 && !body(w, r, 1<<10, &in, true) {
		return
	}
	b := support.Build(s.supportInput(r.Context(), wsOf(r), in.IncludeDatasetNames))
	z, err := b.Zip()
	if err != nil {
		fail(w, 500, "could not build the bundle")
		return
	}
	out := SupportPreview{ID: s.support.put(z), Files: []supportFileView{}}
	for _, f := range b.Files {
		out.Files = append(out.Files, supportFileView{Name: f.Name, Bytes: len(f.Data), Text: string(f.Data)})
		out.Bytes += len(f.Data)
	}
	writeJSON(w, 200, out)
}

func (s *Server) supportDownload(w http.ResponseWriter, r *http.Request) {
	z, ok := s.support.get(r.PathValue("id"))
	if !ok {
		fail(w, 404, "this preview has expired: create the bundle again")
		return
	}
	s.Audit.RecordCtx(r.Context(), "support.bundle", "support", 0, "")
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="vesqira-support.zip"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(z)
}

// supportInput gathers the bundle's inputs. Everything is read-only and bounded; a part that cannot be read is left empty.
func (s *Server) supportInput(ctx context.Context, ws workspace.ID, names bool) support.Input {
	home, _ := os.UserHomeDir()
	in := support.Input{
		Now: time.Now(), AppVersion: Version, Protocol: ProtocolVersion, Schema: schema.Version,
		Capabilities: s.currentCapabilities(), OS: runtime.GOOS, Arch: runtime.GOARCH, GoVersion: runtime.Version(),
		APILog: apiLogTail.Lines(), WorkerLog: worker.StderrTail.Lines(), Home: home, IncludeDatasetNames: names,
		TableCounts: map[string]int64{}, JobStates: map[string]int64{},
	}
	for _, f := range []string{s.PublishKeyFile, s.GroupKeyFile} { // the publisher key and the group server's push token
		if f == "" {
			continue
		}
		if k, err := readKey(f); err == nil {
			in.Secrets = append(in.Secrets, k)
		}
	}
	if s.Analysis != nil && s.Analysis.Python != "" {
		in.Python = pythonVersions(ctx, s.Analysis.Python)
	}
	if ds, err := s.Store.List(wsCtx(ctx)); err == nil {
		for _, d := range ds {
			in.DatasetNames = append(in.DatasetNames, d.Name, filepath.Base(d.Path))
			for _, l := range d.Locations {
				in.DatasetNames = append(in.DatasetNames, filepath.Base(l.Path))
			}
		}
	}
	if db := s.Store.DB(); db != nil {
		if rows, err := db.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`); err == nil {
			var tables []string
			for rows.Next() {
				var n string
				if rows.Scan(&n) == nil {
					tables = append(tables, n)
				}
			}
			rows.Close()
			for _, n := range tables {
				var c int64
				if db.QueryRowContext(ctx, `SELECT COUNT(*) FROM "`+strings.ReplaceAll(n, `"`, `""`)+`"`).Scan(&c) == nil {
					in.TableCounts[n] = c
				}
			}
		}
		if rows, err := db.QueryContext(ctx, `SELECT state, COUNT(*) FROM jobs GROUP BY state`); err == nil {
			for rows.Next() {
				var st string
				var c int64
				if rows.Scan(&st, &c) == nil {
					in.JobStates[st] = c
				}
			}
			rows.Close()
		}
		if rows, err := db.QueryContext(ctx, `SELECT at, action, kind FROM audit WHERE workspace_id=? ORDER BY id DESC LIMIT ?`, ws, support.MaxAuditLines); err == nil {
			for rows.Next() {
				var at int64
				var e support.AuditEntry
				if rows.Scan(&at, &e.Action, &e.Kind) == nil {
					e.At = time.Unix(at, 0)
					in.Audit = append(in.Audit, e)
				}
			}
			rows.Close()
		}
	}
	if s.HostDB != nil {
		if hs, err := s.HostDB.List(); err == nil {
			for _, h := range hs {
				up, _ := s.reach.get(h.ID)
				in.Hosts = append(in.Hosts, support.Host{Alias: h.Name, Reachable: up})
			}
		}
	}
	return in
}

// pythonVersions asks the worker's interpreter for its own versions, with the workers' minimal environment and a short limit.
func pythonVersions(ctx context.Context, python string) map[string]string {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, python, "-I", "-c", `import sys
print(sys.version.split()[0])
for m in ("astropy", "numpy"):
    try:
        print(__import__(m).__version__)
    except Exception:
        print("not installed")`)
	cmd.Env = worker.BaseEnv()
	out, err := cmd.Output()
	if err != nil {
		return map[string]string{"error": "the worker's Python could not be run"}
	}
	f := strings.Fields(strings.ReplaceAll(string(out), "not installed", "not-installed"))
	if len(f) != 3 {
		return nil
	}
	return map[string]string{"python": f[0], "astropy": strings.ReplaceAll(f[1], "not-installed", "not installed"), "numpy": strings.ReplaceAll(f[2], "not-installed", "not installed")}
}
