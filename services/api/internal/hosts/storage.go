package hosts

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"math"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Disk and quota on a host (ADR 0044). Everything here is read-only and runs as the user on the host, never with sudo. The
// output of a remote tool is untrusted text: it is bounded, parsed defensively, and a tool whose output does not parse is
// reported as unavailable, not guessed at. Sizes are powers of 1024, which is what each of these tools prints.

const (
	storageTimeout = 60 * time.Second
	// maxQuotaRoots bounds how many of a host's roots are asked about (one lfs and one df call each).
	maxQuotaRoots = 8
	maxSectionLen = 16 << 10
	maxRawLen     = 200
	maxBytes      = int64(1) << 60
)

// QuotaEntry is one line of a quota report. Soft and Hard are nil where the tool says there is no limit.
type QuotaEntry struct {
	Scope     string `json:"scope"` // storage pool, file system or mount the line is about
	UsedBytes int64  `json:"usedBytes"`
	SoftBytes *int64 `json:"softBytes"`
	HardBytes *int64 `json:"hardBytes"`
	// Raw is the line as the tool printed it (shortened), so the numbers can be compared with what the cluster says.
	Raw string `json:"raw"`
}

// Quota is the answer of the first quota tool that gave a readable one.
type Quota struct {
	Tool    string       `json:"tool"` // beegfs-ctl | lfs quota | quota
	Command string       `json:"command"`
	Entries []QuotaEntry `json:"entries"`
}

// Filesystem is what df reported for one of the host's roots.
type Filesystem struct {
	Root       string `json:"root"`
	Filesystem string `json:"filesystem"`
	Mount      string `json:"mount"`
	TotalBytes int64  `json:"totalBytes"`
	UsedBytes  int64  `json:"usedBytes"`
	AvailBytes int64  `json:"availBytes"`
	Raw        string `json:"raw"`
	Error      string `json:"error,omitempty"`
}

// StorageReport is one look at a host's storage. Quota is nil when no tool answered, and QuotaNote then says so in words.
type StorageReport struct {
	CheckedAt   time.Time    `json:"checkedAt"`
	Quota       *Quota       `json:"quota"`
	QuotaNote   string       `json:"quotaNote"`
	Filesystems []Filesystem `json:"filesystems"`
}

const noQuotaNote = "This site's quota tool is not available (none of beegfs-ctl, lfs quota or quota answered), so no quota is shown. Free space is the file system's, from df."

var quotaTools = []struct{ key, name, command string }{
	{"beegfs", "beegfs-ctl", "beegfs-ctl --getquota --uid $(id -u)"},
	{"lfs", "lfs quota", "lfs quota -u $USER <root>"},
	{"quota", "quota", "quota -s -w"},
}

// storageCommand is the one shell command that asks every tool in turn. Sections are separated by a line that starts with a
// random marker, so remote output cannot forge a boundary. Nothing in it comes from the caller except the quoted roots.
func storageCommand(marker string, roots []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "M=%s; T=''; command -v timeout >/dev/null 2>&1 && T='timeout 20'\n", quote(marker))
	fmt.Fprintf(&b, `if command -v beegfs-ctl >/dev/null 2>&1; then echo "$M beegfs"; $T beegfs-ctl --getquota --uid "$(id -u)" 2>&1 | head -c %d; echo; fi`+"\n", maxSectionLen)
	b.WriteString("if command -v lfs >/dev/null 2>&1; then\n")
	for i, r := range roots {
		fmt.Fprintf(&b, `echo "$M lfs %d"; $T lfs quota -u "$(id -un)" %s 2>&1 | head -c %d; echo`+"\n", i, quote(r), maxSectionLen)
	}
	b.WriteString("fi\n")
	fmt.Fprintf(&b, `if command -v quota >/dev/null 2>&1; then echo "$M quota"; $T quota -s -w 2>&1 | head -c %d; echo; fi`+"\n", maxSectionLen)
	for i, r := range roots {
		fmt.Fprintf(&b, `echo "$M df %d"; $T df -P -k -- %s 2>&1 | head -c %d; echo`+"\n", i, quote(r), maxSectionLen)
	}
	b.WriteString(`echo "$M end"` + "\n")
	return b.String()
}

