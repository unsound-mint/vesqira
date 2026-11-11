package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeSupportAPI(t *testing.T, gotBody *string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/support/preview", func(w http.ResponseWriter, r *http.Request) {
		var b bytes.Buffer
		_, _ = b.ReadFrom(r.Body)
		*gotBody = b.String()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"abc","bytes":5,"files":[{"name":"README.txt","bytes":5,"text":"hello"}]}`))
	})
	mux.HandleFunc("GET /api/support/bundle/abc", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ZIPBYTES")) })
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func TestSupportBundleCommandExitCodes(t *testing.T) {
	var body string
	ts := fakeSupportAPI(t, &body)
	out := filepath.Join(t.TempDir(), "b.zip")
	run := func(args ...string) (int, string, string) {
		var so, se bytes.Buffer
		code := runSupport(args, &so, &se)
		return code, so.String(), se.String()
	}

	if code, _, _ := run("--bogus"); code != 2 {
		t.Errorf("unknown flag: %d, want 2", code)
	}
	if code, _, _ := run("--api", ts.URL, "extra"); code != 2 {
		t.Errorf("stray argument: %d, want 2", code)
	}
	if code, _, _ := run("--api", "http://192.0.2.1:8787"); code != 2 {
		t.Errorf("non-loopback api: %d, want 2", code)
	}
	if code, _, _ := run("--api", "http://127.0.0.1:1"); code != 1 {
		t.Errorf("app not running: %d, want 1", code)
	}

	// preview only: nothing is written
	code, stdout, _ := run("--api", ts.URL, "--show", "--include-dataset-names")
	if code != 0 || !strings.Contains(stdout, "README.txt") || !strings.Contains(stdout, "hello") || !strings.Contains(stdout, "Nothing was saved") {
		t.Fatalf("preview: %d %q", code, stdout)
	}
	if body != `{"includeDatasetNames":true}`+"\n" && body != `{"includeDatasetNames":true}` {
		t.Errorf("request body %q", body)
	}

	if code, _, errOut := run("--api", ts.URL, "--out", out); code != 0 {
		t.Fatalf("save: %d %s", code, errOut)
	}
	if b, _ := os.ReadFile(out); string(b) != "ZIPBYTES" {
		t.Errorf("saved %q", b)
	}
	if code, _, _ := run("--api", ts.URL, "--out", out); code != 1 {
		t.Errorf("overwrite: %d, want 1", code)
	}
}
