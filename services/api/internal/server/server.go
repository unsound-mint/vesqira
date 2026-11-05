// Package server exposes the catalog over a small JSON API.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/unsound-mint/vesqira/services/api/internal/audit"
	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/derived"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/regions"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
)

type HeaderReader interface {
	Header(ctx context.Context, path string) (json.RawMessage, error)
}

type Server struct {
	Roots      sourceroot.Roots
	Store      *catalog.Store
	Worker     HeaderReader
	Planes     PlaneService // persistent plane worker; nil answers 503
	Cache      *PlaneCache  // optional; nil disables caching
	Boards     *boards.Store
	Runs       *runs.Store
	HostDB     *hosts.Store // remote hosts; nil disables them
	Remote     *hosts.Manager
	Jobs       *jobs.Store
	Derived    *derived.Store
	ManagedDir string
	Analysis   *AnalysisConfig
	Audit      *audit.Store // nil records nothing
	Regions    *regions.Store
	SSHConfig  string     // the user's ssh_config, only read to offer host aliases; empty offers none
	Hints      hintSource // overrides the catalog for the runs list's input hints (tests count calls)
	WebDist    string     // built web app served at / on this (loopback) listener; empty serves the API only

	progress progressCache // what job logs said about progress, for board panels that follow a job
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/capabilities", s.capabilities)
	mux.HandleFunc("GET /api/roots", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]any{"roots": s.Roots}) })
	mux.HandleFunc("GET /api/datasets", s.list)
	mux.HandleFunc("GET /api/datasets/availability", s.availability)
	mux.HandleFunc("GET /api/datasets/{id}", s.get)
	mux.HandleFunc("POST /api/datasets", s.register)
	if s.HostDB != nil && s.Remote != nil {
		mux.HandleFunc("GET /api/hosts", s.listHosts)
		mux.HandleFunc("GET /api/hosts/ssh-config", s.sshConfigHosts)
		mux.HandleFunc("POST /api/hosts", s.createHost)
		mux.HandleFunc("POST /api/hosts/{id}/check", s.checkHost)
		mux.HandleFunc("POST /api/hosts/{id}/delete", s.deleteHost)
		mux.HandleFunc("GET /api/hosts/{id}/list", s.listRemote)
		mux.HandleFunc("POST /api/hosts/{id}/register", s.registerRemote)
		mux.HandleFunc("GET /api/hosts/{id}/text", s.hostText)
		if s.Jobs != nil {
			mux.HandleFunc("GET /api/jobs", s.listJobs)
			mux.HandleFunc("POST /api/jobs", s.createJob)
			mux.HandleFunc("GET /api/jobs/{id}", s.getJob)
			mux.HandleFunc("POST /api/jobs/{id}/script", s.editJobScript)
			mux.HandleFunc("POST /api/jobs/{id}/submit", s.submitJob)
			mux.HandleFunc("POST /api/jobs/{id}/refresh", s.refreshJob)
			mux.HandleFunc("POST /api/jobs/{id}/reopen", s.reopenJob)
			mux.HandleFunc("POST /api/jobs/{id}/cancel", s.cancelJob)
			mux.HandleFunc("GET /api/jobs/{id}/log", s.jobLog)
			mux.HandleFunc("POST /api/jobs/{id}/register", s.registerJobOutputs)
			mux.HandleFunc("GET /api/jobs/{id}/result", s.jobResult)
		}
	}
	if s.analysisDeps() {
		mux.HandleFunc("POST /api/analysis", s.analyze)
		mux.HandleFunc("GET /api/datasets/{id}/derived", s.getDerived)
		if s.HostDB != nil && s.Remote != nil && s.Jobs != nil {
			mux.HandleFunc("POST /api/analysis/job", s.analysisJob)
		}
	}
	if s.Runs != nil {
		mux.HandleFunc("POST /api/runs/import", s.importRun)
		mux.HandleFunc("GET /api/runs", s.listRuns)
		mux.HandleFunc("GET /api/runs/{id}", s.getRun)
		mux.HandleFunc("GET /api/runs/{id}/rerun", s.rerunRun)
		mux.HandleFunc("POST /api/runs/{id}/review", s.setRunReview)
		mux.HandleFunc("GET /api/runs/{id}/history", s.runHistory)
	}
	if s.Boards != nil {
		mux.HandleFunc("POST /api/figures", s.createFigure)
		mux.HandleFunc("GET /api/figures", s.listFigures)
		mux.HandleFunc("GET /api/figures/{id}/preview", s.figurePreview)
		mux.HandleFunc("GET /api/figures/{id}/download", s.figureDownload)
		mux.HandleFunc("POST /api/figures/{id}/text", s.figureText)
		mux.HandleFunc("GET /api/boards", s.listBoards)
		mux.HandleFunc("POST /api/boards", s.createBoard)
		mux.HandleFunc("GET /api/boards/{id}", s.getBoard)
		mux.HandleFunc("POST /api/boards/{id}/items", s.boardAdd)
		mux.HandleFunc("POST /api/boards/{id}/items/{pos}/remove", s.boardItem("remove"))
		mux.HandleFunc("POST /api/boards/{id}/items/{pos}/move", s.boardItem("move"))
		mux.HandleFunc("POST /api/boards/{id}/items/{pos}/note", s.boardItem("note"))
		mux.HandleFunc("POST /api/boards/{id}/items/{pos}/pin", s.boardPin)
		mux.HandleFunc("GET /api/boards/{id}/preflight", s.preflight)
		mux.HandleFunc("POST /api/boards/{id}/publish", s.publish)
		mux.HandleFunc("GET /api/boards/{id}/snapshots", s.snapshots)
		mux.HandleFunc("POST /api/snapshots/{id}/revoke", s.revoke)
		mux.HandleFunc("GET /api/snapshots/{id}/comments", s.snapshotComments)
		mux.HandleFunc("POST /api/snapshots/{id}/comments", s.ownerReply)
		mux.HandleFunc("POST /api/comments/{id}/resolve", s.resolveComment)
	}
	mux.HandleFunc("GET /api/datasets/{id}/header-cards", s.headerCards)
	mux.HandleFunc("GET /api/datasets/{id}/view", s.view)
	mux.HandleFunc("GET /api/datasets/{id}/plane", s.plane)
	mux.HandleFunc("GET /api/datasets/{id}/coord", s.coord)
	mux.HandleFunc("GET /api/datasets/{id}/spectrum", s.spectrum)
	mux.HandleFunc("GET /api/datasets/{id}/axial", s.axial)
	if s.Regions != nil {
		mux.HandleFunc("GET /api/datasets/{id}/regions", s.listRegions)
		mux.HandleFunc("POST /api/datasets/{id}/regions", s.createRegion)
		mux.HandleFunc("GET /api/datasets/{id}/regions.reg", s.regionsDS9)
		mux.HandleFunc("POST /api/datasets/{id}/regions/import", s.importRegions)
		mux.HandleFunc("POST /api/regions/{id}", s.updateRegion)
		mux.HandleFunc("POST /api/regions/{id}/delete", s.deleteRegion)
	}
	mux.HandleFunc("GET /api/compat", s.compatibility)
	if s.Audit != nil {
		mux.HandleFunc("GET /api/audit", s.listAudit)
	}
	mux.HandleFunc("GET /api/preview", s.previewFile)
	mux.HandleFunc("POST /api/datasets/{id}/copy", s.copyDataset)
	mux.HandleFunc("POST /api/datasets/{id}/nature", s.setNature)
	mux.HandleFunc("POST /api/datasets/{id}/interpretation", s.interpret)
	if s.WebDist != "" {
		mux.Handle("GET /", staticApp(s.WebDist))
	}
	return logRequests(localOnly(mux))
}

