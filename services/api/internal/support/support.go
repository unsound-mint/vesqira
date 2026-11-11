// Package support builds the support bundle (ADR 0055): a zip a person can attach to a bug report. It holds versions, capabilities,
// counts and recent log lines, and nothing that identifies the research: no file contents, no hostnames, no keys or tokens, no
// dataset names unless the person asked for them. Everything is plain text so the person can read every file before saving.
//
// The package takes what the server gathered as plain values and does the redaction itself; it imports no other internal
// package, so it cannot reach the catalog, the hosts or any secret by itself.
package support

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Limits on what a bundle can hold; the server asks for the last N and this is the ceiling.
const (
	MaxLogLines   = 200
	MaxAuditLines = 100
)

// Host is a configured host as the bundle shows it: the alias the person chose and whether it answered, never its address.
type Host struct {
	Alias     string
	Reachable string // "yes", "no" or "unknown"
}

// AuditEntry is one audit record reduced to when and what kind of action; targets and details are never included.
type AuditEntry struct {
	At     time.Time
	Action string
	Kind   string
}

// Input is everything the bundle is made from.
type Input struct {
	Now          time.Time
	AppVersion   string
	Protocol     int
	Schema       int
	Capabilities any // the GET /api/capabilities body
	OS, Arch     string
	GoVersion    string
	Python       map[string]string // python, numpy, astropy versions of the worker; empty when it could not be asked
	APILog       []string          // JSON lines as the API logged them
	WorkerLog    []string          // lines the scientific workers wrote to stderr
	TableCounts  map[string]int64  // table name -> number of rows
	Hosts        []Host
	JobStates    map[string]int64
	Audit        []AuditEntry
	Home         string   // the user's home directory, replaced by ~ wherever it appears
	Secrets      []string // exact secret values known to the server (the publish key); removed wherever they appear
	DatasetNames []string // names and file names of the catalog's datasets, removed unless IncludeDatasetNames

	IncludeDatasetNames bool
}

// File is one file of the bundle.
type File struct {
	Name string
	Data []byte
}

// Bundle is the files of a bundle, in the order they are listed.
type Bundle struct{ Files []File }

var (
	homeLike    = regexp.MustCompile(`(?:/home|/Users)/[^/\s"'<>:;,)]+`)
	rootHome    = regexp.MustCompile(`/root(/|\b)`)
	sshPath     = regexp.MustCompile(`(~|\S*)/\.ssh/[^\s"'<>:;,)]*`)
	sshConfig   = regexp.MustCompile(`\.ssh/\S*`)
	assignment  = regexp.MustCompile(`(?i)\b(token|api[_-]?key|key|secret|password|passwd|passphrase|authorization|cookie)(["']?\s*[=:]\s*["']?)([^\s"',;&]+)`)
	bearer      = regexp.MustCompile(`(?i)\b(bearer)\s+([^\s"',;]+)`)
	opaque      = regexp.MustCompile(`[A-Za-z0-9_\-+=]{32,}`)
	pemBlock    = regexp.MustCompile(`-----BEGIN [A-Z ]*(PRIVATE KEY|CERTIFICATE)[A-Z ]*-----`)
	dataFile    = regexp.MustCompile(`[^\s"'/<>:;,)]+\.(?i:fits|fit|fts|fz|csv|dat|tsv|zip|tar|gz|h5|hdf5|npy|npz)\b`)
	requestPath = regexp.MustCompile(`/review-api/[^\s"'?]*`)
)

// redactor removes what must never leave the computer from a line of text.
type redactor struct {
	secrets []string
	home    string
	names   []string // longest first
}

func newRedactor(in Input) redactor {
	r := redactor{home: strings.TrimRight(in.Home, "/")}
	for _, s := range in.Secrets {
		if len(s) >= 4 {
			r.secrets = append(r.secrets, s)
		}
	}
	if !in.IncludeDatasetNames {
		for _, n := range in.DatasetNames {
			if len(n) >= 3 {
				r.names = append(r.names, n)
			}
		}
		sort.Slice(r.names, func(i, j int) bool { return len(r.names[i]) > len(r.names[j]) })
	}
	return r
}

