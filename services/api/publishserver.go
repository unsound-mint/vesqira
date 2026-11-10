package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	_ "modernc.org/sqlite"

	"github.com/unsound-mint/vesqira/services/api/internal/publish"
)

// publishServerCommand handles `publish-server` and its add-publisher and revoke-publisher forms. It reports whether
// args named it.
func publishServerCommand(args []string) bool {
	if len(args) == 0 || args[0] != "publish-server" {
		return false
	}
	sub, rest := "", args[1:]
	if len(rest) > 0 && (rest[0] == "add-publisher" || rest[0] == "revoke-publisher") {
		sub, rest = rest[0], rest[1:]
	}
	fs := flag.NewFlagSet("publish-server "+sub, flag.ExitOnError)
	data := fs.String("data-dir", "", "data directory (publish.db and uploaded assets)")
	name := fs.String("name", "", "publisher name (add-publisher, revoke-publisher)")
	addr := fs.String("addr", "127.0.0.1:8790", "listen address; put TLS in front of anything but loopback")
	web := fs.String("web-dist", "", "built web app to serve at / (optional)")
	ipHeader := fs.String("client-ip-header", "", "header a trusted reverse proxy fills with the client address, for example X-Forwarded-For")
	_ = fs.Parse(rest)
	die := func(err error) {
		fmt.Fprintln(os.Stderr, "publish-server:", err)
		os.Exit(1)
	}
	if *data == "" || (sub != "" && *name == "") {
		fmt.Fprintln(os.Stderr, "usage: vesqira-api publish-server [add-publisher|revoke-publisher --name NAME] --data-dir DIR [--addr A] [--web-dist DIR] [--client-ip-header H]")
		os.Exit(2)
	}
	dir, err := filepath.Abs(*data)
	if err != nil {
		die(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		die(err)
	}
	dsn := "file:" + filepath.Join(dir, "publish.db") + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		die(err)
	}
	defer db.Close()
	st, err := publish.Open(db, dir)
	if err != nil {
		die(err)
	}
	switch sub {
	case "add-publisher":
		key, err := st.AddPublisher(*name)
		if err != nil {
			die(err)
		}
		fmt.Println(key)
		fmt.Fprintln(os.Stderr, "This is the publisher key for", *name+". It is shown once; only its hash is kept here. Store it in a 0600 file on the owner's instance.")
		return true
	case "revoke-publisher":
		if err := st.RevokePublisher(*name); err != nil {
			die(fmt.Errorf("no publisher named %q", *name))
		}
		fmt.Println("revoked", *name)
		return true
	}
	if host, _, err := net.SplitHostPort(*addr); err != nil || !(host == "localhost" || net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback()) {
		log.Printf("publish server on %s is not loopback: it serves reader links and accepts publishes from anyone holding a key, so put TLS in front", *addr)
	}
	srv := &http.Server{
		Addr:              *addr,
		Handler:           (&publish.Handler{Store: st, WebDist: *web, ClientIPHeader: *ipHeader}).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
	log.Printf("publish server on http://%s, data in %s", *addr, dir)
	go func() {
		if err := srv.ListenAndServe(); err != http.ErrServerClosed {
			die(err)
		}
	}()
	stop, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	<-stop.Done()
	done, release := context.WithTimeout(context.Background(), 15*time.Second)
	defer release()
	_ = srv.Shutdown(done) // let running requests finish before the deferred db.Close
	return true
}
