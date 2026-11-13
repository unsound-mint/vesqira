// Package hosts connects Vesqira to other machines (an HPC login node, another laptop) through the system ssh.
//
// Vesqira never stores, reads or sends keys or passwords: it runs `ssh` with the user's own configuration and agent.
// A connection (VPN included) is the user's to bring up. The plane worker runs on the remote host over that ssh
// channel, so only planes travel and cubes stay where they are (until the owner queues a transfer, transfer.go), and the remote worker enforces the host's source
// roots itself, because this process cannot resolve symlinks on another machine.
package hosts

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/unsound-mint/vesqira/services/api/internal/schema"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

var (
	ErrNotFound = errors.New("not found")
	ErrInvalid  = errors.New("invalid")
)

var (
	destRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,99}$`)       // never starts with "-": no option injection
	pythonRe = regexp.MustCompile(`^[A-Za-z0-9_./~][A-Za-z0-9_./~-]{0,199}$`) // never starts with "-": `exec -c` or a python option must not come from here
)

// MaxRoots is how many source roots a host may have. Opening a project folder on a host adds one.
const MaxRoots = 32

type Host struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Dest      string    `json:"dest"`
	Python    string    `json:"python"`
	Roots     []string  `json:"roots"`
	CreatedAt time.Time `json:"createdAt"`
	// VPNCommand and VPNDisconnectCommand are the user's own VPN client as an argv (ADR 0029 step 3), stored verbatim and run
	// without a shell (vpn.go). Empty means none.
	VPNCommand           []string `json:"vpnCommand,omitempty"`
	VPNDisconnectCommand []string `json:"vpnDisconnectCommand,omitempty"`
}

// Validate checks everything that ends up on an ssh command line.
func (h Host) Validate() error {
	if n := strings.TrimSpace(h.Name); n == "" || len(n) > 60 || strings.ContainsAny(n, "\x00\n\r") {
		return fmt.Errorf("%w: a host needs a name of up to 60 characters", ErrInvalid)
	}
	if !destRe.MatchString(h.Dest) {
		return fmt.Errorf("%w: the ssh destination must be a host name or ssh alias (letters, digits, . _ @ -), not starting with a dash", ErrInvalid)
	}
	if !pythonRe.MatchString(h.Python) {
		return fmt.Errorf("%w: the Python command may only contain letters, digits and . _ / ~ -", ErrInvalid)
	}
	if len(h.Roots) == 0 || len(h.Roots) > MaxRoots {
		return fmt.Errorf("%w: give between one and %d source roots on the host", ErrInvalid, MaxRoots)
	}
	for _, r := range h.Roots {
		if !path.IsAbs(r) || len(r) > 500 || strings.ContainsAny(r, "\x00\n\r") || path.Clean(r) != strings.TrimRight(r, "/") && r != "/" {
			return fmt.Errorf("%w: source roots must be clean absolute paths", ErrInvalid)
		}
		if r == "/" {
			return fmt.Errorf("%w: the root directory is not an acceptable source root", ErrInvalid)
		}
	}
	if len(h.VPNCommand) > 0 {
		if err := ValidateVPNArgv(h.VPNCommand); err != nil {
			return err
		}
	}
	if len(h.VPNDisconnectCommand) > 0 {
		if err := ValidateVPNArgv(h.VPNDisconnectCommand); err != nil {
			return err
		}
	}
	return nil
}

type Store struct{ db *sql.DB }

func Open(db *sql.DB) (*Store, error) {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS hosts(
		id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE, dest TEXT NOT NULL, python TEXT NOT NULL,
		roots TEXT NOT NULL, created_at INTEGER NOT NULL, vpn_command TEXT NOT NULL DEFAULT '', vpn_disconnect TEXT NOT NULL DEFAULT '')`)
	if err == nil {
		err = schema.Scope(db, "hosts")
	}
	if err != nil {
		return nil, err
	}
	return &Store{db}, nil
}

