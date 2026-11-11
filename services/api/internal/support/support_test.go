package support

import (
	"archive/zip"
	"bytes"
	"io"
	"sort"
	"strings"
	"testing"
	"time"
)

// Fixtures that must never appear in a bundle, whatever file or line they were found in.
const (
	reviewToken = "q8Zr1mXk4vB7nT2cLw9sYdE5uHf3JaP0gRoVxKiN6bM"       // 43 characters, the shape of a review link token
	publishKey  = "pk-live-7f3a9c1e5b2d4f68a0c9e1d3b5f7a2c4"          // the publisher key for the publish server
	sshKeyPath  = "/home/alice/.ssh/id_ed25519_cluster"               // an ssh private key path
	homePath    = "/home/alice/Projects/hi-survey"                    // somewhere under the user's home
	datasetName = "NGC1333-HI-cube"                                   // a dataset the researcher has not published
	dataFileOn  = "/data/survey/L1688_secret_field.fits"              // a data file outside home
	hostAddr    = "login3.private-cluster.example.org"                // a host address
	bearerValue = "Bearer abcdefghijklmnopqrstuvwxyz0123456789ABCDEF" // an authorization header
)

func fixtureInput() Input {
	return Input{
		Now: time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC), AppVersion: "1.2.3", Protocol: 1, Schema: 17,
		Capabilities: map[string]any{"features": map[string]bool{"planes": true}},
		OS:           "linux", Arch: "amd64", GoVersion: "go1.24",
		Python: map[string]string{"python": "3.13.1", "astropy": "7.0", "numpy": "2.2"},
		APILog: []string{
			`{"time":"2026-11-12T09:00:00Z","level":"INFO","msg":"request","id":"abc12345def","method":"GET","path":"/api/datasets/3/plane","status":500,"bytes":20,"ms":12,"extra":"` + sshKeyPath + `"}`,
			`{"time":"2026-11-12T09:00:01Z","level":"INFO","msg":"request","id":"zzz12345def","method":"GET","path":"/review-api/` + reviewToken + `","status":404,"bytes":2,"ms":1}`,
			`{"time":"2026-11-12T09:00:02Z","level":"DEBUG","msg":"request","id":"dbg12345def","method":"GET","path":"/api/roots","status":200,"bytes":2,"ms":1}`,
			`not json ` + publishKey,
		},
		WorkerLog: []string{
			"worker request=abc12345def op=plane failed cannot read " + homePath + "/" + datasetName + ".fits: " + bearerValue,
			"worker request=- op=spectrum slow 4.2 s reading " + dataFileOn,
			"Traceback (most recent call last): File " + sshKeyPath + " key=" + publishKey,
			"analysis request=ana12345def op=moment failed token=" + reviewToken,
		},
		TableCounts: map[string]int64{"datasets": 4, "jobs": 2},
		Hosts:       []Host{{Alias: "cluster-a", Reachable: "yes"}},
		JobStates:   map[string]int64{"running": 1, "failed": 2},
		Audit:       []AuditEntry{{At: time.Date(2026, 11, 12, 8, 0, 0, 0, time.UTC), Action: "job.submit", Kind: "job"}},
		Home:        "/home/alice", Secrets: []string{publishKey}, DatasetNames: []string{datasetName, "L1688_secret_field.fits"},
	}
}

func all(b Bundle) string {
	var sb strings.Builder
	for _, f := range b.Files {
		sb.WriteString("== " + f.Name + "\n" + string(f.Data))
	}
	return sb.String()
}

func TestBundleHoldsTheListedFilesAndNoSecrets(t *testing.T) {
	b := Build(fixtureInput())
	var names []string
	for _, f := range b.Files {
		names = append(names, f.Name)
	}
	want := []string{"README.txt", "versions.json", "capabilities.json", "api-log.jsonl", "worker-stderr.txt", "catalog-counts.json", "hosts.txt", "job-states.json", "audit-kinds.txt"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("files %v, want %v", names, want)
	}
	text := all(b)
	for _, secret := range []string{reviewToken, publishKey, sshKeyPath, ".ssh/id_", homePath, "/home/alice", datasetName, "L1688_secret_field", dataFileOn, hostAddr, "abcdefghijklmnopqrstuvwxyz0123456789"} {
		if strings.Contains(text, secret) {
			t.Errorf("bundle contains %q:\n%s", secret, text)
		}
	}
	// the zip holds the same bytes
	z, err := b.Zip()
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(z), int64(len(z)))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != len(want) {
		t.Fatalf("zip has %d files", len(zr.File))
	}
	for _, f := range zr.File {
		rc, _ := f.Open()
		data, _ := io.ReadAll(rc)
		rc.Close()
		for _, secret := range []string{reviewToken, publishKey, "/home/alice", datasetName} {
			if bytes.Contains(data, []byte(secret)) {
				t.Errorf("%s in the zip contains %q", f.Name, secret)
			}
		}
	}
}

