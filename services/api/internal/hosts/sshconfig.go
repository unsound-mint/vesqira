package hosts

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// SSHHost is a name from the user's own ssh configuration that can be used as a destination. Only the alias and the
// plain facts the user wrote for it are kept; key files, proxy commands and any other setting stay in ssh's own hands.
type SSHHost struct {
	Alias    string `json:"alias"`
	HostName string `json:"hostName,omitempty"`
	User     string `json:"user,omitempty"`
}

const maxSSHHosts = 500

var plainValue = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._@:-]{0,99}$`)

// ParseSSHConfig reads Host blocks from an ssh_config file. Patterns with wildcards or negation are not names a person
// would connect to and are skipped, as are Match blocks. An alias that could not be passed safely as an ssh destination
// (it must satisfy the same rule as a host's destination) is dropped. include, when not nil, is asked for the lines of an
// Include directive so the caller decides which files may be read.
func ParseSSHConfig(r io.Reader, include func(pattern string) []io.Reader) []SSHHost {
	byAlias := map[string]*SSHHost{}
	var order []string
	var current []*SSHHost
	var walk func(r io.Reader, depth int)
	walk = func(r io.Reader, depth int) {
		sc := bufio.NewScanner(r)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			key, val := splitKeyword(line)
			switch key {
			case "host":
				current = current[:0]
				for _, p := range strings.Fields(val) {
					if strings.ContainsAny(p, "*?!") || !destRe.MatchString(p) {
						continue
					}
					h, ok := byAlias[p]
					if !ok {
						if len(byAlias) >= maxSSHHosts {
							continue
						}
						h = &SSHHost{Alias: p}
						byAlias[p] = h
						order = append(order, p)
					}
					current = append(current, h)
				}
			case "match":
				current = current[:0]
			case "hostname", "user":
				v := strings.Trim(val, `"`)
				if !plainValue.MatchString(v) {
					continue
				}
				for _, h := range current {
					if key == "hostname" && h.HostName == "" {
						h.HostName = v
					}
					if key == "user" && h.User == "" {
						h.User = v
					}
				}
			case "include":
				if include == nil || depth >= 3 {
					continue
				}
				for _, f := range strings.Fields(val) {
					for _, rd := range include(strings.Trim(f, `"`)) {
						walk(rd, depth+1)
					}
				}
				current = current[:0]
			}
		}
	}
	walk(r, 0)
	sort.Strings(order)
	out := make([]SSHHost, 0, len(order))
	for _, a := range order {
		out = append(out, *byAlias[a])
	}
	return out
}

// splitKeyword separates "Keyword value", "Keyword=value" and "Keyword = value".
func splitKeyword(line string) (string, string) {
	i := strings.IndexAny(line, " \t=")
	if i < 0 {
		return strings.ToLower(line), ""
	}
	return strings.ToLower(line[:i]), strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line[i:]), "="))
}

// LoadSSHConfig reads the user's ssh configuration at path (normally ~/.ssh/config), following Include lines that
// resolve inside the same directory. A missing or unreadable file is not an error: it simply offers no names.
func LoadSSHConfig(path string) (hosts []SSHHost, found bool) {
	f, err := os.Open(path)
	if err != nil {
		return []SSHHost{}, false
	}
	defer f.Close()
	dir := filepath.Dir(path)
	var opened []*os.File
	defer func() {
		for _, o := range opened {
			o.Close()
		}
	}()
	include := func(pattern string) []io.Reader {
		if strings.HasPrefix(pattern, "~/.ssh/") {
			pattern = strings.TrimPrefix(pattern, "~/.ssh/")
		}
		if filepath.IsAbs(pattern) || strings.Contains(pattern, "..") {
			return nil
		}
		matches, _ := filepath.Glob(filepath.Join(dir, pattern))
		var out []io.Reader
		for _, m := range matches {
			if rel, err := filepath.Rel(dir, m); err != nil || strings.HasPrefix(rel, "..") {
				continue
			}
			fh, err := os.Open(m)
			if err != nil {
				continue
			}
			opened = append(opened, fh)
			out = append(out, fh)
		}
		return out
	}
	return ParseSSHConfig(f, include), true
}