func (s *Store) Create(h Host) (Host, error) {
	if h.Python == "" {
		h.Python = "python3"
	}
	h.Name = strings.TrimSpace(h.Name)
	if err := h.Validate(); err != nil {
		return Host{}, err
	}
	rj, _ := json.Marshal(h.Roots)
	res, err := s.db.Exec(`INSERT INTO hosts(name,dest,python,roots,created_at,vpn_command,vpn_disconnect) VALUES(?,?,?,?,?,?,?)`,
		h.Name, h.Dest, h.Python, string(rj), time.Now().Unix(), encodeArgv(h.VPNCommand), encodeArgv(h.VPNDisconnectCommand))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return Host{}, fmt.Errorf("%w: a host with that name exists", ErrInvalid)
		}
		return Host{}, err
	}
	id, _ := res.LastInsertId()
	return s.Get(id)
}

func scan(row interface{ Scan(...any) error }) (Host, error) {
	var h Host
	var rj, vc, vd string
	var at int64
	if err := row.Scan(&h.ID, &h.Name, &h.Dest, &h.Python, &rj, &at, &vc, &vd); err != nil {
		return Host{}, err
	}
	_ = json.Unmarshal([]byte(rj), &h.Roots)
	h.VPNCommand, h.VPNDisconnectCommand = decodeArgv(vc), decodeArgv(vd)
	h.CreatedAt = time.Unix(at, 0).UTC()
	return h, nil
}