func TestWhatIsKept(t *testing.T) {
	b := Build(fixtureInput())
	byName := map[string]string{}
	for _, f := range b.Files {
		byName[f.Name] = string(f.Data)
	}
	log := byName["api-log.jsonl"]
	if !strings.Contains(log, `"id":"abc12345def"`) || !strings.Contains(log, `"status":500`) {
		t.Errorf("failure line missing:\n%s", log)
	}
	if strings.Contains(log, "dbg12345def") || strings.Contains(log, "extra") || strings.Contains(log, "not json") {
		t.Errorf("debug lines, unlisted fields or non-JSON lines kept:\n%s", log)
	}
	if !strings.Contains(log, "/review-api/<redacted>") {
		t.Errorf("review path not redacted:\n%s", log)
	}
	w := byName["worker-stderr.txt"]
	if !strings.Contains(w, "request=abc12345def op=plane failed") || !strings.Contains(w, "request=- op=spectrum slow") || !strings.Contains(w, "1 other stderr line(s) were left out") {
		t.Errorf("worker lines:\n%s", w)
	}
	if strings.Contains(w, "Traceback") {
		t.Error("free-text stderr line kept")
	}
	if !strings.Contains(byName["hosts.txt"], "cluster-a\treachable=yes") || strings.Contains(byName["hosts.txt"], "private-cluster") {
		t.Errorf("hosts:\n%s", byName["hosts.txt"])
	}
	if !strings.Contains(byName["catalog-counts.json"], `"datasets": 4`) || !strings.Contains(byName["audit-kinds.txt"], "2026-11-12T08:00:00Z\tjob.submit\tjob") {
		t.Errorf("counts or audit missing")
	}
	if !strings.Contains(byName["versions.json"], `"astropy": "7.0"`) {
		t.Error("worker versions missing")
	}
}

func TestDatasetNamesOnlyWhenAsked(t *testing.T) {
	in := fixtureInput()
	in.IncludeDatasetNames = true
	b := Build(in)
	text := all(b)
	if !strings.Contains(text, "dataset-names.txt") || !strings.Contains(text, datasetName) {
		t.Fatalf("names requested but absent:\n%s", text)
	}
	// still no secrets, and the home folder is still shown as ~
	for _, secret := range []string{reviewToken, publishKey, sshKeyPath, "/home/alice"} {
		if strings.Contains(text, secret) {
			t.Errorf("bundle with names contains %q", secret)
		}
	}
	if strings.Contains(all(Build(fixtureInput())), "dataset-names.txt") {
		t.Error("names file present without the box")
	}
}

func TestBundleIsDeterministicAndBounded(t *testing.T) {
	in := fixtureInput()
	in.Audit = nil
	for i := 0; i < MaxAuditLines+50; i++ {
		in.Audit = append(in.Audit, AuditEntry{At: in.Now, Action: "x", Kind: "y"})
	}
	for i := 0; i < MaxLogLines+50; i++ {
		in.WorkerLog = append(in.WorkerLog, "worker request=abc12345def op=plane failed x")
	}
	a, _ := Build(in).Zip()
	b, _ := Build(in).Zip()
	if !bytes.Equal(a, b) {
		t.Error("the same input gave different zips")
	}
	var lines []string
	for _, f := range Build(in).Files {
		if f.Name == "audit-kinds.txt" || f.Name == "worker-stderr.txt" {
			lines = append(lines, f.Name+":"+itoa(strings.Count(string(f.Data), "\n")))
		}
	}
	sort.Strings(lines)
	if lines[0] != "audit-kinds.txt:"+itoa(MaxAuditLines+1) || lines[1] != "worker-stderr.txt:"+itoa(MaxLogLines+1) {
		t.Errorf("not bounded: %v", lines)
	}
}

func itoa(n int) string {
	return strings.TrimSpace(strings.Repeat(" ", 0) + string(rune('0'+n/100%10)) + string(rune('0'+n/10%10)) + string(rune('0'+n%10)))
}
