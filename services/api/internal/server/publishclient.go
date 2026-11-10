package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/publish"
)

// publishClient talks to the owner's publish server (ADR 0036) with the publisher key. The key is read from a 0600
// file beside the catalog on every use and is never logged, stored in the catalog or returned by the API.
type publishClient struct {
	base string // https://host[:port], no trailing slash
	key  string
	http *http.Client
}

var errNotConfigured = errors.New("No publish server is set up yet. Open Share to add its address and publisher key.")

// checkPublishURL accepts https, and plain http only to a loopback address (a server on this machine, or a test).
func checkPublishURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("the publish server address must look like https://reports.example.org")
	}
	switch u.Scheme {
	case "https":
	case "http":
		h := u.Hostname()
		if ip := net.ParseIP(h); h != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return "", fmt.Errorf("plain http is only allowed to this machine; use https for a server")
		}
	default:
		return "", fmt.Errorf("the publish server address must start with https://")
	}
	return u.Scheme + "://" + u.Host, nil
}

func readKey(file string) (string, error) {
	st, err := os.Stat(file)
	if errors.Is(err, os.ErrNotExist) {
		return "", errNotConfigured
	}
	if err != nil {
		return "", err
	}
	if st.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("the publisher key file %s is readable by other users; run chmod 600 on it", file)
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	k := strings.TrimSpace(string(b))
	if k == "" {
		return "", errNotConfigured
	}
	return k, nil
}

// writeKey replaces the key file atomically with a fresh 0600 file (never an existing one, which could be a symlink
// or keep a wider mode).
func writeKey(file, key string) error {
	tmp, err := os.CreateTemp(filepath.Dir(file), ".publish.key-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // a no-op after the rename
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(strings.TrimSpace(key) + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file)
}

func (s *Server) publisher() (*publishClient, error) {
	if s.Reports == nil || s.PublishKeyFile == "" {
		return nil, errNotConfigured
	}
	base, err := s.Reports.Setting("publish_url")
	if err != nil {
		return nil, err
	}
	if base == "" {
		return nil, errNotConfigured
	}
	key, err := readKey(s.PublishKeyFile)
	if err != nil {
		return nil, err
	}
	return &publishClient{base: base, key: key, http: &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// remoteError is what the publish server answered; the message is shown to the owner as is (it never holds the key).
type remoteError struct {
	Status int
	Msg    string
}

func (e *remoteError) Error() string {
	return fmt.Sprintf("the publish server answered %d: %s", e.Status, e.Msg)
}

func (c *publishClient) do(method, path, contentType string, body io.Reader, out any) error {
	req, err := http.NewRequest(method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err // the URL has no secret, but keep messages short
		}
		return fmt.Errorf("the publish server could not be reached: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error string `json:"error"`
		}
		msg := strings.TrimSpace(string(b))
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return &remoteError{resp.StatusCode, msg}
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}

func (c *publishClient) json(method, path string, in, out any) error {
	var body io.Reader
	ct := ""
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body, ct = bytes.NewReader(b), "application/json"
	}
	return c.do(method, path, ct, body, out)
}

func (c *publishClient) ping() error { return c.json("GET", "/publish-api/ping", nil, nil) }

func (c *publishClient) putAsset(sha string, data []byte) error {
	return c.do("PUT", "/publish-api/assets/"+sha, "application/octet-stream", bytes.NewReader(data), nil)
}

func (c *publishClient) pushRevision(r publish.Revision) (publish.Revision, error) {
	var out publish.Revision
	err := c.json("POST", "/publish-api/revisions", r, &out)
	return out, err
}

func (c *publishClient) putSpace(sp publish.Space) error {
	return c.json("PUT", "/publish-api/spaces/"+url.PathEscape(sp.Slug), map[string]any{"title": sp.Title, "reports": sp.Reports}, nil)
}

func (c *publishClient) readers() ([]publish.Reader, error) {
	var out []publish.Reader
	err := c.json("GET", "/publish-api/readers", nil, &out)
	return out, err
}

func (c *publishClient) createReader(space, name string, expires *time.Time) (publish.Reader, error) {
	var out publish.Reader
	err := c.json("POST", "/publish-api/spaces/"+url.PathEscape(space)+"/readers", map[string]any{"name": name, "expiresAt": expires}, &out)
	return out, err
}

func (c *publishClient) revokeReader(id int64) error {
	return c.json("POST", "/publish-api/readers/"+strconv.FormatInt(id, 10)+"/revoke", nil, nil)
}

func (c *publishClient) commentsAfter(after int64) ([]publish.Comment, error) {
	var out []publish.Comment
	err := c.json("GET", "/publish-api/comments?after="+strconv.FormatInt(after, 10), nil, &out)
	return out, err
}

func (c *publishClient) ownerComment(space string, in publish.Comment) (publish.Comment, error) {
	var out publish.Comment
	err := c.json("POST", "/publish-api/comments", map[string]any{
		"space": space, "report": in.Report, "revision": in.Revision, "block": in.Block, "parentId": in.ParentID, "body": in.Body, "author": in.Author, "quote": in.Quote,
	}, &out)
	return out, err
}

func (c *publishClient) deleteComment(id int64) ([]int64, error) {
	var out struct {
		Deleted []int64 `json:"deleted"`
	}
	err := c.json("DELETE", "/publish-api/comments/"+strconv.FormatInt(id, 10), nil, &out)
	return out.Deleted, err
}

func (c *publishClient) deletionsAfter(after int64) ([]publish.Deletion, error) {
	var out struct {
		Deletions []publish.Deletion `json:"deletions"`
	}
	err := c.json("GET", "/publish-api/comments/deleted?after="+strconv.FormatInt(after, 10), nil, &out)
	return out.Deletions, err
}

func (c *publishClient) withdraw(key string) error {
	return c.json("DELETE", "/publish-api/reports/"+url.PathEscape(key), nil, nil)
}

func (c *publishClient) resolve(id int64, resolved bool) (publish.Comment, error) {
	var out publish.Comment
	err := c.json("POST", "/publish-api/comments/"+strconv.FormatInt(id, 10)+"/resolve", map[string]bool{"resolved": resolved}, &out)
	return out, err
}

// isNotFound is true when the publish server answered 404: what was to be deleted is already gone there.
func isNotFound(err error) bool {
	var re *remoteError
	return errors.As(err, &re) && re.Status == 404
}