// Storage asks the host for its quota (first tool that answers readably) and for df of each declared root, in one ssh call.
func (m *Manager) Storage(ctx context.Context, h Host) (StorageReport, error) {
	var nb [8]byte
	_, _ = rand.Read(nb[:])
	marker := "@@vq" + hex.EncodeToString(nb[:])
	roots := h.Roots
	if len(roots) > maxQuotaRoots {
		roots = roots[:maxQuotaRoots]
	}
	out, err := m.RunShell(ctx, h, storageCommand(marker, roots), nil, storageTimeout)
	if err != nil {
		return StorageReport{}, err
	}
	return ParseStorage(string(out), marker, roots, time.Now()), nil
}

// splitSections cuts the combined output at lines that begin with marker. A section is keyed by its header (the text after
// the marker); output before the first marker is dropped.
func splitSections(out, marker string) map[string]string {
	secs := map[string]string{}
	var key string
	var buf []string
	flush := func() {
		if key != "" {
			if _, dup := secs[key]; !dup {
				secs[key] = strings.Join(buf, "\n")
			}
		}
		buf = nil
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, marker+" ") {
			flush()
			key = strings.TrimSpace(strings.TrimPrefix(line, marker+" "))
			continue
		}
		if key != "" {
			buf = append(buf, strings.TrimRight(line, "\r"))
		}
	}
	flush()
	return secs
}

// ParseStorage turns the output of storageCommand into a report. The first quota tool whose output parses wins; a tool that
// printed something unreadable is skipped, not repaired.
func ParseStorage(out, marker string, roots []string, now time.Time) StorageReport {
	secs := splitSections(out, marker)
	rep := StorageReport{CheckedAt: now.UTC(), Filesystems: []Filesystem{}}
	for _, tool := range quotaTools {
		var entries []QuotaEntry
		switch tool.key {
		case "beegfs":
			if text, ok := secs["beegfs"]; ok {
				entries, _ = ParseBeeGFSQuota(text)
			}
		case "lfs":
			for i, r := range roots {
				if text, ok := secs["lfs "+strconv.Itoa(i)]; ok {
					if es, err := ParseLfsQuota(text); err == nil {
						for j := range es {
							if es[j].Scope == "" {
								es[j].Scope = r
							}
						}
						entries = append(entries, es...)
					}
				}
			}
		case "quota":
			if text, ok := secs["quota"]; ok {
				entries, _ = ParseQuotaCmd(text)
			}
		}
		if len(entries) > 0 {
			rep.Quota = &Quota{Tool: tool.name, Command: tool.command, Entries: entries}
			break
		}
	}
	if rep.Quota == nil {
		rep.QuotaNote = noQuotaNote
	}
	for i, r := range roots {
		fs := Filesystem{Root: r}
		if text, ok := secs["df "+strconv.Itoa(i)]; !ok {
			fs.Error = "df gave no answer"
		} else if parsed, err := ParseDF(text); err != nil {
			fs.Error = err.Error()
		} else {
			parsed.Root = r
			fs = parsed
		}
		rep.Filesystems = append(rep.Filesystems, fs)
	}
	return rep
}

