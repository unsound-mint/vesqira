package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
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

// proxyWarning is the start-up warning for an https origin served from a loopback or private address (behind a proxy) with no
// client-address header: sign-in failures are then limited per name only, not per client.
func proxyWarning(origin, clientIPHeader, addr string) string {
	if clientIPHeader != "" || !strings.HasPrefix(origin, "https://") {
		return ""
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	if host == "localhost" {
		host = "127.0.0.1"
	}
	ip, err := netip.ParseAddr(host)
	if err != nil || !(ip.IsLoopback() || ip.IsPrivate()) {
		return ""
	}
	return "WARNING: the group server listens on " + addr + " behind a proxy but --client-ip-header is not set: every client shares the proxy's address, so sign-in failures are limited per name only, not per client address; set --client-ip-header to the header your proxy fills (for example X-Forwarded-For)"
}

// dataTables are the tables whose rows are someone's work. A catalog with rows in any of them and no account has been used in
// local mode; turning it into a group server shows all of it to every account, which needs the operator to say so.
var dataTables = []string{"datasets", "figures", "boards", "snapshots", "reports", "attachments", "runs", "jobs", "sweeps", "hosts",
	"projects", "subsets", "masks", "saved_regions", "transfers", "derived", "run_views", "exploration_steps"}

// adoptionNeeded returns the data tables that hold rows when the catalog has no account yet (a local catalog), or nil for a
// fresh catalog or one that already is a group server's.
func adoptionNeeded(db *sql.DB) ([]string, error) {
	var accts int
	if err := db.QueryRow(`SELECT COUNT(*) FROM accounts`).Scan(&accts); err != nil {
		return nil, err
	}
	if accts > 0 {
		return nil, nil
	}
	var held []string
	for _, t := range dataTables {
		var exists int
		if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, t).Scan(&exists); err != nil {
			return nil, err
		}
		if exists == 0 {
			continue
		}
		var any int
		if err := db.QueryRow(`SELECT EXISTS(SELECT 1 FROM ` + t + `)`).Scan(&any); err != nil {
			return nil, err
		}
		if any == 1 {
			held = append(held, t)
		}
	}
	return held, nil
}

// adoptionRefusal is the one sentence the server stops with, or "" when it may start.
func adoptionRefusal(held []string, adopt bool) string {
	if len(held) == 0 || adopt {
		return ""
	}
	return "this catalog already holds local work (" + strings.Join(held, ", ") + ") and every account will see all of it: pass --adopt-existing-catalog to accept that, or start with a new catalog"
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
func openGroup(store *catalog.Store, dsn, dataDir, keyPath, origin, clientIPHeader, addr string, adopt bool) (server.MultiUser, func()) {
	acc, err := accounts.Open(store.DB())
	if err != nil {
		log.Fatal(err)
	}
	held, err := adoptionNeeded(store.DB())
	if err != nil {
		log.Fatal(err)
	}
	if msg := adoptionRefusal(held, adopt); msg != "" {
		log.Fatalf("refusing to start the group server: %s", msg)
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
	if w := proxyWarning(origin, clientIPHeader, addr); w != "" {
		log.Print(w)
	}
	return server.NewMultiUser(origin, acc, clientIPHeader), func() {
		stop()
		ro.Close()
	}
}
