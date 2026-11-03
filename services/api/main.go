// Command api is the Vesqira control plane (local mode: SQLite, loopback only).
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
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
	flag.Var(&roots, "root", "source root directory (repeatable, required)")
	flag.Parse()

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

	srv := &server.Server{Roots: rs, Store: store, Worker: worker.Runner{Command: []string{*python, "-P", *script}, Timeout: 20 * time.Second}}
	log.Printf("listening on http://%s, roots: %v", *addr, []string(rs))
	log.Fatal((&http.Server{Addr: *addr, Handler: srv.Handler(), ReadHeaderTimeout: 5 * time.Second}).ListenAndServe())
}