// line redacts one line. Order matters: exact secrets and dataset names first (before paths are shortened, so a name inside a
// path is still found), then paths, then patterns for secrets nobody told us about.
func (r redactor) line(s string, includeNames bool) string {
	if pemBlock.MatchString(s) {
		return "<redacted: key material>"
	}
	for _, sec := range r.secrets {
		s = strings.ReplaceAll(s, sec, "<redacted>")
	}
	for _, n := range r.names {
		s = strings.ReplaceAll(s, n, "<dataset>")
	}
	s = requestPath.ReplaceAllString(s, "/review-api/<redacted>")
	s = sshPath.ReplaceAllString(s, "~/.ssh/<redacted>")
	s = sshConfig.ReplaceAllString(s, ".ssh/<redacted>")
	if r.home != "" && r.home != "/" {
		s = strings.ReplaceAll(s, r.home, "~")
	}
	s = homeLike.ReplaceAllString(s, "~")
	s = rootHome.ReplaceAllString(s, "~$1")
	if !includeNames {
		s = dataFile.ReplaceAllString(s, "<file>")
	}
	s = assignment.ReplaceAllString(s, "${1}${2}<redacted>")
	s = bearer.ReplaceAllString(s, "${1} <redacted>")
	s = opaque.ReplaceAllStringFunc(s, func(m string) string {
		if strings.Count(m, "-")+strings.Count(m, "_") > len(m)/2 { // a separator-heavy word, not a secret
			return m
		}
		return "<redacted>"
	})
	return s
}

// Build makes the bundle. The same input always gives the same files.
func Build(in Input) Bundle {
	r := newRedactor(in)
	red := func(s string) string { return r.line(s, in.IncludeDatasetNames) }
	var b Bundle
	add := func(name, text string) { b.Files = append(b.Files, File{name, []byte(text)}) }

	add("README.txt", readme(in))

	ver := map[string]any{
		"app": in.AppVersion, "protocol": in.Protocol, "schema": in.Schema,
		"os": in.OS, "arch": in.Arch, "go": in.GoVersion, "worker": orEmpty(in.Python),
		"created": in.Now.UTC().Format(time.RFC3339),
	}
	add("versions.json", pretty(ver))
	add("capabilities.json", pretty(in.Capabilities))

	add("api-log.jsonl", apiLog(in.APILog, red))
	add("worker-stderr.txt", workerLog(in.WorkerLog, red))

	counts := map[string]any{"schema": in.Schema, "tables": orEmpty64(in.TableCounts)}
	add("catalog-counts.json", pretty(counts))

	var hs strings.Builder
	hosts := append([]Host(nil), in.Hosts...)
	sort.Slice(hosts, func(i, j int) bool { return hosts[i].Alias < hosts[j].Alias })
	fmt.Fprintf(&hs, "%d configured host(s). Alias and whether it last answered; addresses, users, paths and VPN commands are never included.\n", len(hosts))
	for _, h := range hosts {
		fmt.Fprintf(&hs, "%s\treachable=%s\n", red(h.Alias), h.Reachable)
	}
	add("hosts.txt", hs.String())

	add("job-states.json", pretty(orEmpty64(in.JobStates)))

	var as strings.Builder
	n := len(in.Audit)
	if n > MaxAuditLines {
		n = MaxAuditLines
	}
	fmt.Fprintf(&as, "The last %d audit entries: when, what happened, to which kind of thing. Targets and details are never included.\n", n)
	for _, e := range in.Audit[:n] {
		fmt.Fprintf(&as, "%s\t%s\t%s\n", e.At.UTC().Format(time.RFC3339), red(e.Action), red(e.Kind))
	}
	add("audit-kinds.txt", as.String())

	if in.IncludeDatasetNames {
		names := append([]string(nil), in.DatasetNames...)
		sort.Strings(names)
		var ds strings.Builder
		ds.WriteString("Dataset names and file names, included because you asked for them.\n")
		for _, n := range names {
			ds.WriteString(r.line(n, true) + "\n")
		}
		add("dataset-names.txt", ds.String())
	}
	return b
}

