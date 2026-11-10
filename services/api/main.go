// Command api is the Vesqira control plane (local mode: SQLite, loopback only).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/audit"
	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/derived"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/regions"
	"github.com/unsound-mint/vesqira/services/api/internal/reports"
	"github.com/unsound-mint/vesqira/services/api/internal/review"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/server"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	if archiveCommand(os.Args[1:]) || publishServerCommand(os.Args[1:]) || agentCommand(os.Args[1:]) {
		return
	}
	var roots multi
	addr := flag.String("addr", "127.0.0.1:8787", "listen address (must be a loopback address)")
	db := flag.String("db", "vesqira.db", "SQLite database path")
	script := flag.String("worker", "services/scientific-worker/fits_header.py", "header worker script")
	python := flag.String("python", "python3", "interpreter for the worker")
	planeScript := flag.String("plane-worker", "services/scientific-worker/plane_worker.py", "persistent plane worker script")
	cacheMiB := flag.Int("plane-cache-mib", 256, "plane cache size in MiB (0 disables)")
	dataDir := flag.String("data-dir", "", "directory for figure previews (default: next to the database)")
	reviewAddr := flag.String("review-addr", "", "optional address for the reviewer surface (published previews only); may be non-loopback, put TLS in front")
	clientIP := flag.String("review-client-ip-header", "", "header a trusted reverse proxy sets to the client address (for example X-Forwarded-For); without it all clients behind a proxy share one failure allowance")
	webDist := flag.String("web-dist", "", "built web app: served on this address, and on the review address when there is one")
	sshBin := flag.String("ssh", "ssh", "ssh client used to reach remote hosts (your own ssh config and agent are used; Vesqira never handles keys)")
	sshConfig := flag.String("ssh-config", "~/.ssh/config", "ssh_config read only to offer its host aliases when adding a host (empty offers none)")
	rootsFile := flag.String("roots-file", "", "file that remembers folders opened from the app, one per line (the launcher passes its roots file)")
	flag.Var(&roots, "root", "source root directory (repeatable, required)")
	flag.Parse()

	// Resolve the interpreter once, in this process's environment, so the workers (which run with a minimal PATH)
	// use the same Python, and the same astropy, as the one the operator started the API with.
	if p, err := exec.LookPath(*python); err == nil {
		*python = p
	}
	host, _, err := net.SplitHostPort(*addr)
	if err != nil || (host != "127.0.0.1" && host != "localhost" && host != "::1") {
		log.Fatalf("refusing to listen on %q: local mode binds to loopback only", *addr)
	}
	if len(roots) == 0 {
		fmt.Fprintln(os.Stderr, "at least one --root is required: files outside source roots are never read")
		os.Exit(2)
	}
	// derived results (moment maps, cutouts, aligned cubes) are written under the data directory, which is a source root
	ddir := *dataDir
	if ddir == "" {
		ddir = filepath.Dir(strings.TrimPrefix(*db, "file:"))
	}
	derivedDir := filepath.Join(ddir, "derived")
	if err := os.MkdirAll(derivedDir, 0o700); err != nil {
		log.Fatal(err)
	}
	managedDir := filepath.Join(ddir, "managed")
	if err := os.MkdirAll(managedDir, 0o700); err != nil {
		log.Fatal(err)
	}
	roots = append(roots, derivedDir, managedDir)
	rs, err := sourceroot.New(roots)
	if err != nil {
		log.Fatal(err)
	}
	store, err := catalog.Open(*db)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()

	dir := *dataDir
	if dir == "" {
		dir = filepath.Dir(strings.TrimPrefix(*db, "file:"))
	}
	bs, err := boards.Open(store.DB(), dir)
	if err != nil {
		log.Fatal(err)
	}
	rn, err := runs.Open(store.DB())
	if err != nil {
		log.Fatal(err)
	}
	hostStore, err := hosts.Open(store.DB())
	if err != nil {
		log.Fatal(err)
	}
	files := map[string][]byte{}
	for _, f := range []string{"plane_worker.py", "fits_header.py", "analysis.py"} {
		b, err := os.ReadFile(filepath.Join(filepath.Dir(*planeScript), f))
		if err != nil {
			log.Fatalf("remote hosts need the worker scripts: %v", err)
		}
		files[f] = b
	}
	jobStore, err := jobs.Open(store.DB())
	if err != nil {
		log.Fatal(err)
	}
	auditStore, err := audit.Open(store.DB())
	if err != nil {
		log.Fatal(err)
	}
	regionStore, err := regions.Open(store.DB())
	if err != nil {
		log.Fatal(err)
	}
	derivedStore, err := derived.Open(store.DB())
	if err != nil {
		log.Fatal(err)
	}
	reportStore, err := reports.Open(store.DB(), dir)
	if err != nil {
		log.Fatal(err)
	}
	remote := hosts.NewManager(*sshBin, files)
	defer remote.Close()
	var servers []*http.Server
	if *reviewAddr != "" {
		rv := &http.Server{Addr: *reviewAddr, Handler: (&review.Handler{Store: bs, WebDist: *webDist, ClientIPHeader: *clientIP}).Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
		if h, _, err := net.SplitHostPort(*reviewAddr); err != nil || (h != "127.0.0.1" && h != "localhost" && h != "::1") {
			log.Printf("review surface on %s is not loopback: it serves published previews to anyone holding a review link, so put TLS in front", *reviewAddr)
		}
		servers = append(servers, rv)
		go func() {
			if err := rv.ListenAndServe(); err != http.ErrServerClosed {
				log.Fatal(err)
			}
		}()
		log.Printf("review surface on http://%s (published previews only)", *reviewAddr)
	}
	planes := &worker.Persistent{Command: []string{*python, "-P", *planeScript}, Timeout: 30 * time.Second}
	defer planes.Close()
	srv := &server.Server{Roots: rs, RootsFile: *rootsFile, Store: store, Worker: worker.Runner{Command: []string{*python, "-P", *script}, Timeout: 20 * time.Second}, Planes: planes, Boards: bs, Runs: rn, HostDB: hostStore, Remote: remote, Jobs: jobStore, Derived: derivedStore, ManagedDir: managedDir, Audit: auditStore, Regions: regionStore, WebDist: *webDist, Reports: reportStore, PublishKeyFile: filepath.Join(dir, "publish.key"),
		Analysis: &server.AnalysisConfig{Python: *python, Script: filepath.Join(filepath.Dir(*planeScript), "analysis.py"), DerivedDir: derivedDir, Timeout: 30 * time.Minute}}
	if *cacheMiB > 0 {
		srv.Cache = server.NewPlaneCache(*cacheMiB << 20)
	}
	if p := *sshConfig; p != "" { // only to offer host names that already work with ssh
		if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, "~/") {
			p = filepath.Join(home, p[2:])
		}
		srv.SSHConfig = p
	}
	go func() { // comments from the publish server, while the app runs (ADR 0030)
		for {
			_ = srv.SyncComments()
			time.Sleep(time.Minute)
		}
	}()
	log.Printf("listening on http://%s, roots: %v", *addr, []string(rs))
	// No WriteTimeout: a cube analysis answers one request for up to its own 30-minute budget. Reading a request is
	// bounded, and so is an idle connection.
	owner := &http.Server{Addr: *addr, Handler: srv.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 16 << 10}
	servers = append(servers, owner)
	go func() {
		if err := owner.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	// On SIGINT/SIGTERM stop taking requests and let running ones finish (bounded), so a write to the catalog or a
	// copy in progress is not cut off mid-way. The deferred Closes above then run.
	stop, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	<-stop.Done()
	log.Printf("shutting down: finishing running requests")
	done, release := context.WithTimeout(context.Background(), 15*time.Second)
	defer release()
	for _, s := range servers {
		if err := s.Shutdown(done); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}
}
