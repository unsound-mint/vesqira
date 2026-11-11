// Command api is the Vesqira control plane (local mode: SQLite, loopback only).
package main

import (
	"context"
	"flag"
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
	"github.com/unsound-mint/vesqira/services/api/internal/notify"
	"github.com/unsound-mint/vesqira/services/api/internal/projects"
	"github.com/unsound-mint/vesqira/services/api/internal/regions"
	"github.com/unsound-mint/vesqira/services/api/internal/reports"
	"github.com/unsound-mint/vesqira/services/api/internal/review"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/server"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
	"github.com/unsound-mint/vesqira/services/api/internal/sweeps"
	"github.com/unsound-mint/vesqira/services/api/internal/transfers"
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
	python := flag.String("python", "python3", "interpreter for the worker")
	planeScript := flag.String("plane-worker", "services/scientific-worker/plane_worker.py", "persistent plane worker script")
	cacheMiB := flag.Int("plane-cache-mib", 256, "plane cache size in MiB (0 disables)")
	dataDir := flag.String("data-dir", "", "directory for figure previews (default: next to the database)")
	jobWatch := flag.Duration("job-watch", server.MinWatchInterval, "how often queued and running jobs are looked up on each host (at least 1m, one call per host per interval; 0 turns it off)")
	reviewAddr := flag.String("review-addr", "", "optional address for the reviewer surface (published previews only); may be non-loopback, put TLS in front")
	clientIP := flag.String("review-client-ip-header", "", "header a trusted reverse proxy sets to the client address (for example X-Forwarded-For); without it all clients behind a proxy share one failure allowance")
	webDist := flag.String("web-dist", "", "built web app: served on this address, and on the review address when there is one")
	sshBin := flag.String("ssh", "ssh", "ssh client used to reach remote hosts (your own ssh config and agent are used; Vesqira never handles keys)")
	sshConfig := flag.String("ssh-config", "~/.ssh/config", "ssh_config read only to offer its host aliases when adding a host (empty offers none)")
	enableEditor := flag.Bool("enable-editor", false, "let the owner edit text files inside the source roots from the app (ADR 0029); off by default, never on the review address")
	enableTerminal := flag.Bool("enable-terminal", false, "let the owner open a shell on this computer or on a registered host from the app (ADR 0029); runs commands as you; off by default, never on the review address")
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
		// opening a project folder makes it readable (ADR 0030); nothing else outside the data directory is
		log.Printf("no --root given: only the data directory and project folders you open are readable")
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
	files, err := hosts.LoadWorkerFiles(filepath.Dir(*planeScript))
	if err != nil {
		log.Fatalf("remote hosts need the worker scripts: %v", err)
	}
	jobStore, err := jobs.Open(store.DB())
	if err != nil {
		log.Fatal(err)
	}
	sweepStore, err := sweeps.Open(store.DB())
	if err != nil {
		log.Fatal(err)
	}
	auditStore, err := audit.Open(store.DB())
	if err != nil {
		log.Fatal(err)
	}
	noteStore, err := notify.Open(store.DB())
	if err != nil {
		log.Fatal(err)
	}
	transferStore, err := transfers.Open(store.DB())
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
	projectStore, err := projects.Open(store.DB())
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
	srv := &server.Server{Roots: rs, Store: store, Worker: planes, Planes: planes, Boards: bs, Runs: rn, HostDB: hostStore, Remote: remote, Jobs: jobStore, Sweeps: sweepStore, Transfers: transferStore, Derived: derivedStore, ManagedDir: managedDir, CacheDir: filepath.Join(dir, "cache", "rohsa"), Audit: auditStore, Notes: noteStore, Regions: regionStore, Projects: projectStore, WebDist: *webDist, EditorEnabled: *enableEditor, TerminalEnabled: *enableTerminal, Reports: reportStore, PublishKeyFile: filepath.Join(dir, "publish.key"),
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
	go func() { // comments from the publish server, while the app runs (ADR 0036)
		for {
			_ = srv.SyncComments()
			time.Sleep(time.Minute)
		}
	}()
	if *enableEditor {
		log.Printf("editor enabled: text files inside the source roots can be changed from the app (loopback only)")
	}
	if *enableTerminal {
		log.Printf("terminal enabled: the app can open a shell as this user, here or over ssh (loopback only)")
	}
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
	if *jobWatch > 0 { // follow queued and running jobs on the hosts while the app runs (ADR 0043)
		go srv.WatchJobs(stop, *jobWatch)
	}
	transfersStopped := make(chan struct{}) // the transfer queue (ADR 0051); on shutdown rsync gets SIGTERM and keeps its partial files
	go func() {
		srv.RunTransfers(stop)
		close(transfersStopped)
	}()
	<-stop.Done()
	log.Printf("shutting down: finishing running requests")
	select {
	case <-transfersStopped:
	case <-time.After(30 * time.Second):
		log.Printf("transfers did not stop in time; they resume from their partial files at the next start")
	}
	done, release := context.WithTimeout(context.Background(), 15*time.Second)
	defer release()
	for _, s := range servers {
		if err := s.Shutdown(done); err != nil {
			log.Printf("shutdown: %v", err)
		}
	}
}
