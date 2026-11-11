package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/audit"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

const (
	fixReviewToken = "q8Zr1mXk4vB7nT2cLw9sYdE5uHf3JaP0gRoVxKiN6bM"
	fixPublishKey  = "pk-live-7f3a9c1e5b2d4f68a0c9e1d3b5f7a2c4"
	fixSSHKeyPath  = "/home/alice/.ssh/id_ed25519_cluster"
	fixHostAddr    = "login3.private-cluster.example.org"
	fixDatasetName = "NGC1333-HI-cube"
)

func supportServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	cat, err := catalog.Open("file:" + filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cat.Close() })
	keyFile := filepath.Join(dir, "publish.key")
	if err := os.WriteFile(keyFile, []byte(fixPublishKey+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	if _, err := cat.Upsert(catalog.Dataset{Path: filepath.Join(home, "data", fixDatasetName+".fits"), Name: fixDatasetName, SizeBytes: 5, ModifiedAt: time.Now(), Header: json.RawMessage(`{}`)}); err != nil {
		t.Fatal(err)
	}
	hs, err := hosts.Open(cat.DB())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hs.Create(hosts.Host{Name: "cluster-a", Dest: fixHostAddr, Python: "python3", Roots: []string{"/data"}}); err != nil {
		t.Fatal(err)
	}
	au, err := audit.Open(cat.DB())
	if err != nil {
		t.Fatal(err)
	}
	au.Record("job.submit", "job", 7, "sbatch for "+fixDatasetName)
	s := &Server{Store: cat, PublishKeyFile: keyFile, HostDB: hs, Remote: hosts.NewManager("ssh", nil), Audit: au}
	// secrets that reached the logs
	w := apiLogTail.Writer(io.Discard)
	_, _ = w.Write([]byte(`{"time":"2026-11-12T09:00:00Z","level":"INFO","msg":"request","id":"abc12345def","method":"GET","path":"/review-api/` + fixReviewToken + `","status":404,"bytes":2,"ms":1}` + "\n"))
	_, _ = worker.StderrTail.Writer(io.Discard).Write([]byte("worker request=abc12345def op=plane failed cannot read " + home + "/data/" + fixDatasetName + ".fits via " + fixSSHKeyPath + " " + fixPublishKey + "\n"))
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

func previewBundle(t *testing.T, ts *httptest.Server, body string) (SupportPreview, []byte) {
	t.Helper()
	resp, err := http.Post(ts.URL+"/api/support/preview", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("preview: %d %s", resp.StatusCode, b)
	}
	raw, _ := io.ReadAll(resp.Body)
	conforms(t, responseSchema(t, loadSpec(t), "/api/support/preview", "POST", "application/json"), "support preview", raw)
	var p SupportPreview
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	dl, err := http.Get(ts.URL + "/api/support/bundle/" + p.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer dl.Body.Close()
	z, _ := io.ReadAll(dl.Body)
	if dl.StatusCode != 200 || dl.Header.Get("Content-Type") != "application/zip" {
		t.Fatalf("download: %d %s", dl.StatusCode, dl.Header.Get("Content-Type"))
	}
	return p, z
}

func TestSupportBundleHoldsNoSecretsAndTheDownloadIsWhatWasPreviewed(t *testing.T) {
	_, ts := supportServer(t)
	p, z := previewBundle(t, ts, `{}`)
	home, _ := os.UserHomeDir()

	zr, err := zip.NewReader(bytes.NewReader(z), int64(len(z)))
	if err != nil {
		t.Fatal(err)
	}
	if len(zr.File) != len(p.Files) {
		t.Fatalf("preview lists %d files, zip has %d", len(p.Files), len(zr.File))
	}
	for i, f := range zr.File {
		rc, _ := f.Open()
		data, _ := io.ReadAll(rc)
		rc.Close()
		if f.Name != "vesqira-support/"+p.Files[i].Name || string(data) != p.Files[i].Text {
			t.Errorf("%s differs from its preview", f.Name)
		}
		for _, secret := range []string{fixReviewToken, fixPublishKey, fixSSHKeyPath, ".ssh/id_", fixHostAddr, fixDatasetName, home + "/"} {
			if home != "" && strings.Contains(string(data), secret) {
				t.Errorf("%s contains %q", f.Name, secret)
			}
		}
	}
	text := ""
	for _, f := range p.Files {
		text += f.Name + "\n" + f.Text
	}
	for _, want := range []string{"cluster-a\treachable=unknown", `"datasets": 1`, "job.submit", `"schema"`, "request=abc12345def op=plane failed"} {
		if !strings.Contains(text, want) {
			t.Errorf("bundle lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "sbatch for") {
		t.Error("an audit detail reached the bundle")
	}

	// names only when asked
	p2, _ := previewBundle(t, ts, `{"includeDatasetNames":true}`)
	found := false
	for _, f := range p2.Files {
		if f.Name == "dataset-names.txt" && strings.Contains(f.Text, fixDatasetName) {
			found = true
		}
		if strings.Contains(f.Text, fixPublishKey) || strings.Contains(f.Text, fixReviewToken) {
			t.Errorf("%s leaks a secret when names are included", f.Name)
		}
	}
	if !found {
		t.Error("dataset-names.txt missing when asked for")
	}
}

func TestSupportPreviewExpiresAndRefusesUnknownFieldsAndIds(t *testing.T) {
	s, ts := supportServer(t)
	if resp, _ := http.Post(ts.URL+"/api/support/preview", "application/json", strings.NewReader(`{"includeEverything":true}`)); resp.StatusCode != 400 {
		t.Errorf("unknown field: %d", resp.StatusCode)
	}
	if resp, _ := http.Get(ts.URL + "/api/support/bundle/nope"); resp.StatusCode != 404 {
		t.Errorf("unknown id: %d", resp.StatusCode)
	}
	p, _ := previewBundle(t, ts, `{}`)
	s.support.mu.Lock()
	v := s.support.kept[p.ID]
	v.expires = time.Now().Add(-time.Second)
	s.support.kept[p.ID] = v
	s.support.mu.Unlock()
	if resp, _ := http.Get(ts.URL + "/api/support/bundle/" + p.ID); resp.StatusCode != 404 {
		t.Errorf("expired: %d", resp.StatusCode)
	}
}

func TestWorkloadRouteReportsClassesAndTheReasonForOneRequest(t *testing.T) {
	l := worker.NewLoad(worker.Budgets{Interactive: 1, Heavy: 1, Background: 1})
	_, release, _ := l.Acquire(worker.WithLabel(t.Context(), "relative orientation"), worker.Heavy, "analysis")
	defer release()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, r, err := l.Acquire(worker.WithRequestID(t.Context(), "slow-request-1"), worker.Heavy, "PV diagram")
		if err == nil {
			r()
		}
	}()
	ts := httptest.NewServer((&Server{Load: l}).Handler())
	defer ts.Close()
	var got WorkloadReply
	for i := 0; i < 400; i++ {
		resp, err := http.Get(ts.URL + "/api/workload?request=slow-request-1")
		if err != nil {
			t.Fatal(err)
		}
		got = WorkloadReply{}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		conforms(t, responseSchema(t, loadSpec(t), "/api/workload", "GET", "application/json"), "workload", raw)
		_ = json.Unmarshal(raw, &got)
		if got.Request != nil && got.Request.State == "waiting" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(got.Classes) != 3 || got.Classes[1].Class != "heavy" || got.Classes[1].Running != 1 || got.Classes[1].Waiting != 1 {
		t.Fatalf("%+v", got.Classes)
	}
	if got.Request == nil || got.Request.Reason != "waiting: 1 heavy analysis is running (relative orientation); interactive viewing is unaffected" {
		t.Fatalf("%+v", got.Request)
	}
	release()
	<-done
}
