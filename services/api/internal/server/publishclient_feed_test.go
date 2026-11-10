package server

import (
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/reports"
)

// The owner's copy resumes the comment feed from the highest id it holds. A publish server that is compromised or broken
// must not be able to move that cursor past every real comment with one absurd id, nor flood the owner with a page longer
// than its own page size.

func comments(ids ...int64) string {
	var parts []string
	for _, id := range ids {
		parts = append(parts, fmt.Sprintf(`{"id":%d,"report":"r","revision":"v","block":"b","author":"a","body":"x","createdAt":"2026-01-01T00:00:00Z"}`, id))
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func TestCommentFeedWithImplausibleNumbersIsRefused(t *testing.T) {
	for name, body := range map[string]string{
		"max int64":            comments(math.MaxInt64),
		"a jump past the cap":  comments(1, 2, 3+maxFeedStep+1),
		"gaps that add up":     comments(60_000, 120_000),
		"out of order":         comments(5, 4),
		"repeated":             comments(5, 5),
		"not after the cursor": comments(10),
		"zero":                 comments(0),
		"negative":             comments(-3),
	} {
		t.Run(name, func(t *testing.T) {
			after := int64(0)
			if name == "not after the cursor" {
				after = 10
			}
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer ts.Close()
			c := &publishClient{base: ts.URL, key: "k", http: ts.Client()}
			got, err := c.commentsAfter(after)
			if err == nil || len(got) != 0 {
				t.Fatalf("want refusal, got %d comments and err %v", len(got), err)
			}
		})
	}
}

func seq(from, to int64) []int64 {
	var out []int64
	for i := from; i <= to; i++ {
		out = append(out, i)
	}
	return out
}

func TestCommentFeedGapsFromDeletedRowsAndFullPagesAreAccepted(t *testing.T) {
	// ids skip rows that were deleted: honest, as long as the gap is far below the cap
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, comments(7, 9, 5000, 5001))
	}))
	defer ts.Close()
	c := &publishClient{base: ts.URL, key: "k", http: ts.Client()}
	got, err := c.commentsAfter(3)
	if err != nil || len(got) != 4 {
		t.Fatalf("an honest feed was refused: %d, %v", len(got), err)
	}
	// a page longer than this version expects (a newer server) is not a reason to stop syncing
	full := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, comments(seq(1, 700)...)) }))
	defer full.Close()
	c = &publishClient{base: full.URL, key: "k", http: full.Client()}
	if got, err := c.commentsAfter(0); err != nil || len(got) != 700 {
		t.Fatalf("a long page was refused: %d, %v", len(got), err)
	}
}

func TestDeletionFeedWithImplausibleNumbersIsRefused(t *testing.T) {
	for name, body := range map[string]string{
		"huge seq":      fmt.Sprintf(`{"deletions":[{"seq":%d,"id":3}]}`, int64(math.MaxInt64)),
		"seq backward":  `{"deletions":[{"seq":4,"id":3},{"seq":2,"id":4}]}`,
		"no comment id": `{"deletions":[{"seq":1,"id":0}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, body) }))
			defer ts.Close()
			c := &publishClient{base: ts.URL, key: "k", http: ts.Client()}
			if got, err := c.deletionsAfter(0); err == nil || len(got) != 0 {
				t.Fatalf("want refusal, got %d and %v", len(got), err)
			}
		})
	}
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"deletions":[{"seq":2,"id":9},{"seq":4,"id":3}]}`)
	}))
	defer ok.Close()
	c := &publishClient{base: ok.URL, key: "k", http: ok.Client()}
	if got, err := c.deletionsAfter(0); err != nil || len(got) != 2 {
		t.Fatalf("an honest deletion feed was refused: %d, %v", len(got), err)
	}
}

// The whole sync: a hostile page changes nothing the owner holds, the failure is what the owner sees in the sync status,
// and once the server behaves, the comments that were skipped are still pulled (the cursor did not move).
func TestSyncKeepsTheCursorWhenTheFeedIsImplausible(t *testing.T) {
	base := t.TempDir()
	cat, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	bs, _ := boards.Open(cat.DB(), base)
	rp, _ := reports.Open(cat.DB(), base)
	keyFile := filepath.Join(base, "publish.key")
	if err := os.WriteFile(keyFile, []byte("k\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hostile := true
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/publish-api/comments/deleted"):
			fmt.Fprint(w, `{"deletions":[]}`)
		case r.URL.Path == "/publish-api/comments" && hostile:
			fmt.Fprint(w, comments(math.MaxInt64))
		case r.URL.Path == "/publish-api/comments" && r.URL.Query().Get("after") == "0":
			fmt.Fprint(w, comments(1, 2))
		default:
			fmt.Fprint(w, "[]")
		}
	}))
	defer ts.Close()
	if err := rp.SetSetting("publish_url", ts.URL); err != nil {
		t.Fatal(err)
	}
	srv := &Server{Store: cat, Boards: bs, Reports: rp, PublishKeyFile: keyFile}
	if err := srv.SyncComments(); err == nil || !strings.Contains(err.Error(), "does not look like") {
		t.Fatalf("want the refusal to surface, got %v", err)
	}
	if cur, _ := rp.CommentCursor(); cur != 0 {
		t.Fatalf("the cursor moved to %d on a hostile page", cur)
	}
	hostile = false
	if err := srv.SyncComments(); err != nil {
		t.Fatal(err)
	}
	if cur, _ := rp.CommentCursor(); cur != 2 {
		t.Fatalf("after the server behaves the comments must be pulled; cursor %d", cur)
	}
}
