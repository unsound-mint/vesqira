package server

import (
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// Pushed documents count towards the workspace's byte quota as pictures do: an item that would take the workspace past it is
// refused with 507 and the current use.
func TestPushedDocumentsCountTowardsTheQuota(t *testing.T) {
	g, _, ps := pushGroup(t)
	full, _ := g.setupAdmin("ada")
	token := g.mintPush(full)
	pic, sha := cleanPNG(t, 16, 12)
	if w := putAsset(g, token, sha, pic); w.Code != 201 {
		t.Fatalf("asset: %d", w.Code)
	}
	if w := g.bearer(token, req{method: "POST", path: "/api/push/items", body: figureItem(t, sha, nil, nil, "small")}); w.Code != 201 {
		t.Fatalf("a first item: %d %s", w.Code, w.Body)
	}
	if ps.DocBytes(workspace.Local) <= 0 {
		t.Fatal("the documents' bytes are not counted")
	}
	// room for what is stored and a little more, not for a 64 KiB caption
	if err := g.acc.SetQuota(workspace.Local, int64(len(pic))+ps.DocBytes(workspace.Local)+1024); err != nil {
		t.Fatal(err)
	}
	w := g.bearer(token, req{method: "POST", path: "/api/push/items", body: figureItem(t, sha, nil, nil, strings.Repeat("x", 64<<10))})
	if w.Code != 507 || !strings.Contains(w.Body.String(), "may keep") {
		t.Fatalf("a document past the quota: %d %s", w.Code, w.Body)
	}
}