func readme(in Input) string {
	names := "Dataset names and file names were removed from every line."
	if in.IncludeDatasetNames {
		names = "Dataset names and file names are included (dataset-names.txt), because you ticked the box."
	}
	return strings.Join([]string{
		"Vesqira support bundle",
		"",
		"Made " + in.Now.UTC().Format(time.RFC3339) + " by Vesqira " + in.AppVersion + ". Every file is plain text; open them before you send this.",
		"",
		"Included: versions of the app, protocol, catalog schema and the scientific worker's Python, astropy and numpy; the features this server was started with;",
		"the most recent failed or slow API requests (method, path, status, size, duration, request id); the scientific workers' own failure lines, each with the",
		"request id it belongs to; how many rows each catalog table has; configured hosts by alias with reachability; how many jobs are in each state; and the kinds and",
		"times of the latest audit entries.",
		"",
		"Never included: file contents, keys, tokens, review links, ssh configuration, the publish key, host addresses or user names, paths (your home folder is shown as ~).",
		names,
		"",
		"A request id in an error message (Reference: ...) matches the \"id\" of one line in api-log.jsonl and the request= field of the worker lines.",
		"",
	}, "\n")
}

func orEmpty(m map[string]string) map[string]string {
	if m == nil {
		return map[string]string{}
	}
	return m
}

func orEmpty64(m map[string]int64) map[string]int64 {
	if m == nil {
		return map[string]int64{}
	}
	return m
}

func pretty(v any) string {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return "{}\n"
	}
	return string(b) + "\n"
}

// logFields is every field of an API log line the bundle keeps; the rest of a line is dropped, not redacted.
var logFields = []string{"time", "level", "msg", "id", "method", "path", "status", "bytes", "ms"}

func apiLog(lines []string, red func(string) string) string {
	if len(lines) > MaxLogLines {
		lines = lines[len(lines)-MaxLogLines:]
	}
	var out strings.Builder
	for _, l := range lines {
		var m map[string]any
		if json.Unmarshal([]byte(l), &m) != nil {
			continue
		}
		if lvl, _ := m["level"].(string); strings.EqualFold(lvl, "DEBUG") {
			continue
		}
		kept := map[string]any{}
		for _, k := range logFields {
			if v, ok := m[k]; ok {
				if s, isStr := v.(string); isStr {
					v = red(s)
				}
				kept[k] = v
			}
		}
		var one bytes.Buffer
		enc := json.NewEncoder(&one)
		enc.SetEscapeHTML(false) // keep <redacted> readable; map keys are sorted, so the output is stable
		_ = enc.Encode(kept)
		out.Write(one.Bytes())
	}
	return out.String()
}

var workerLine = regexp.MustCompile(`^(worker|analysis) request=([A-Za-z0-9._-]{8,64}|-) op=([a-z_]{1,40}) (.*)$`)

func workerLog(lines []string, red func(string) string) string {
	var out strings.Builder
	left := 0
	var kept []string
	for _, l := range lines {
		m := workerLine.FindStringSubmatch(l)
		if m == nil {
			left++ // free text (a traceback, a library warning) can hold anything: counted, not shown
			continue
		}
		kept = append(kept, fmt.Sprintf("%s request=%s op=%s %s", m[1], m[2], m[3], red(m[4])))
	}
	if len(kept) > MaxLogLines {
		kept = kept[len(kept)-MaxLogLines:]
	}
	fmt.Fprintf(&out, "Worker failure and slow-request lines, tagged with the API request id. %d other stderr line(s) were left out because free text can hold file names.\n", left)
	for _, k := range kept {
		out.WriteString(k + "\n")
	}
	return out.String()
}

// Zip returns the bundle as a zip with fixed timestamps, so the same files give the same bytes.
func (b Bundle) Zip() ([]byte, error) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range b.Files {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: "vesqira-support/" + f.Name, Method: zip.Deflate, Modified: time.Unix(0, 0).UTC()})
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(f.Data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
