package server

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSSHConfigRouteOffersAliasesOnlyAndNothingWhenThereIsNoFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cfg := filepath.Join(dir, "config")
	if err := os.WriteFile(cfg, []byte("Host cluster\n  HostName login.example.org\n  IdentityFile ~/.ssh/id_secret\n  ProxyCommand nc %h %p\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	call := func(path string) (found bool, body string) {
		w := httptest.NewRecorder()
		(&Server{SSHConfig: path}).sshConfigHosts(w, httptest.NewRequest("GET", "/api/hosts/ssh-config", nil))
		var r struct {
			Found bool
			Hosts []map[string]string
		}
		if err := json.Unmarshal(w.Body.Bytes(), &r); err != nil {
			t.Fatal(err)
		}
		return r.Found, w.Body.String()
	}
	found, body := call(cfg)
	if !found || !strings.Contains(body, `"alias":"cluster"`) || !strings.Contains(body, "login.example.org") {
		t.Fatalf("%v %s", found, body)
	}
	for _, leaked := range []string{"id_secret", "ProxyCommand", "nc %h"} {
		if strings.Contains(body, leaked) {
			t.Errorf("%q leaked from the ssh config", leaked)
		}
	}
	if found, body := call(filepath.Join(dir, "nope")); found || !strings.Contains(body, `"hosts":[]`) {
		t.Errorf("missing file: %v %s", found, body)
	}
	if found, _ := call(""); found {
		t.Error("an empty path must offer nothing")
	}
}
