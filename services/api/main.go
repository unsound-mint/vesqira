// Command api is the Vesqira control plane: local mode (SQLite, loopback only, one owner) or, with --multi-user, the group
// server (accounts, sessions and tokens; ADR 0050).
package main

import (
	"context"
	"flag"
	"github.com/unsound-mint/vesqira/services/api/internal/discussion"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/access"
	"github.com/unsound-mint/vesqira/services/api/internal/audit"
	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/derived"
	"github.com/unsound-mint/vesqira/services/api/internal/history"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/masks"
	"github.com/unsound-mint/vesqira/services/api/internal/notify"
	"github.com/unsound-mint/vesqira/services/api/internal/offline"
	"github.com/unsound-mint/vesqira/services/api/internal/outbox"
	"github.com/unsound-mint/vesqira/services/api/internal/projects"
	"github.com/unsound-mint/vesqira/services/api/internal/pushed"
	"github.com/unsound-mint/vesqira/services/api/internal/regions"
	"github.com/unsound-mint/vesqira/services/api/internal/relay"
	"github.com/unsound-mint/vesqira/services/api/internal/reports"
	"github.com/unsound-mint/vesqira/services/api/internal/review"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/server"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
	"github.com/unsound-mint/vesqira/services/api/internal/subsets"
	"github.com/unsound-mint/vesqira/services/api/internal/sweeps"
	"github.com/unsound-mint/vesqira/services/api/internal/transfers"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	if archiveCommand(os.Args[1:]) || publishServerCommand(os.Args[1:]) || agentCommand(os.Args[1:]) || supportCommand(os.Args[1:]) || adminCommand(os.Args[1:]) || multiuserCommand(os.Args[1:]) {
		return
	}
	var roots, trustedProxies multi
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
	enableVPN := flag.Bool("enable-vpn", false, "let the owner set a VPN client command per host and run it from the app (ADR 0029); runs that command as you, without a shell; off by default, never on the review address")
	allowOthers := flag.Bool("allow-other-local-users", false, "DANGEROUS: do not check which user account connects to the owner address; on a shared computer any other account can then use the app as you (and open a shell with --enable-terminal)")
	wInteractive := flag.Int("workers-interactive", worker.DefaultBudgets().Interactive, "worker processes reserved for interactive viewing (planes, coordinates, overlays); never used by heavy or background work (ADR 0056)")
	wHeavy := flag.Int("workers-heavy", worker.DefaultBudgets().Heavy, "heavy tasks at once: spectra over large regions, PV, channel grids, index builds, and analysis programs")
	wBackground := flag.Int("workers-background", worker.DefaultBudgets().Background, "background tasks at once: project scans, fingerprints; at most one of them is a full file hash, which takes the last slot only when this is 1 (ADR 0056)")
	multiUser := flag.Bool("multi-user", false, "run as the group server (ADR 0050): people sign in with a password and TOTP; clusters, jobs, sweeps, host access, the editor and the terminal are not available. Needs --public-origin; create the first admin with `vesqira-api admin bootstrap`")
	publicOrigin := flag.String("public-origin", "", "with --multi-user: the https origin people open (for example https://vesqira.example.edu); every request must name its host and every write must come from it")
	insecureDev := flag.Bool("insecure-dev", false, "with --multi-user: accept an http://localhost public origin, for development only")
	clientIPHeader := flag.String("client-ip-header", "", "with --multi-user: header a trusted reverse proxy sets to the client address (for example X-Forwarded-For), for the per-address sign-in limit")
	adopt := flag.Bool("adopt-existing-catalog", false, "with --multi-user: start on a catalog that already holds local work (datasets, boards, runs...); every account will see all of it")
	secretKey := flag.String("secret-key", "", "with --multi-user: the server key file that seals TOTP secrets (default: secrets/server.key beside the database, created 0600); back it up separately, it is not in exports")
	flag.Var(&trustedProxies, "trusted-proxy", "with --multi-user: an address or CIDR range whose --client-ip-header is believed, besides loopback and private addresses (repeatable)")
	flag.Var(&roots, "root", "source root directory (repeatable, required)")
	flag.Parse()
	if !*multiUser && (*publicOrigin != "" || *insecureDev || *clientIPHeader != "" || *secretKey != "" || *adopt || len(trustedProxies) > 0) {
		log.Fatalf("--public-origin, --insecure-dev, --client-ip-header, --trusted-proxy, --secret-key and --adopt-existing-catalog belong to --multi-user")
	}
	origin := ""
	trusted, err := parseTrustedProxies(trustedProxies)
	if err != nil {
		log.Fatal(err)
	}
	if *multiUser {
		var err error
		origin, err = groupStart{origin: *publicOrigin, insecureDev: *insecureDev, editor: *enableEditor, terminal: *enableTerminal, vpn: *enableVPN,
			addr: *addr, reviewAddr: *reviewAddr, dbDir: filepath.Dir(strings.TrimPrefix(*db, "file:")), onNetworkFS: networkFS, allowOthers: *allowOthers}.check()
		if err != nil {
			log.Fatalf("refusing to start the group server: %v", err)
		}
	}

	budgets := worker.Budgets{Interactive: *wInteractive, Heavy: *wHeavy, Background: *wBackground}
	if err := budgets.Validate(); err != nil {
		log.Fatalf("workload budgets: %v", err)
	}
	load := worker.NewLoad(budgets)
	// Resolve the interpreter once, in this process's environment, so the workers (which run with a minimal PATH)
	// use the same Python, and the same astropy, as the one the operator started the API with.
	if p, err := exec.LookPath(*python); err == nil {
		*python = p
	}
	host, _, err := net.SplitHostPort(*addr)
	if !*multiUser && (err != nil || (host != "127.0.0.1" && host != "localhost" && host != "::1")) {
		log.Fatalf("refusing to listen on %q: local mode binds to loopback only", *addr)
	}
	gateOwner := true
	switch {
	case *multiUser: // the group server is reached through its reverse proxy, which runs as another account; accounts guard it
		gateOwner = false
	case *allowOthers:
		gateOwner = false
		log.Printf("WARNING: --allow-other-local-users: any user account on this computer can reach the owner API on %s and act as you", *addr)
	case runtime.GOOS != "linux":
		gateOwner = false
		if *enableTerminal || *enableEditor || *enableVPN {
			log.Fatalf("--enable-terminal, --enable-editor and --enable-vpn need the check that only your own user account reaches the owner address, which exists on Linux only; on %s pass --allow-other-local-users to accept that any local account can use them", runtime.GOOS)
		}
		log.Printf("WARNING: on %s any user account on this computer can reach the owner API on %s; the same-user check is Linux only (SECURITY.md)", runtime.GOOS, *addr)
	default:
		if _, err := os.Stat("/proc/net/tcp"); err != nil {
			log.Fatalf("cannot check which user connects to %s: %v (the owner address would be open to every account; pass --allow-other-local-users to accept that)", *addr, err)
		}
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
	if !*multiUser {
		if msg, err := localRefusal(store.DB()); err != nil {
			log.Fatal(err)
		} else if msg != "" {
			log.Fatal(msg)
		}
	}

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
	historyStore, err := history.Open(store.DB())
	if err != nil {
		log.Fatal(err)
	}
	regionStore, err := regions.Open(store.DB())
	if err != nil {
		log.Fatal(err)
	}
	subsetStore, err := subsets.Open(store.DB())
	if err != nil {
		log.Fatal(err)
	}
	accessStore, err := access.Open(store.DB())
	if err != nil {
		log.Fatalf("access labels: %v", err)
	}
	derivedStore, err := derived.Open(store.DB())
	if err != nil {
		log.Fatal(err)
	}
	maskStore, err := masks.Open(store.DB())
	if err != nil {
		log.Fatal(err)
	}
	offlineStore, err := offline.Open(store.DB(), filepath.Join(dir, "offline"))
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
	// push to a group server (ADR 0066): the server keeps what linked instances push; a local instance keeps its link and queue
	pushedStore, err := pushed.Open(store.DB(), filepath.Join(dir, "pushed"))
	if err != nil {
		log.Fatal(err)
	}
	outboxStore, err := outbox.Open(store.DB(), filepath.Join(dir, "outbox"))
	if err != nil {
		log.Fatal(err)
	}
	// relayed run requests (ADR 0066 section 3): the server keeps inboxes and requests; a key holder's instance its inbox and mirror
	relayStore, err := relay.Open(store.DB())
	if err != nil {
		log.Fatal(err)
	}
	relayedStore, err := relay.OpenLocal(store.DB())
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
	planes := &worker.Persistent{Command: []string{*python, "-P", *planeScript}, Timeout: 30 * time.Second, Load: load}
	defer planes.Close()
	var group server.MultiUser
	if *multiUser {
		var closeGroup func()
		group, closeGroup = openGroup(store, *db, dir, *secretKey, origin, *clientIPHeader, *addr, trusted, *adopt)
		defer closeGroup()
		auditStore.Default = audit.System // background work on the group server acts for no account; requests record their own actor
		hostStore, remote = nil, nil      // no ssh credential on the group server (ADR 0050 section 5): no host is reachable
	}
	srv := &server.Server{MultiUser: group, Roots: rs, Store: store, Worker: planes, Planes: planes, Load: load, Boards: bs, Runs: rn, HostDB: hostStore, Remote: remote, Jobs: jobStore, Sweeps: sweepStore, Transfers: transferStore, Derived: derivedStore, Access: accessStore, Masks: maskStore, Offline: offlineStore, ManagedDir: managedDir, CacheDir: filepath.Join(dir, "cache", "rohsa"), Audit: auditStore, Notes: noteStore, Regions: regionStore, Subsets: subsetStore, History: historyStore, Discussion: discussion.Open(store.DB()), Projects: projectStore, WebDist: *webDist, EditorEnabled: *enableEditor, TerminalEnabled: *enableTerminal, VPNEnabled: *enableVPN, VPNLogDir: filepath.Join(dir, "vpn"), Reports: reportStore, PublishKeyFile: filepath.Join(dir, "publish.key"),
		Analysis: &server.AnalysisConfig{Python: *python, Script: filepath.Join(filepath.Dir(*planeScript), "analysis.py"), DerivedDir: derivedDir, Timeout: 30 * time.Minute}}
	if *multiUser {
		srv.Pushed, srv.Relay = pushedStore, relayStore
	} else {
		srv.Outbox, srv.GroupKeyFile, srv.Relayed = outboxStore, filepath.Join(dir, "group.key"), relayedStore
	}
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
	if *enableVPN {
		log.Printf("vpn enabled: a host can carry a VPN client command that the app runs as this user, without a shell (loopback only)")
	}
	if *enableTerminal {
		log.Printf("terminal enabled: the app can open a shell as this user, here or over ssh (loopback only)")
	}
	if *multiUser {
		log.Printf("group server on %s for %s (behind your TLS proxy), roots: %v", *addr, origin, []string(rs))
	} else {
		log.Printf("listening on http://%s, roots: %v", *addr, []string(rs))
	}
	// No WriteTimeout: a cube analysis answers one request for up to its own 30-minute budget. Reading a request is
	// bounded, and so is an idle connection.
	owner := &http.Server{Addr: *addr, Handler: srv.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, IdleTimeout: 2 * time.Minute, MaxHeaderBytes: 16 << 10}
	servers = append(servers, owner)
	ownerLn, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	var ownerServe net.Listener = ownerLn
	if gateOwner { // only this process's own uid may connect (peergate.go); the review and publish listeners are for others and stay open
		ownerServe = server.NewPeerGate(ownerLn)
	}
	go func() {
		if err := owner.Serve(ownerServe); err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	// On SIGINT/SIGTERM stop taking requests and let running ones finish (bounded), so a write to the catalog or a
	// copy in progress is not cut off mid-way. The deferred Closes above then run.
	stop, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if *jobWatch > 0 && !*multiUser { // follow queued and running jobs on the hosts while the app runs (ADR 0043)
		go srv.WatchJobs(stop, *jobWatch)
	}
	// headers stored before orientation profiles get their reading (ADR 0068); files that changed are skipped
	if !*multiUser {
		go srv.RefreshProfileHeaders(stop)
	}
	if !*multiUser { // send the push queue to the linked group server, if any, without ever blocking a request (ADR 0066)
		go srv.RunOutbox(stop)
		go srv.RunRelay(stop) // pull the run requests addressed to this computer's account, if it takes any
	}
	transfersStopped := make(chan struct{}) // the transfer queue (ADR 0051); on shutdown rsync gets SIGTERM and keeps its partial files
	go func() {
		if !*multiUser {
			srv.RunTransfers(stop)
		}
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
