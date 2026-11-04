// Package hosts connects Vesqira to other machines (an HPC login node, another laptop) through the system ssh.
//
// Vesqira never stores, reads or sends keys or passwords: it runs `ssh` with the user's own configuration and agent.
// A connection (VPN included) is the user's to bring up. The plane worker runs on the remote host over that ssh
// channel, so only planes travel and cubes stay where they are, and the remote worker enforces the host's source
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
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
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
	destRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@-]{0,99}$`) // never starts with "-": no option injection
	pythonRe = regexp.MustCompile(`^[A-Za-z0-9_./~-]{1,200}$`)
)

type Host struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Dest      string    `json:"dest"`
	Python    string    `json:"python"`
	Roots     []string  `json:"roots"`
	CreatedAt time.Time `json:"createdAt"`
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
	if len(h.Roots) == 0 || len(h.Roots) > 8 {
		return fmt.Errorf("%w: give between one and eight source roots on the host", ErrInvalid)
	}
	for _, r := range h.Roots {
		if !path.IsAbs(r) || len(r) > 500 || strings.ContainsAny(r, "\x00\n\r") || path.Clean(r) != strings.TrimRight(r, "/") && r != "/" {
			return fmt.Errorf("%w: source roots must be clean absolute paths", ErrInvalid)
		}
		if r == "/" {
			return fmt.Errorf("%w: the root directory is not an acceptable source root", ErrInvalid)
		}
	}
	return nil
}

type Store struct{ db *sql.DB }

func Open(db *sql.DB) (*Store, error) {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS hosts(
		id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE, dest TEXT NOT NULL, python TEXT NOT NULL,
		roots TEXT NOT NULL, created_at INTEGER NOT NULL)`)
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
	res, err := s.db.Exec(`INSERT INTO hosts(name,dest,python,roots,created_at) VALUES(?,?,?,?,?)`, h.Name, h.Dest, h.Python, string(rj), time.Now().Unix())
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
	var rj string
	var at int64
	if err := row.Scan(&h.ID, &h.Name, &h.Dest, &h.Python, &rj, &at); err != nil {
		return Host{}, err
	}
	_ = json.Unmarshal([]byte(rj), &h.Roots)
	h.CreatedAt = time.Unix(at, 0).UTC()
	return h, nil
}

func (s *Store) Get(id int64) (Host, error) {
	h, err := scan(s.db.QueryRow(`SELECT id,name,dest,python,roots,created_at FROM hosts WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Host{}, ErrNotFound
	}
	return h, err
}

func (s *Store) List() ([]Host, error) {
	rows, err := s.db.Query(`SELECT id,name,dest,python,roots,created_at FROM hosts ORDER BY name`)
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
	Files      map[string][]byte // worker script files to deploy: plane_worker.py and fits_header.py
	ControlDir string            // where ssh keeps its multiplexing sockets (short path)
	Timeout    time.Duration

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
	for _, n := range []string{"fits_header.py", "plane_worker.py"} {
		h.Write([]byte(n))
		h.Write(m.Files[n])
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func (m *Manager) sshArgs(h Host) []string {
	return []string{"-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "ServerAliveInterval=15", "-o", "ServerAliveCountMax=2",
		"-o", "ControlMaster=auto", "-o", "ControlPath=" + filepath.Join(m.ControlDir, "%C"), "-o", "ControlPersist=120", "-o", "LogLevel=ERROR", h.Dest}
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
	for _, n := range []string{"fits_header.py", "plane_worker.py"} {
		data, ok := m.Files[n]
		if !ok {
			return nil, fmt.Errorf("worker file %s is not loaded", n)
		}
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
	remote := fmt.Sprintf(`cd "%s" && VESQIRA_ROOTS=%s exec %s -P plane_worker.py`, m.remoteDir(), quote(string(rj)), h.Python)
	w := &worker.Persistent{Command: append(append([]string{m.Ssh}, m.sshArgs(h)...), "sh -c "+quote(remote)), Timeout: m.Timeout, ExtraEnv: m.env()}
	m.workers[h.ID] = w
	return w
}

// Do sends one request to the host's worker, deploying it first if needed.
func (m *Manager) Do(ctx context.Context, h Host, req map[string]any) (worker.Reply, error) {
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
