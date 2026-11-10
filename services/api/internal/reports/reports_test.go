package reports

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/publish"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	cat, err := catalog.Open("file:" + filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cat.Close() })
	s, err := Open(cat.DB(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func ptr(s string) *string { return &s }

func TestASaveFromAStaleRevisionIsRefusedAndNothingIsOverwritten(t *testing.T) {
	s := newStore(t)
	r, err := s.Create("Block size 32 vs 64", "first", AuthorAgent)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(r.ID, Update{Body: ptr("owner edit"), Base: 1, Author: AuthorOwner}); err != nil {
		t.Fatal(err)
	}
	cur, err := s.Update(r.ID, Update{Body: ptr("agent edit from rev 1"), Base: 1, Author: AuthorAgent})
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("stale save: %v", err)
	}
	if cur.Body != "owner edit" || cur.Revision != 2 || cur.LastAuthor != AuthorOwner {
		t.Fatalf("conflict should return the current report: %+v", cur)
	}
	h, _ := s.History(r.ID, true)
	if len(h) != 2 || h[0].Author != AuthorOwner || h[1].Author != AuthorAgent || h[1].Body != "first" {
		t.Fatalf("history: %+v", h)
	}
}

func TestAgentSavesAreNewUntilTheOwnerOpensTheReport(t *testing.T) {
	s := newStore(t)
	r, _ := s.Create("Weekly notes 2026-W45", "x", AuthorOwner)
	list, _ := s.List()
	if list[0].AgentUnseen {
		t.Fatal("an owner's own report is not new to them")
	}
	r, _ = s.Update(r.ID, Update{Body: ptr("y"), Base: r.Revision, Author: AuthorAgent})
	list, _ = s.List()
	if !list[0].AgentUnseen || list[0].LastAuthor != AuthorAgent {
		t.Fatalf("agent save should be new: %+v", list[0])
	}
	if err := s.MarkSeen(r.ID, r.Revision); err != nil {
		t.Fatal(err)
	}
	list, _ = s.List()
	if list[0].AgentUnseen {
		t.Fatal("seen")
	}
	if err := s.MarkSeen(r.ID, 99); err != nil { // cannot mark beyond what exists
		t.Fatal(err)
	}
	got, _ := s.Get(r.ID)
	if got.SeenRevision != got.Revision {
		t.Fatalf("seen revision clamped: %d vs %d", got.SeenRevision, got.Revision)
	}
}

func TestTitlesAreUniqueIgnoringCaseAndCannotHoldLinkSyntax(t *testing.T) {
	s := newStore(t)
	if _, err := s.Create("Noise masking", "", AuthorOwner); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create("noise  MASKING", "", AuthorOwner); !errors.Is(err, ErrConflict) {
		t.Fatalf("duplicate title: %v", err)
	}
	for _, bad := range []string{"", "  ", "a|b", "see [[x]]", "a#b", strings.Repeat("x", MaxTitle+1)} {
		if _, err := s.Create(bad, "", AuthorOwner); !errors.Is(err, ErrInvalid) {
			t.Errorf("title %q: %v", bad, err)
		}
	}
	if _, err := s.Create("ok", "", "someone"); !errors.Is(err, ErrInvalid) {
		t.Errorf("unknown author accepted")
	}
	if _, err := s.Create("big", strings.Repeat("x", MaxBody+1), AuthorOwner); !errors.Is(err, ErrInvalid) {
		t.Errorf("oversized body accepted")
	}
}

func TestBacklinksFollowLinksAndARenameRewritesThem(t *testing.T) {
	s := newStore(t)
	target, _ := s.Create("Noise masking threshold sweep", "body", AuthorOwner)
	a, _ := s.Create("Block size 32 vs 64", "See [[noise masking threshold sweep]] and [[Noise masking threshold sweep|the sweep]].\n\n```\n[[Not a link]]\n```", AuthorAgent)
	_, _ = s.Create("Unrelated", "nothing", AuthorOwner)
	bl, _ := s.Backlinks(target.ID)
	if len(bl) != 1 || bl[0].ID != a.ID {
		t.Fatalf("backlinks: %+v", bl)
	}
	if _, err := s.Update(target.ID, Update{Title: ptr("Masking threshold sweep"), Base: target.Revision, Author: AuthorOwner}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(a.ID)
	want := "See [[Masking threshold sweep]] and [[Masking threshold sweep|the sweep]].\n\n```\n[[Not a link]]\n```"
	if got.Body != want {
		t.Fatalf("relinked body:\n%s", got.Body)
	}
	if got.Revision != 2 || got.LastAuthor != AuthorOwner {
		t.Fatalf("relink is a recorded save by the renamer: %+v", got)
	}
	bl, _ = s.Backlinks(target.ID)
	if len(bl) != 1 {
		t.Fatalf("backlinks after rename: %+v", bl)
	}
}

func TestBlocksSplitParagraphsHeadingsCodeMathAndEmbeds(t *testing.T) {
	fig := strings.Repeat("ab", 32)
	body := "# Question\nWhy is AM lower?\nSecond line.\n\n- a\n- b\n\n```julia\nx = 1\n\ny = 2\n```\n$$\nAM = \\langle \\cos 2\\theta \\rangle\n$$\n![[figure:" + fig + "]]\n[[run:42]]\nInline [[run:7]] and ![[figure:" + fig + "]] stay text."
	got := Blocks(body)
	kinds := []string{}
	for _, b := range got {
		kinds = append(kinds, b.Kind)
	}
	want := []string{"text", "text", "text", "code", "text", "figure", "run", "text"}
	if !reflect.DeepEqual(kinds, want) {
		t.Fatalf("kinds %v, want %v\n%#v", kinds, want, got)
	}
	if got[0].Text != "# Question" || got[1].Text != "Why is AM lower?\nSecond line." {
		t.Fatalf("heading split: %#v", got[:2])
	}
	if !strings.Contains(got[3].Text, "y = 2") {
		t.Fatalf("a blank line inside code must not split it: %q", got[3].Text)
	}
	if got[5].Figure != fig || got[6].Run != 42 {
		t.Fatalf("embeds: %#v", got[5:7])
	}
	if got := ReportTitles("See [[Report 10.11.2026 — What it means: GASS and CO]] and [[run:3]]"); !reflect.DeepEqual(got, []string{"Report 10.11.2026 — What it means: GASS and CO"}) {
		t.Fatalf("a title with a colon is a link, typed references are not: %v", got)
	}
	links := Links(body)
	wantLinks := []Link{{"figure", fig}, {"run", "42"}, {"run", "7"}}
	if !reflect.DeepEqual(links, wantLinks) {
		t.Fatalf("links %v", links)
	}
}

func TestAttachmentsMustBeCompleteImages(t *testing.T) {
	s := newStore(t)
	var buf bytes.Buffer
	_ = png.Encode(&buf, image.NewGray(image.Rect(0, 0, 4, 3)))
	sha, mime, err := s.AddAttachment(buf.Bytes())
	if err != nil || mime != "image/png" {
		t.Fatal(err, mime)
	}
	sha2, _, _ := s.AddAttachment(buf.Bytes())
	if sha2 != sha {
		t.Fatal("identical bytes are one attachment")
	}
	b, m, err := s.Attachment(sha)
	if err != nil || m != "image/png" || !bytes.Equal(b, buf.Bytes()) {
		t.Fatal("read back", err)
	}
	for name, bad := range map[string][]byte{
		"empty": nil, "text": []byte("<svg onload=alert(1)>"), "truncated": buf.Bytes()[:len(buf.Bytes())-12],
	} {
		if _, _, err := s.AddAttachment(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s accepted: %v", name, err)
		}
	}
	if _, _, err := s.Attachment("../../etc/passwd"); !errors.Is(err, ErrNotFound) {
		t.Errorf("path in sha: %v", err)
	}
}

func TestAPublishedReportCannotBeDeleted(t *testing.T) {
	s := newStore(t)
	r, _ := s.Create("Draft", "", AuthorOwner)
	p, _ := s.Create("Shared", "", AuthorOwner)
	if err := s.RecordPublication(p.ID, Publication{Number: 1, RevisionID: "x", WorkingRevision: 1, PublishedAt: time.Now(), Space: "supervisor"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(p.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("published delete: %v", err)
	}
	if err := s.Delete(r.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted report still there")
	}
}

func TestReplyDraftsGoToTheThreadAndAreNeverSentByTheStore(t *testing.T) {
	s := newStore(t)
	now := time.Now().UTC()
	if err := s.MirrorComments([]publish.Comment{
		{ID: 10, Space: "sup", Report: "aaaaaaaaaaaaaaaa", Revision: "r", Block: "b", Author: "Supervisor", Body: "Why lower?", CreatedAt: now},
		{ID: 11, Space: "sup", Report: "aaaaaaaaaaaaaaaa", Revision: "r", Block: "b", ParentID: 10, Author: "Denis", FromOwner: true, Body: "Looking", CreatedAt: now},
	}); err != nil {
		t.Fatal(err)
	}
	d, err := s.DraftReply(11, "  Block 64 smooths more.  ", AuthorAgent)
	if err != nil {
		t.Fatal(err)
	}
	if d.CommentID != 10 || d.Body != "Block 64 smooths more." {
		t.Fatalf("draft: %+v", d)
	}
	if _, err := s.DraftReply(999, "x", AuthorAgent); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown comment: %v", err)
	}
	ds, _ := s.Drafts()
	if len(ds) != 1 {
		t.Fatal(ds)
	}
	if err := s.DiscardDraft(d.ID); err != nil {
		t.Fatal(err)
	}
	if ds, _ := s.Drafts(); len(ds) != 0 {
		t.Fatal("discarded draft listed")
	}
	cur, _ := s.CommentCursor()
	if cur != 11 {
		t.Fatalf("cursor %d", cur)
	}
	res := now.Add(time.Minute)
	_ = s.MirrorComments([]publish.Comment{{ID: 10, Space: "sup", Report: "aaaaaaaaaaaaaaaa", Revision: "r", Block: "b", Author: "Supervisor", Body: "Why lower?", CreatedAt: now, ResolvedAt: &res}})
	c, _ := s.Comment(10)
	if c.ResolvedAt == nil {
		t.Fatal("resolved state not mirrored")
	}
}

func TestChangesPairRemovedAndAddedBlocks(t *testing.T) {
	before := "# Setup\n\nRange -8..+8 km/s.\n\nKept paragraph.\n\nOld closing."
	after := "# Setup\n\nRange -12..+12 km/s.\n\nKept paragraph.\n\nNew note.\n\nOld closing."
	got := Changes(before, after)
	want := []Change{
		{"same", "# Setup", "# Setup"},
		{"changed", "Range -8..+8 km/s.", "Range -12..+12 km/s."},
		{"same", "Kept paragraph.", "Kept paragraph."},
		{"added", "", "New note."},
		{"same", "Old closing.", "Old closing."},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v", got)
	}
	if c := Changes("a\n\nb", ""); len(c) != 2 || c[0].Status != "removed" {
		t.Fatalf("all removed: %#v", c)
	}
}
