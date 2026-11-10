package server

import (
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/publish"
	"github.com/unsound-mint/vesqira/services/api/internal/reports"
)

// Pages, front matter, quoted comments, deletions and withdrawing, through both servers: only the date and summary
// of the front matter leave the laptop; a reader deleting their own comment and the owner deleting one both reach the
// owner's copy; a withdrawn report is gone for readers, its comments are gone on both sides, and the draft can then
// be deleted or published again from revision 1.
func TestReportPagesQuotesDeletionsAndWithdraw(t *testing.T) {
	base := t.TempDir()
	cat, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	bs, _ := boards.Open(cat.DB(), base)
	rp, _ := reports.Open(cat.DB(), base)
	srv := &Server{Store: cat, Boards: bs, Reports: rp, PublishKeyFile: filepath.Join(base, "publish.key")}
	owner := httptest.NewServer(srv.Handler())
	defer owner.Close()
	pdb, _ := sql.Open("sqlite", "file:"+filepath.Join(base, "publish.db"))
	defer pdb.Close()
	ps, _ := publish.Open(pdb, filepath.Join(base, "pubdata"))
	key, _ := ps.AddPublisher("laptop")
	pub := httptest.NewServer((&publish.Handler{Store: ps}).Handler())
	defer pub.Close()
	api := owner.URL
	must := func(method, url string, in any, want int, what string) []byte {
		t.Helper()
		if in == nil && method == "POST" {
			in = map[string]any{}
		}
		st, b := callJSON(t, method, url, in)
		if st != want {
			t.Fatalf("%s: %d %s", what, st, b)
		}
		return b
	}
	idOf := func(b []byte) string {
		var x struct{ ID int64 }
		json.Unmarshal(b, &x)
		return itoa(int(x.ID))
	}

	// pages: a parent, a child under it; a page cannot move under its own child
	parent := idOf(must("POST", api+"/api/reports", map[string]any{"title": "Meetings", "body": ""}, 201, "parent"))
	body := "---\ndate: 2026-11-10\nsummary: Cold fractions are lower limits.\npresenter: Denis\nstatus: Draft\n---\n\nThe first paragraph.\n\nThe second paragraph."
	b := must("POST", api+"/api/reports", map[string]any{"title": "Report 10.11", "body": body, "parent": atoi64(parent)}, 201, "child")
	child := idOf(b)
	must("POST", api+"/api/reports/"+parent+"/move", map[string]any{"parent": atoi64(child)}, 422, "cycle")
	var list []reports.Summary
	json.Unmarshal(must("GET", api+"/api/reports", nil, 200, "list"), &list)
	for _, r := range list {
		if itoa(int(r.ID)) == child && (itoa(int(r.Parent)) != parent || r.Date != "2026-11-10" || r.Summary != "Cold fractions are lower limits.") {
			t.Fatalf("list row: %+v", r)
		}
	}

	must("POST", api+"/api/publish/settings", map[string]any{"url": pub.URL, "key": key, "ownerName": "Denis"}, 200, "settings")
	var rep reports.Report
	json.Unmarshal(b, &rep)
	must("POST", api+"/api/reports/"+child+"/publish", map[string]int{"revision": rep.Revision}, 200, "publish")
	var made struct{ Reader publish.Reader }
	json.Unmarshal(must("POST", api+"/api/publish/readers", map[string]any{"name": "Supervisor"}, 201, "reader"), &made)
	tok := made.Reader.Token

	raw := must("GET", pub.URL+"/read-api/"+tok+"/reports/"+rep.Key, nil, 200, "read")
	for _, leak := range []string{"presenter", "Draft", "status"} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("front matter key %q reached the reader", leak)
		}
	}
	var rv publish.ReportView
	json.Unmarshal(raw, &rv)
	if rv.Revision.Date != "2026-11-10" || rv.Revision.Summary != "Cold fractions are lower limits." || len(rv.Revision.Blocks) != 2 {
		t.Fatalf("revision: %+v", rv.Revision)
	}

	// a comment on a phrase, and one the reader deletes again; both reach the owner's copy as they end up
	comment := func(block int, text, quote string) int64 {
		var c publish.Comment
		json.Unmarshal(must("POST", pub.URL+"/read-api/"+tok+"/comments", map[string]any{"report": rep.Key, "revision": rv.Revision.ID, "block": rv.Revision.Blocks[block].ID, "body": text, "quote": quote}, 201, "comment"), &c)
		return c.ID
	}
	quoted := comment(0, "Which paragraph?", "first   paragraph")
	gone := comment(1, "Typo, ignore", "")
	if err := srv.SyncComments(); err != nil {
		t.Fatal(err)
	}
	must("DELETE", pub.URL+"/read-api/"+tok+"/comments/"+itoa(int(gone)), nil, 204, "reader deletes own")
	if err := srv.SyncComments(); err != nil {
		t.Fatal(err)
	}
	cs, _ := rp.Comments(rep.Key)
	if len(cs) != 1 || cs[0].ID != quoted || cs[0].Quote != "first paragraph" {
		t.Fatalf("owner copy after the reader's deletion: %+v", cs)
	}
	must("POST", api+"/api/report-comments/"+itoa(int(quoted))+"/delete", nil, 200, "owner deletes")
	if cs, _ = rp.Comments(rep.Key); len(cs) != 0 {
		t.Fatalf("owner copy after deleting: %+v", cs)
	}

	// withdraw: gone for the reader, deletable or publishable again from revision 1
	comment(0, "One more", "")
	srv.SyncComments()
	must("POST", api+"/api/reports/"+child+"/withdraw", nil, 200, "withdraw")
	must("GET", pub.URL+"/read-api/"+tok+"/reports/"+rep.Key, nil, 404, "withdrawn report")
	if cs, _ = rp.Comments(rep.Key); len(cs) != 0 {
		t.Fatalf("comments kept after withdrawing: %+v", cs)
	}
	must("POST", api+"/api/reports/"+child+"/withdraw", nil, 422, "withdraw twice")
	must("POST", api+"/api/reports/"+child+"/publish", map[string]int{"revision": rep.Revision}, 200, "publish again")
	json.Unmarshal(must("GET", pub.URL+"/read-api/"+tok+"/reports/"+rep.Key, nil, 200, "read again"), &rv)
	if rv.Revision.Number != 1 {
		t.Fatalf("republished as %d", rv.Revision.Number)
	}
	must("POST", api+"/api/reports/"+child+"/withdraw", nil, 200, "withdraw again")
	must("POST", api+"/api/reports/"+parent+"/delete", nil, 200, "delete parent")
	var after reports.Report
	json.Unmarshal(must("GET", api+"/api/reports/"+child, nil, 200, "get"), &after)
	if after.Parent != 0 {
		t.Fatalf("a deleted page's child moves up: %+v", after.Parent)
	}
}

func atoi64(s string) int64 {
	var n int64
	for _, c := range s {
		n = n*10 + int64(c-'0')
	}
	return n
}
