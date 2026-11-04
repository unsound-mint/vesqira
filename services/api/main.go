// Command api is the Vesqira control plane (local mode: SQLite, loopback only).
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
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
	webDist := flag.String("web-dist", "", "built web app to serve on the review address")
	sshBin := flag.String("ssh", "ssh", "ssh client used to reach remote hosts (your own ssh config and agent are used; Vesqira never handles keys)")
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
	for _, f := range []string{"plane_worker.py", "fits_header.py"} {
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
	remote := hosts.NewManager(*sshBin, files)
	defer remote.Close()
	if *reviewAddr != "" {
		rv := &http.Server{Addr: *reviewAddr, Handler: (&review.Handler{Store: bs, WebDist: *webDist, ClientIPHeader: *clientIP}).Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
		if h, _, err := net.SplitHostPort(*reviewAddr); err != nil || (h != "127.0.0.1" && h != "localhost" && h != "::1") {
			log.Printf("review surface on %s is not loopback: it serves published previews to anyone holding a review link, so put TLS in front", *reviewAddr)
		}
		go func() { log.Fatal(rv.ListenAndServe()) }()
		log.Printf("review surface on http://%s (published previews only)", *reviewAddr)
	}
	planes := &worker.Persistent{Command: []string{*python, "-P", *planeScript}, Timeout: 30 * time.Second}
	defer planes.Close()
	srv := &server.Server{Roots: rs, Store: store, Worker: worker.Runner{Command: []string{*python, "-P", *script}, Timeout: 20 * time.Second}, Planes: planes, Boards: bs, Runs: rn, HostDB: hostStore, Remote: remote, Jobs: jobStore}
	if *cacheMiB > 0 {
		srv.Cache = server.NewPlaneCache(*cacheMiB << 20)
	}
	log.Printf("listening on http://%s, roots: %v", *addr, []string(rs))
	log.Fatal((&http.Server{Addr: *addr, Handler: srv.Handler(), ReadHeaderTimeout: 5 * time.Second}).ListenAndServe())
}