// parseAmount reads "395.20 GiB", "4096M", "12G", "0 Byte" and a bare number (in unit bare) into bytes. A trailing "*"
// (over quota) is ignored. "unlimited" and the like are not amounts.
func parseAmount(s string, bare int64) (int64, bool) {
	s = strings.ToLower(strings.ReplaceAll(strings.TrimSuffix(strings.TrimSpace(s), "*"), " ", ""))
	if s == "" || len(s) > 32 {
		return 0, false
	}
	i := 0
	for i < len(s) && (s[i] >= '0' && s[i] <= '9' || s[i] == '.') {
		i++
	}
	num, unit := s[:i], s[i:]
	var mul int64
	switch unit {
	case "":
		mul = bare
	case "b", "byte", "bytes":
		mul = 1
	case "k", "kb", "kib":
		mul = 1 << 10
	case "m", "mb", "mib":
		mul = 1 << 20
	case "g", "gb", "gib":
		mul = 1 << 30
	case "t", "tb", "tib":
		mul = 1 << 40
	case "p", "pb", "pib":
		mul = 1 << 50
	default:
		return 0, false
	}
	v, err := strconv.ParseFloat(num, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v*float64(mul) > float64(maxBytes) {
		return 0, false
	}
	return int64(math.Round(v * float64(mul))), true
}

// limit reads a limit column: a size, or nothing ("unlimited", "none", "-", 0 for the tools where 0 means unset).
func limit(s string, bare int64, zeroIsNone bool) (*int64, bool) {
	b, ok := parseAmount(s, bare)
	if !ok {
		switch strings.ToLower(strings.TrimSpace(s)) {
		case "unlimited", "none", "-", "":
			return nil, true
		}
		return nil, false
	}
	if zeroIsNone && b == 0 {
		return nil, true
	}
	return &b, true
}

func raw(line string) string {
	line = strings.Join(strings.Fields(line), " ")
	if len(line) > maxRawLen {
		line = line[:maxRawLen]
	}
	return line
}

var beegfsPool = regexp.MustCompile(`(?i)^\s*Quota information for storage pool\s+(.+?)\s*\(ID:\s*(\d+)\)`)

// ParseBeeGFSQuota reads `beegfs-ctl --getquota --uid N`: one table per storage pool whose rows are
// "name | id || used | hard || files used | files hard". Only the size columns are read.
func ParseBeeGFSQuota(text string) ([]QuotaEntry, error) {
	var out []QuotaEntry
	pool := ""
	for _, line := range strings.Split(text, "\n") {
		if m := beegfsPool.FindStringSubmatch(line); m != nil {
			pool = "storage pool " + m[1]
			continue
		}
		parts := strings.Split(line, "||")
		if len(parts) < 2 {
			continue
		}
		sz := strings.Split(parts[1], "|")
		if len(sz) != 2 {
			continue
		}
		used, ok := parseAmount(sz[0], 1)
		if !ok {
			continue // the header row ("used | hard") and anything else that is not a number
		}
		hard, ok := limit(sz[1], 1, false)
		if !ok {
			continue
		}
		out = append(out, QuotaEntry{Scope: pool, UsedBytes: used, HardBytes: hard, Raw: raw(line)})
		if len(out) >= 64 {
			break
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no quota rows in the output")
	}
	return out, nil
}

// ParseLfsQuota reads `lfs quota -u USER ROOT`: columns "Filesystem kbytes quota limit grace files quota limit grace", the
// file system name alone on its line when it is long. kbytes are KiB; 0 in quota or limit means none is set.
func ParseLfsQuota(text string) ([]QuotaEntry, error) {
	lines := strings.Split(text, "\n")
	var out []QuotaEntry
	for i := 0; i < len(lines); i++ {
		f := strings.Fields(lines[i])
		rawLine := lines[i]
		if len(f) == 1 && i+1 < len(lines) { // wrapped: the name, then the numbers on the next line
			if g := strings.Fields(lines[i+1]); len(g) >= 4 {
				f, rawLine = append(f, g...), lines[i]+" "+lines[i+1]
				i++
			}
		}
		if len(f) < 4 || strings.HasPrefix(strings.ToLower(f[0]), "disk") || strings.EqualFold(f[0], "filesystem") {
			continue
		}
		used, ok := parseAmount(f[1], 1<<10)
		if !ok || !isDigits(strings.TrimSuffix(f[1], "*")) {
			continue
		}
		soft, ok1 := limit(f[2], 1<<10, true)
		hard, ok2 := limit(f[3], 1<<10, true)
		if !ok1 || !ok2 || !isDigits(f[2]) || !isDigits(f[3]) {
			continue
		}
		out = append(out, QuotaEntry{Scope: f[0], UsedBytes: used, SoftBytes: soft, HardBytes: hard, Raw: raw(rawLine)})
		if len(out) >= 64 {
			break
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no quota rows in the output")
	}
	return out, nil
}

// ParseQuotaCmd reads `quota -s -w`: "Filesystem space quota limit [grace] files quota limit [grace]" with human sizes
// (2048M, 12G). The grace column may be empty, which shifts everything after it, so only the first four columns are read.
func ParseQuotaCmd(text string) ([]QuotaEntry, error) {
	lines := strings.Split(text, "\n")
	var out []QuotaEntry
	for i := 0; i < len(lines); i++ {
		f := strings.Fields(lines[i])
		rawLine := lines[i]
		if len(f) == 1 && i+1 < len(lines) {
			if g := strings.Fields(lines[i+1]); len(g) >= 3 {
				f, rawLine = append(f, g...), lines[i]+" "+lines[i+1]
				i++
			}
		}
		if len(f) < 4 || strings.HasPrefix(strings.ToLower(f[0]), "disk") || strings.EqualFold(f[0], "filesystem") {
			continue
		}
		used, ok := parseAmount(f[1], 1<<10)
		soft, ok1 := limit(f[2], 1<<10, true)
		hard, ok2 := limit(f[3], 1<<10, true)
		if !ok || !ok1 || !ok2 || !startsWithDigit(f[1]) {
			continue
		}
		out = append(out, QuotaEntry{Scope: f[0], UsedBytes: used, SoftBytes: soft, HardBytes: hard, Raw: raw(rawLine)})
		if len(out) >= 64 {
			break
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no quota rows in the output")
	}
	return out, nil
}

func isDigits(s string) bool {
	s = strings.TrimSuffix(s, "*")
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func startsWithDigit(s string) bool { return s != "" && s[0] >= '0' && s[0] <= '9' }

var dfRow = regexp.MustCompile(`^(.+?)\s+(\d+)\s+(\d+)\s+(\d+)\s+(\d+)%\s+(\S.*)$`)

// ParseDF reads `df -P -k`: "Filesystem 1024-blocks Used Available Capacity Mounted-on".
func ParseDF(text string) (Filesystem, error) {
	for _, line := range strings.Split(text, "\n") {
		m := dfRow.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		total, e1 := strconv.ParseInt(m[2], 10, 64)
		used, e2 := strconv.ParseInt(m[3], 10, 64)
		avail, e3 := strconv.ParseInt(m[4], 10, 64)
		const maxKiB = int64(1) << 50
		if e1 != nil || e2 != nil || e3 != nil || total < 0 || total > maxKiB || used < 0 || used > maxKiB || avail < 0 || avail > maxKiB {
			return Filesystem{}, fmt.Errorf("df printed numbers that are not usable")
		}
		return Filesystem{Filesystem: m[1], Mount: m[6], TotalBytes: total << 10, UsedBytes: used << 10, AvailBytes: avail << 10, Raw: raw(line)}, nil
	}
	return Filesystem{}, fmt.Errorf("df did not print a usable line")
}

// ---- folder sizes ----

// DiskUsage is the size of one folder as du reports it. Partial means du could not read some entries, so the size is a
// lower bound. KiB is du's own unit (`du -sk`): disk blocks used, not the sum of file lengths.
type DiskUsage struct {
	Path       string    `json:"path"`
	KiB        int64     `json:"kib"`
	Bytes      int64     `json:"bytes"`
	Partial    bool      `json:"partial"`
	MeasuredAt time.Time `json:"measuredAt"`
}

const (
	duTimeout     = 90 * time.Second
	duRemoteLimit = 80
)

// ValidFolder reports whether p is a clean absolute path that may be measured on this host: inside one of its roots.
func (h Host) ValidFolder(p string) bool {
	return len(p) <= 1000 && !strings.ContainsAny(p, "\x00\n\r") && path.IsAbs(p) && h.InRoots(p)
}

func duCommand(p string) string {
	return fmt.Sprintf("T=''; command -v timeout >/dev/null 2>&1 && T='timeout %d'\n$T du -sk -- %s 2>/dev/null; echo \"@@rc $?\"\n", duRemoteLimit, quote(p))
}

// DiskUsage measures a folder inside the host's roots with `du -sk`, read-only and bounded in time.
func (m *Manager) DiskUsage(ctx context.Context, h Host, p string) (DiskUsage, error) {
	if !h.ValidFolder(p) {
		return DiskUsage{}, fmt.Errorf("%w: give a folder inside one of the host's source folders", ErrInvalid)
	}
	out, err := m.RunShell(ctx, h, duCommand(p), nil, duTimeout)
	if err != nil {
		return DiskUsage{}, err
	}
	u, err := ParseDU(string(out))
	if err != nil {
		return DiskUsage{}, err
	}
	u.Path, u.MeasuredAt = p, time.Now().UTC()
	return u, nil
}

// ParseDU reads the output of duCommand: du's "KiB<TAB>path" line and the exit status line.
func ParseDU(out string) (DiskUsage, error) {
	rc := -1
	var kib int64 = -1
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if r, ok := strings.CutPrefix(line, "@@rc "); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(r)); err == nil {
				rc = n
			}
			continue
		}
		if kib >= 0 {
			continue
		}
		if f, _, ok := strings.Cut(line, "\t"); ok && isDigits(f) && len(f) <= 16 {
			if n, err := strconv.ParseInt(f, 10, 64); err == nil && n <= int64(1)<<50 {
				kib = n
			}
		}
	}
	switch {
	case rc == 124 || rc == 137:
		return DiskUsage{}, fmt.Errorf("du did not finish in %d s on the host; the folder is large or the file system is slow, so no size is shown", duRemoteLimit)
	case kib < 0:
		return DiskUsage{}, fmt.Errorf("du gave no size (the folder may not exist or may not be readable)")
	}
	return DiskUsage{KiB: kib, Bytes: kib << 10, Partial: rc != 0}, nil
}