func (s *Store) Get(id int64) (Host, error) {
	h, err := scan(s.db.QueryRow(`SELECT id,name,dest,python,roots,created_at,vpn_command,vpn_disconnect FROM hosts WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Host{}, ErrNotFound
	}
	return h, err
}

func (s *Store) List() ([]Host, error) {
	rows, err := s.db.Query(`SELECT id,name,dest,python,roots,created_at,vpn_command,vpn_disconnect FROM hosts ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Host{}
	for rows.Next() {
		h, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// AddRoot makes one more folder on the host readable (a project folder that was opened). The caller must drop the host's
// worker afterwards (Manager.Forget): the worker enforces the roots it was started with.
func (s *Store) AddRoot(id int64, root string) (Host, error) {
	h, err := s.Get(id)
	if err != nil {
		return Host{}, err
	}
	for _, r := range h.Roots {
		if r == root {
			return h, nil
		}
	}
	h.Roots = append(h.Roots, root)
	if err := h.Validate(); err != nil {
		return Host{}, err
	}
	rj, _ := json.Marshal(h.Roots)
	if _, err := s.db.Exec(`UPDATE hosts SET roots=? WHERE id=?`, string(rj), id); err != nil {
		return Host{}, err
	}
	return h, nil
}

// RemoveRoot takes a root away again (a project folder whose worker check failed).
func (s *Store) RemoveRoot(id int64, root string) error {
	h, err := s.Get(id)
	if err != nil {
		return err
	}
	var keep []string
	for _, r := range h.Roots {
		if r != root {
			keep = append(keep, r)
		}
	}
	if len(keep) == 0 || len(keep) == len(h.Roots) {
		return nil // never leave a host without a root; nothing to remove
	}
	rj, _ := json.Marshal(keep)
	_, err = s.db.Exec(`UPDATE hosts SET roots=? WHERE id=?`, string(rj), id)
	return err
}

func (s *Store) Delete(id int64) error {
	res, err := s.db.Exec(`DELETE FROM hosts WHERE id=?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- running the worker on a host ----

// Manager keeps one persistent remote worker per host (one scientific task at a time per host, which also bounds the
// load on a shared login node) and deploys the worker scripts to the host by content hash.
type Manager struct {
	Ssh        string            // the ssh binary; "ssh" unless a test substitutes one
	Rsync      string            // the rsync binary for transfers; "rsync" unless a test substitutes one
	Files      map[string][]byte // worker script files to deploy: the modules listed in the worker directory's DEPLOY file (LoadWorkerFiles)
	ControlDir string            // where ssh keeps its multiplexing sockets (short path)
	Timeout    time.Duration
	OnDo       func(op string) // called for each worker request, before it is sent; tests count round trips with it

	mu       sync.Mutex
	workers  map[int64]*worker.Persistent
	deployed map[int64]string // host id -> hash deployed this session
}

func NewManager(ssh string, files map[string][]byte) *Manager {
	if ssh == "" {
		ssh = "ssh"
	}
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("vesqira-ssh-%d", os.Getuid()))
	_ = os.MkdirAll(dir, 0o700)
	return &Manager{Ssh: ssh, Files: files, ControlDir: dir, Timeout: 90 * time.Second, workers: map[int64]*worker.Persistent{}, deployed: map[int64]string{}}
}

// Hash identifies the deployed worker version.
func (m *Manager) Hash() string {
	h := sha256.New()
	for _, n := range m.fileNames() {
		h.Write([]byte(n))
		h.Write(m.Files[n])
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func (m *Manager) fileNames() []string {
	names := make([]string, 0, len(m.Files))
	for n := range m.Files {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// RemoteCacheDir is the folder on a host where its worker keeps rebuildable sidecars (the index of a ROHSA .dat): inside
// the account's own cache, beside the deployed worker, never under a source root or next to data (the shell expands $HOME).
func (m *Manager) RemoteCacheDir() string { return "$HOME/.cache/vesqira/rohsa" }

// RemoteDir is where the deployed worker scripts live on a host (the shell expands $HOME there).
func (m *Manager) RemoteDir() string { return m.remoteDir() }

func (m *Manager) sshArgs(h Host) []string {
	return []string{"-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=2",
		"-o", "ControlMaster=auto", "-o", "ControlPath=" + filepath.Join(m.ControlDir, "%C"), "-o", "ControlPersist=120", "-o", "LogLevel=ERROR", h.Dest}
}

// TerminalCommand is the ssh command line for an interactive login on h (the integrated terminal, ADR 0029): the same
// binary, the same multiplexing socket and the user's own agent as the worker connections, with a forced terminal (-tt).
// Unlike the worker connections it is not BatchMode: a passphrase or one-time-code prompt appears in the terminal, where
// the person types it, and Vesqira never sees it. The destination is the stored, validated one, never a string from the page.
func (m *Manager) TerminalCommand(h Host) (bin string, args []string, env []string, err error) {
	if err := h.Validate(); err != nil {
		return "", nil, nil, err
	}
	// -e none: no ssh escape character (~. and friends) is interpreted from what the page types; -- ends the options so
	// a destination can never be read as one.
	args = []string{"-tt", "-e", "none", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=2",
		"-o", "ControlMaster=auto", "-o", "ControlPath=" + filepath.Join(m.ControlDir, "%C"), "-o", "ControlPersist=120", "--", h.Dest}
	return m.Ssh, args, os.Environ(), nil // the whole environment, so the agent socket and the user's locale reach ssh
}

// quote single-quotes a string for the remote shell.
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func (m *Manager) env() []string {
	var out []string
	for _, k := range []string{"SSH_AUTH_SOCK", "SSH_AGENT_PID"} { // the agent is how the user's key is used; Vesqira never reads keys
		if v := os.Getenv(k); v != "" {
			out = append(out, k+"="+v)
		}
	}
	return out
}

// remoteDir is where this worker version lives on the host.
func (m *Manager) remoteDir() string { return "$HOME/.cache/vesqira/" + m.Hash() }

func (m *Manager) tarball() ([]byte, error) {
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for _, n := range m.fileNames() {
		data := m.Files[n]
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(data)), ModTime: time.Unix(0, 0)}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Deploy copies the worker scripts to the host if this version is not there yet.
func (m *Manager) Deploy(ctx context.Context, h Host) error {
	m.mu.Lock()
	if m.deployed[h.ID] == m.Hash() {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()
	tb, err := m.tarball()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	d := m.remoteDir()
	remote := fmt.Sprintf(`test -f "%s/plane_worker.py" || (mkdir -p "%s" && tar -xf - -C "%s")`, d, d, d)
	cmd := exec.CommandContext(ctx, m.Ssh, append(m.sshArgs(h), "sh -c "+quote(remote))...) // sh -c: the login shell may not be POSIX
	cmd.Env = append([]string{"PATH=/usr/bin:/bin:/usr/local/bin", "HOME=" + os.Getenv("HOME")}, m.env()...)
	cmd.Stdin = bytes.NewReader(tb)
	killTree(cmd)
	if err := cmd.Run(); err != nil {
		return errors.New("could not reach the host or copy the worker (is the VPN up and ssh working for this host?)")
	}
	m.mu.Lock()
	m.deployed[h.ID] = m.Hash()
	m.mu.Unlock()
	return nil
}

func (m *Manager) worker(h Host) *worker.Persistent {
	m.mu.Lock()
	defer m.mu.Unlock()
	if w, ok := m.workers[h.ID]; ok {
		return w
	}
	rj, _ := json.Marshal(h.Roots)
	// VESQIRA_CACHE is the one place this worker may write (the ROHSA index, ADR 0046): fixed here, never taken from a request
	remote := fmt.Sprintf(`cd "%s" && VESQIRA_ROOTS=%s VESQIRA_CACHE="%s" exec %s -P plane_worker.py`, m.remoteDir(), quote(string(rj)), m.RemoteCacheDir(), h.Python)
	w := &worker.Persistent{Command: append(append([]string{m.Ssh}, m.sshArgs(h)...), "sh -c "+quote(remote)), Timeout: m.Timeout, ExtraEnv: m.env()}
	m.workers[h.ID] = w
	return w
}

// Do sends one request to the host's worker, deploying it first if needed.
func (m *Manager) Do(ctx context.Context, h Host, req map[string]any) (worker.Reply, error) {
	if m.OnDo != nil {
		op, _ := req["op"].(string)
		m.OnDo(op)
	}
	if err := m.Deploy(ctx, h); err != nil {
		return worker.Reply{}, fmt.Errorf("%w: %v", worker.ErrUnavailable, err)
	}
	return m.worker(h).Do(ctx, req)
}

// Forget drops a host's worker (after the host is edited or removed).
func (m *Manager) Forget(id int64) {
	m.mu.Lock()
	w := m.workers[id]
	delete(m.workers, id)
	delete(m.deployed, id)
	m.mu.Unlock()
	if w != nil {
		w.Close()
	}
}

func (m *Manager) Close() {
	m.mu.Lock()
	ws := m.workers
	m.workers = map[int64]*worker.Persistent{}
	m.mu.Unlock()
	for _, w := range ws {
		w.Close()
	}
}

// InRoots is a lexical pre-check so obviously foreign paths are refused before a round trip; the remote worker
// still enforces the roots on the resolved path.
func (h Host) InRoots(p string) bool {
	if !path.IsAbs(p) || path.Clean(p) != p {
		return false
	}
	for _, r := range h.Roots {
		if p == r || strings.HasPrefix(p, strings.TrimRight(r, "/")+"/") {
			return true
		}
	}
	return false
}

// RunShell runs one fixed command on the host through a login shell (module and Slurm tools live there) and returns
// its output, capped. The command text is built by Vesqira from validated pieces; anything caller-supplied (a job
// script) is passed on stdin.
func (m *Manager) RunShell(ctx context.Context, h Host, inner string, stdin []byte, timeout time.Duration) ([]byte, error) {
	return m.runShellCap(ctx, h, inner, stdin, timeout, maxShellOutput)
}

// runShellCap is RunShell with its own bound on the output kept (the text editor reads files of up to 2 MiB).
func (m *Manager) runShellCap(ctx context.Context, h Host, inner string, stdin []byte, timeout time.Duration, maxOut int) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, m.Ssh, append(m.sshArgs(h), "bash -lc "+quote(inner))...)
	cmd.Env = append([]string{"PATH=/usr/bin:/bin:/usr/local/bin", "HOME=" + os.Getenv("HOME")}, m.env()...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	out := capped{max: maxOut}
	cmd.Stdout = &out
	killTree(cmd)
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, errors.New("the host did not answer in time")
		}
		return nil, errors.New("the command failed on the host (is the VPN up, and does ssh work for it without a prompt?)")
	}
	return out.b.Bytes(), nil
}

const maxShellOutput = 256 << 10

// capped is a writer that keeps at most max bytes and drops the rest.
type capped struct {
	b   bytes.Buffer
	max int
}

func (c *capped) Write(p []byte) (int, error) {
	if room := c.max - c.b.Len(); room > 0 {
		if len(p) > room {
			c.b.Write(p[:room])
		} else {
			c.b.Write(p)
		}
	}
	return len(p), nil
}

// ValidDest reports whether s is acceptable as an ssh destination (the same rule hosts are validated with).
func ValidDest(s string) bool { return destRe.MatchString(s) }

// DirEntry is one sub-folder shown while choosing a source folder on a host.
type DirEntry struct {
	Name string `json:"name"`
}

// BrowseDirs lists the sub-folders of dir on a host that is not saved yet, with the user's own ssh, so source folders can
// be chosen instead of typed. An empty dir means the host's home directory. It lists names only and never reads files;
// credential folders are not shown. It returns the resolved directory.
func (m *Manager) BrowseDirs(ctx context.Context, dest, dir string) (string, []DirEntry, error) {
	if !ValidDest(dest) {
		return "", nil, fmt.Errorf("%w: the ssh destination must be a host name or ssh alias", ErrInvalid)
	}
	target := `"$HOME"`
	if dir != "" {
		if !path.IsAbs(dir) || len(dir) > 500 || strings.ContainsAny(dir, "\x00\n\r") || path.Clean(dir) != strings.TrimRight(dir, "/") && dir != "/" {
			return "", nil, fmt.Errorf("%w: give a clean absolute folder", ErrInvalid)
		}
		target = quote(dir)
	}
	out, err := m.RunShell(ctx, Host{Dest: dest}, "cd -- "+target+" && pwd -P && ls -1pA", nil, 20*time.Second)
	if err != nil {
		return "", nil, err
	}
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	if len(lines) == 0 || !path.IsAbs(lines[0]) {
		return "", nil, errors.New("the host did not answer with a folder")
	}
	dirs := []DirEntry{}
	for _, l := range lines[1:] {
		name, isDir := strings.CutSuffix(l, "/")
		if !isDir || name == "" || name == ".ssh" || name == ".gnupg" {
			continue
		}
		dirs = append(dirs, DirEntry{Name: name})
		if len(dirs) >= 2000 {
			break
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return strings.ToLower(dirs[i].Name) < strings.ToLower(dirs[j].Name) })
	return lines[0], dirs, nil
}

// LoadWorkerFiles reads the worker modules listed in dir/DEPLOY (one name per line, # comments): the single list of what
// ships to a remote host. tools/check_worker_manifest.py keeps it equal to what the worker imports.
func LoadWorkerFiles(dir string) (map[string][]byte, error) {
	list, err := os.ReadFile(filepath.Join(dir, "DEPLOY"))
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	for _, line := range strings.Split(string(list), "\n") {
		name := strings.TrimSpace(line)
		if name == "" || strings.HasPrefix(name, "#") {
			continue
		}
		if name != filepath.Base(name) {
			return nil, fmt.Errorf("DEPLOY: %q is not a plain file name", name)
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		files[name] = b
	}
	return files, nil
}
