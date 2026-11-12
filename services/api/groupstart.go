package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/accounts"
	"github.com/unsound-mint/vesqira/services/api/internal/authz"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/server"
)

// groupStart holds what --multi-user is started with; check refuses a configuration ADR 0050 section 1 rules out, before
// anything is opened.
type groupStart struct {
	origin                  string
	insecureDev             bool
	editor, terminal, vpn   bool
	addr, reviewAddr, dbDir string
	onNetworkFS             func(dir string) (bool, string)
	allowOthers             bool
}

// check returns the normalised public origin (scheme://host[:port]) or the reason the server must not start.
func (g groupStart) check() (string, error) {
	if g.editor || g.terminal || g.vpn {
		return "", errors.New("--enable-editor, --enable-terminal and --enable-vpn run things as the server's account: they are never available with --multi-user")
	}
	if g.allowOthers {
		return "", errors.New("--allow-other-local-users is a local-mode flag; the group server has accounts")
	}
	u, err := url.Parse(g.origin)
	if g.origin == "" || err != nil || u.Host == "" || u.Path != "" && u.Path != "/" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", errors.New("--multi-user needs --public-origin https://host (the address people open, with no path)")
	}
	switch u.Scheme {
	case "https":
	case "http":
		h := u.Hostname()
		if !g.insecureDev || (h != "localhost" && h != "127.0.0.1" && h != "::1") {
			return "", errors.New("--public-origin must be https (put a TLS reverse proxy in front that passes Host through unchanged); --insecure-dev allows http://localhost for development only")
		}
	default:
		return "", fmt.Errorf("--public-origin %q is not an http(s) origin", g.origin)
	}
	if g.reviewAddr != "" && sameAddr(g.reviewAddr, g.addr) {
		return "", errors.New("--review-addr must not be the group server's own address")
	}
	if g.onNetworkFS != nil {
		if remote, fs := g.onNetworkFS(g.dbDir); remote {
			return "", fmt.Errorf("the catalog is on a network file system (%s): SQLite locking is not safe there; put it on a local disk", fs)
		}
	}
	host := strings.ToLower(u.Host)
	// a browser leaves the default port out of Origin and Host, so the origin compared against them must too
	if p := u.Port(); (u.Scheme == "https" && p == "443") || (u.Scheme == "http" && p == "80") {
		host = strings.TrimSuffix(host, ":"+p)
	}
	return u.Scheme + "://" + host, nil
}

func sameAddr(a, b string) bool {
	ha, pa, err1 := net.SplitHostPort(a)
	hb, pb, err2 := net.SplitHostPort(b)
	if err1 != nil || err2 != nil {
		return a == b
	}
	wild := func(h string) bool { return h == "" || h == "0.0.0.0" || h == "::" }
	return pa == pb && (ha == hb || wild(ha) || wild(hb))
}

// openGroup opens what the group server needs beside the catalog: the account store with its server key, a read-only pool
// for the per-request session and token lookups, and the writer lock-wait log (ADR 0050 section 6). Any failure stops the start.
func openGroup(store *catalog.Store, dsn, dataDir, keyPath, origin, clientIPHeader string) (server.MultiUser, func()) {
	acc, err := accounts.Open(store.DB())
	if err != nil {
		log.Fatal(err)
	}
	if keyPath == "" {
		keyPath = filepath.Join(dataDir, "secrets", "server.key")
	}
	key, err := accounts.LoadOrCreateKey(keyPath)
	if err != nil {
		log.Fatalf("server key: %v", err)
	}
	acc.SetKey(key)
	for _, sc := range authz.Scopes() {
		acc.KnownScopes = append(acc.KnownScopes, string(sc))
	}
	ro, err := catalog.OpenReadPool(dsn, 8)
	if err != nil {
		log.Fatalf("read-only pool: %v", err)
	}
	acc.UseReadPool(ro)
	ctx, stop := context.WithCancel(context.Background())
	go catalog.WriterWaits(ctx, store.DB(), time.Minute, func(n int64, total time.Duration) {
		log.Printf("catalog writer lock: %d waits in the last minute, %s in total, %s on average", n, total.Round(time.Microsecond), (total / time.Duration(n)).Round(time.Microsecond))
	})
	if has, err := acc.HasAdmin(); err == nil && !has {
		log.Printf("no admin account yet: run `vesqira-api admin bootstrap --db %s --public-origin %s` on this machine", dsn, origin)
	}
	return server.MultiUser{PublicOrigin: origin, Accounts: acc, Limiter: &accounts.Limiter{}, ClientIPHeader: clientIPHeader}, func() {
		stop()
		ro.Close()
	}
}