// staticApp serves the built web app for the installed program, so one address is the whole application. Paths
// that are not files fall back to index.html (the app routes in the browser); /api is never answered from disk.
func staticApp(dist string) http.Handler {
	fs := http.FileServer(http.Dir(dist))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api" || strings.HasPrefix(r.URL.Path, "/api/") {
			fail(w, 404, "not found")
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if st, err := os.Stat(filepath.Join(dist, filepath.Clean("/"+r.URL.Path))); (err != nil || !st.Mode().IsRegular()) && r.URL.Path != "/" {
			http.ServeFile(w, r, filepath.Join(dist, "index.html"))
			return
		}
		fs.ServeHTTP(w, r)
	})
}

// localOnly rejects requests whose Host is not a loopback name, which blocks DNS-rebinding
// pages from reaching the local API, and rejects cross-site browser writes.
func localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if i := lastColon(host); i >= 0 {
			host = host[:i]
		}
		if host != "127.0.0.1" && host != "localhost" && host != "[::1]" {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		// A page on another site must not make the API read files or run the worker, even by GET.
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
			http.Error(w, "cross-site request refused", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost {
			if ct := r.Header.Get("Content-Type"); ct != "application/json" {
				http.Error(w, "content type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func lastColon(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ':' {
			if s[len(s)-1] == ']' {
				return -1
			}
			return i
		}
	}
	return -1
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	ds, err := s.Store.List()
	if err != nil {
		log.Printf("list: %v", err)
		fail(w, 500, "internal error")
		return
	}
	writeJSON(w, 200, map[string]any{"datasets": ds})
}

// availability says, for each registered file on this computer, whether it is still what was registered: available, modified
// (size or modification time differ, so the stored header may be out of date), missing, or outside the source roots. Files on
// other hosts are not contacted here (that would wait on the network and the VPN) and are reported as unchecked. Only datasets
// that are not simply available are listed.
func (s *Server) availability(w http.ResponseWriter, r *http.Request) {
	ds, err := s.Store.List()
	if err != nil {
		log.Printf("availability: %v", err)
		fail(w, 500, "internal error")
		return
	}
	out := map[string]string{}
	for _, d := range ds {
		if d.HostID != 0 {
			continue
		}
		switch _, _, status, _ := s.authorize(d); status {
		case 0:
		case 404:
			out[strconv.FormatInt(d.ID, 10)] = "missing"
		case 409:
			out[strconv.FormatInt(d.ID, 10)] = "modified"
		default:
			if _, err := os.Lstat(d.Path); errors.Is(err, fs.ErrNotExist) { // the roots check cannot resolve a path that is gone
				out[strconv.FormatInt(d.ID, 10)] = "missing"
			} else {
				out[strconv.FormatInt(d.ID, 10)] = "not_allowed"
			}
		}
	}
	writeJSON(w, 200, map[string]any{"problems": out, "note": "remote files are not checked here"})
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, 400, "bad id")
		return
	}
	d, err := s.Store.Get(id)
	if errors.Is(err, catalog.ErrNotFound) {
		fail(w, 404, "dataset not found")
		return
	}
	if err != nil {
		log.Printf("get: %v", err)
		fail(w, 500, "internal error")
		return
	}
	writeJSON(w, 200, d)
}

// registerPath reads the header of an already-authorized file and records the reference.
func (s *Server) registerPath(ctx context.Context, real string) (catalog.Dataset, int, string) {
	hdr, err := s.Worker.Header(ctx, real)
	if err != nil {
		return catalog.Dataset{}, 422, err.Error()
	}
	info, err := statFile(real)
	if err != nil {
		return catalog.Dataset{}, 404, "file not found"
	}
	d, err := s.Store.Upsert(catalog.Dataset{Path: real, Name: filepath.Base(real), SizeBytes: info.Size(), ModifiedAt: info.ModTime(), Header: hdr})
	if err != nil {
		log.Printf("register: %v", err)
		return catalog.Dataset{}, 500, "internal error"
	}
	return d, 0, ""
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var in struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, 400, "invalid request body")
		return
	}
	real, err := s.Roots.Resolve(in.Path)
	if err != nil {
		fail(w, 403, err.Error())
		return
	}
	d, code, msg := s.registerPath(r.Context(), real)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	writeJSON(w, 201, d)
}
