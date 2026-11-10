package publish

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func newStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "p.db")+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	s, err := Open(db, dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func pngBytes(t *testing.T, w, h int, seed uint8) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = uint8(i) ^ seed
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func shaOf(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

func putPNG(t *testing.T, s *Store, seed uint8) string {
	t.Helper()
	b := pngBytes(t, 8, 8, seed)
	if err := s.PutAsset(shaOf(b), b); err != nil {
		t.Fatal(err)
	}
	return shaOf(b)
}

const keyA, keyB = "aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb"

func figBlock(id, asset string) Block {
	return Block{ID: id, Kind: BlockFigure, Figure: &Figure{Asset: asset, Width: 8, Height: 8, Title: "t", Provenance: json.RawMessage(`{"a":1}`)}}
}

func rev(report string, n int, blocks ...Block) Revision {
	return Revision{Report: report, Number: n, Title: "Report", PublishedAt: time.Unix(1700000000, 0).UTC(), Authors: "x", Blocks: blocks}
}

// world: space "sup" holds report A (with a figure and a link to B), report B is outside the space.
type world struct {
	s            *Store
	asset, other string
	revA, revB   Revision
	token        string
}

func newWorld(t *testing.T) world {
	s := newStore(t)
	w := world{s: s, asset: putPNG(t, s, 1), other: putPNG(t, s, 2)}
	var err error
	w.revA, err = s.PushRevision(rev(keyA, 1,
		Block{ID: "t1", Kind: BlockText, Text: "see [[report:" + keyB + "|B]] and [[report:cccccccccccccccc|C]] ![x](asset:" + w.asset + ")"},
		figBlock("f1", w.asset),
		Block{ID: "r1", Kind: BlockRun, Run: json.RawMessage(`{"state":"done"}`)}))
	if err != nil {
		t.Fatal(err)
	}
	w.revB, err = s.PushRevision(rev(keyB, 1, figBlock("f2", w.other)))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutSpace(Space{Slug: "sup", Title: "Supervisor", Reports: []string{keyA}}); err != nil {
		t.Fatal(err)
	}
	rd, err := s.CreateReader("sup", "Prof", nil)
	if err != nil {
		t.Fatal(err)
	}
	w.token = rd.Token
	return w
}

func TestAssetValidation(t *testing.T) {
	s := newStore(t)
	good := pngBytes(t, 4, 4, 0)
	var wide bytes.Buffer
	_ = png.Encode(&wide, image.NewGray(image.Rect(0, 0, 4097, 1)))
	var rgba bytes.Buffer
	_ = png.Encode(&rgba, func() image.Image { i := image.NewRGBA(image.Rect(0, 0, 2, 2)); i.Set(0, 0, color.White); return i }())
	big := make([]byte, MaxAssetBytes+1)
	trunc := good[:len(good)-12]
	for name, c := range map[string]struct {
		sha  string
		body []byte
	}{
		"sha mismatch": {strings.Repeat("0", 64), good},
		"not an image": {shaOf([]byte("hello")), []byte("hello")},
		"truncated":    {shaOf(trunc), trunc},
		"oversized":    {shaOf(big), big},
		"too wide":     {shaOf(wide.Bytes()), wide.Bytes()},
		"bad name":     {"abc", good},
		"empty":        {shaOf(nil), nil},
	} {
		if err := s.PutAsset(c.sha, c.body); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	for i := 0; i < 2; i++ { // idempotent
		if err := s.PutAsset(shaOf(good), good); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutAsset(shaOf(rgba.Bytes()), rgba.Bytes()); err != nil {
		t.Fatal(err)
	}
}

func TestRevisionValidation(t *testing.T) {
	s := newStore(t)
	a := putPNG(t, s, 1)
	long := strings.Repeat("x", 65)
	many := make([]Block, 2001)
	for i := range many {
		many[i] = Block{ID: ContentID("text", string(rune(i))) + string(rune('a'+i%26)) + strings.Repeat("0", i%7) + itoa(i), Kind: BlockText, Text: "x"}
	}
	for name, r := range map[string]Revision{
		"bad key":            rev("ZZ", 1),
		"number 0":           rev(keyA, 0),
		"empty title":        func() Revision { r := rev(keyA, 1); r.Title = ""; return r }(),
		"long title":         func() Revision { r := rev(keyA, 1); r.Title = strings.Repeat("t", 201); return r }(),
		"too many blocks":    rev(keyA, 1, many...),
		"empty block id":     rev(keyA, 1, Block{Kind: BlockText, Text: "x"}),
		"long block id":      rev(keyA, 1, Block{ID: long, Kind: BlockText}),
		"duplicate block id": rev(keyA, 1, Block{ID: "a", Kind: BlockText}, Block{ID: "a", Kind: BlockText}),
		"unknown kind":       rev(keyA, 1, Block{ID: "a", Kind: "html"}),
		"text too long":      rev(keyA, 1, Block{ID: "a", Kind: BlockText, Text: strings.Repeat("x", 64<<10+1)}),
		"missing asset":      rev(keyA, 1, figBlock("a", strings.Repeat("1", 64))),
		"figure no figure":   rev(keyA, 1, Block{ID: "a", Kind: BlockFigure}),
		"provenance array":   rev(keyA, 1, Block{ID: "a", Kind: BlockFigure, Figure: &Figure{Asset: a, Provenance: json.RawMessage(`[1]`)}}),
		"provenance missing": rev(keyA, 1, Block{ID: "a", Kind: BlockFigure, Figure: &Figure{Asset: a}}),
		"run string":         rev(keyA, 1, Block{ID: "a", Kind: BlockRun, Run: json.RawMessage(`"x"`)}),
		"run too big":        rev(keyA, 1, Block{ID: "a", Kind: BlockRun, Run: json.RawMessage(`{"x":"` + strings.Repeat("y", 64<<10) + `"}`)}),
		"text asset missing": rev(keyA, 1, Block{ID: "a", Kind: BlockText, Text: "![x](asset:" + strings.Repeat("2", 64) + ")"}),
	} {
		if _, err := s.PushRevision(r); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	// total document size
	var blocks []Block
	for i := 0; i < 70; i++ {
		blocks = append(blocks, Block{ID: itoa(i), Kind: BlockText, Text: strings.Repeat("x", 60<<10)})
	}
	if _, err := s.PushRevision(rev(keyA, 1, blocks...)); !errors.Is(err, ErrInvalid) {
		t.Errorf("4 MiB: err = %v", err)
	}
}

func itoa(i int) string { b, _ := json.Marshal(i); return string(b) }

func TestPushIdempotentConflictAndServerID(t *testing.T) {
	s := newStore(t)
	r := rev(keyA, 1, Block{ID: "a", Kind: BlockText, Text: "hi"})
	r.ID = "client-chosen"
	first, err := s.PushRevision(r)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := ComputeID(r)
	if first.ID != want || first.ID == "client-chosen" {
		t.Fatalf("ID = %q, want server-computed %q", first.ID, want)
	}
	again, err := s.PushRevision(r)
	if err != nil || again.ID != first.ID {
		t.Fatalf("idempotent push: %v %v", again.ID, err)
	}
	r2 := rev(keyA, 1, Block{ID: "a", Kind: BlockText, Text: "changed"})
	if _, err := s.PushRevision(r2); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict err = %v", err)
	}
	r2.Number = 2
	if _, err := s.PushRevision(r2); err != nil {
		t.Fatal(err)
	}
}

func TestSpaceMembershipControlsReaderAccess(t *testing.T) {
	w := newWorld(t)
	v, err := w.s.Space(w.token)
	if err != nil || v.Reader != "Prof" || v.Title != "Supervisor" || len(v.Reports) != 1 || v.Reports[0].Key != keyA || v.Reports[0].Latest != 1 {
		t.Fatalf("space view = %+v, %v", v, err)
	}
	if v.LastSync != nil {
		t.Error("lastSync before any publisher contact")
	}
	rep, err := w.s.Report(w.token, keyA, 0)
	if err != nil {
		t.Fatal(err)
	}
	// B is not in the space yet, so the link to it is not shared
	if len(rep.Revision.Blocks[0].Shared) != 0 {
		t.Fatalf("report outside space listed as shared: %v", rep.Revision.Blocks[0].Shared)
	}
	if _, err := w.s.Report(w.token, keyB, 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other space's report: %v", err)
	}
	if _, _, err := w.s.Asset(w.token, w.other); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other space's asset: %v", err)
	}
	mime, b, err := w.s.Asset(w.token, w.asset)
	if err != nil || mime != "image/png" || shaOf(b) != w.asset {
		t.Fatalf("asset: %v %v", mime, err)
	}
	// Adding B to the space opens both and makes the link shared.
	if err := w.s.PutSpace(Space{Slug: "sup", Title: "Supervisor", Reports: []string{keyA, keyB}}); err != nil {
		t.Fatal(err)
	}
	rep, _ = w.s.Report(w.token, keyA, 1)
	if got := rep.Revision.Blocks[0].Shared; len(got) != 1 || got[0] != keyB {
		t.Fatalf("shared after adding = %v", got)
	}
	if _, _, err := w.s.Asset(w.token, w.other); err != nil {
		t.Fatal(err)
	}
	// A report without revisions is not listed.
	_ = w.s.PutSpace(Space{Slug: "sup", Title: "Supervisor", Reports: []string{keyA, "dddddddddddddddd"}})
	if v, _ := w.s.Space(w.token); len(v.Reports) != 1 {
		t.Fatalf("reports = %+v", v.Reports)
	}
	if _, err := w.s.Report(w.token, keyA, 9); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing revision: %v", err)
	}
}

func TestReaderTokens(t *testing.T) {
	w := newWorld(t)
	past := time.Now().Add(-time.Hour)
	if _, err := w.s.CreateReader("sup", "x", &past); !errors.Is(err, ErrInvalid) {
		t.Fatalf("past expiry accepted: %v", err)
	}
	soon := time.Now().Add(1100 * time.Millisecond)
	short, err := w.s.CreateReader("sup", "Short", &soon)
	if err != nil {
		t.Fatal(err)
	}
	revoked, _ := w.s.CreateReader("sup", "Gone", nil)
	if err := w.s.RevokeReader(revoked.ID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1200 * time.Millisecond)
	for name, tok := range map[string]string{"unknown": strings.Repeat("a", 43), "revoked": revoked.Token, "expired": short.Token, "short": "x", "empty": ""} {
		if _, err := w.s.Space(tok); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	list, err := w.s.Readers()
	if err != nil || len(list) != 3 {
		t.Fatal(list, err)
	}
	for _, r := range list {
		if r.Token != "" {
			t.Error("token listed")
		}
	}
	if _, err := w.s.CreateReader("nospace", "x", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown space: %v", err)
	}
	if err := w.s.RevokeReader(999); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestComments(t *testing.T) {
	w := newWorld(t)
	s := w.s
	in := func(block, body string) CommentInput {
		return CommentInput{Report: keyA, Revision: w.revA.ID, Block: block, Body: body, Author: "Mallory"}
	}
	c, err := s.AddComment(w.token, in("t1", "  nice  "))
	if err != nil || c.Author != "Prof" || c.FromOwner || c.Body != "nice" || c.Space != "" {
		t.Fatalf("comment = %+v, %v", c, err)
	}
	pt := &Anchor{Kind: "point", X: .5, Y: .5}
	bad := []struct {
		name string
		in   CommentInput
	}{
		{"empty body", in("t1", "  ")},
		{"long body", in("t1", strings.Repeat("x", 4001))},
		{"other report", CommentInput{Report: keyB, Revision: w.revB.ID, Block: "f2", Body: "x"}},
		{"revision of another report", CommentInput{Report: keyA, Revision: w.revB.ID, Block: "f1", Body: "x"}},
		{"unknown block", in("nope", "x")},
		{"anchor on text", CommentInput{Report: keyA, Revision: w.revA.ID, Block: "t1", Body: "x", Anchor: pt}},
		{"anchor outside", CommentInput{Report: keyA, Revision: w.revA.ID, Block: "f1", Body: "x", Anchor: &Anchor{Kind: "point", X: 1.5}}},
		{"degenerate box", CommentInput{Report: keyA, Revision: w.revA.ID, Block: "f1", Body: "x", Anchor: &Anchor{Kind: "box", X: .5, Y: .1, X1: .5, Y1: .2}}},
		{"reply to nothing", CommentInput{ParentID: 999, Body: "x"}},
	}
	for _, b := range bad {
		if _, err := s.AddComment(w.token, b.in); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", b.name, err)
		}
	}
	fc, err := s.AddComment(w.token, CommentInput{Report: keyA, Revision: w.revA.ID, Block: "f1", Body: "look", Anchor: &Anchor{Kind: "box", X: .1, Y: .1, X1: .4, Y1: .4}})
	if err != nil || fc.Anchor == nil {
		t.Fatalf("figure anchor: %+v %v", fc, err)
	}
	// replies inherit and carry no anchor
	rp, err := s.AddComment(w.token, CommentInput{ParentID: fc.ID, Body: "reply", Report: keyB, Block: "zzz"})
	if err != nil || rp.Report != keyA || rp.Block != "f1" || rp.Revision != w.revA.ID || rp.Anchor != nil {
		t.Fatalf("reply = %+v %v", rp, err)
	}
	if _, err := s.AddComment(w.token, CommentInput{ParentID: fc.ID, Body: "x", Anchor: pt}); !errors.Is(err, ErrInvalid) {
		t.Error("anchored reply accepted")
	}
	if _, err := s.AddComment(w.token, CommentInput{ParentID: rp.ID, Body: "x"}); !errors.Is(err, ErrInvalid) {
		t.Error("reply to a reply accepted")
	}
	// a thread of another space cannot be replied to
	_ = s.PutSpace(Space{Slug: "other", Title: "O", Reports: []string{keyA}})
	o, _ := s.CreateReader("other", "Else", nil)
	if _, err := s.AddComment(o.Token, CommentInput{ParentID: fc.ID, Body: "x"}); !errors.Is(err, ErrInvalid) {
		t.Error("cross-space reply accepted")
	}
	// owner comment
	oc, err := s.AddOwnerComment("sup", CommentInput{ParentID: fc.ID, Body: "thanks", Author: "Denis"})
	if err != nil || !oc.FromOwner || oc.Author != "Denis" || oc.Space != "sup" {
		t.Fatalf("owner = %+v %v", oc, err)
	}
	if _, err := s.AddOwnerComment("sup", CommentInput{ParentID: fc.ID, Body: "x"}); !errors.Is(err, ErrInvalid) {
		t.Error("owner comment without author accepted")
	}
	if _, err := s.AddOwnerComment("nospace", CommentInput{ParentID: fc.ID, Body: "x", Author: "D"}); !errors.Is(err, ErrInvalid) {
		t.Error("owner comment in unknown space accepted")
	}
	// resolve: first comments only
	if _, err := s.ResolveComment(rp.ID, true); !errors.Is(err, ErrInvalid) {
		t.Errorf("resolve reply: %v", err)
	}
	r, err := s.ResolveComment(fc.ID, true)
	if err != nil || r.ResolvedAt == nil {
		t.Fatalf("resolve: %+v %v", r, err)
	}
	if r, _ = s.ResolveComment(fc.ID, false); r.ResolvedAt != nil {
		t.Error("reopen kept resolvedAt")
	}
	if _, err := s.ResolveComment(12345, true); !errors.Is(err, ErrNotFound) {
		t.Error(err)
	}
	// feed ascending with space; cursor
	feed, err := s.CommentsAfter(0)
	if err != nil || len(feed) != 4 || feed[0].Space != "sup" {
		t.Fatalf("feed: %d %v", len(feed), err)
	}
	for i := 1; i < len(feed); i++ {
		if feed[i].ID <= feed[i-1].ID {
			t.Fatal("feed not ascending")
		}
	}
	if after, _ := s.CommentsAfter(feed[1].ID); len(after) != 2 || after[0].ID != feed[2].ID {
		t.Fatalf("cursor: %+v", after)
	}
	// reader view lists this space's comments only, with no space field
	v, _ := s.Report(w.token, keyA, 0)
	if len(v.Comments) != 4 {
		t.Fatalf("reader comments = %d", len(v.Comments))
	}
	if sv, _ := s.Space(w.token); sv.Comments != 4 {
		t.Fatalf("count = %d", sv.Comments)
	}
}

func TestCommentLimit(t *testing.T) {
	w := newWorld(t)
	tx, _ := w.s.db.Begin()
	for i := 0; i < maxCommentsSpace; i++ {
		_, _ = tx.Exec(`INSERT INTO comments(space,report,revision,block,author,body,created_at) VALUES('sup',?,?,'t1','a','b',1)`, keyA, w.revA.ID)
	}
	_ = tx.Commit()
	if _, err := w.s.AddComment(w.token, CommentInput{Report: keyA, Revision: w.revA.ID, Block: "t1", Body: "x"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("limit: %v", err)
	} // the owner can still answer when readers have filled the space
	if _, err := w.s.AddOwnerComment("sup", CommentInput{Report: keyA, Revision: w.revA.ID, Block: "t1", Body: "answer", Author: "Denis"}); err != nil {
		t.Fatalf("owner blocked by readers' comments: %v", err)
	}
}

func TestOneLinkCannotFloodASpace(t *testing.T) {
	w := newWorld(t)
	other, _ := w.s.CreateReader("sup", "Committee", nil)
	for i := 0; i < maxCommentsPerLinkDay; i++ {
		if _, err := w.s.AddComment(w.token, CommentInput{Report: keyA, Revision: w.revA.ID, Block: "t1", Body: "x"}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.s.AddComment(w.token, CommentInput{Report: keyA, Revision: w.revA.ID, Block: "t1", Body: "one more"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("daily link limit: %v", err)
	}
	if _, err := w.s.AddComment(other.Token, CommentInput{Report: keyA, Revision: w.revA.ID, Block: "t1", Body: "another reader"}); err != nil {
		t.Fatalf("another link is not affected: %v", err)
	}
}

func TestPublisherKeys(t *testing.T) {
	s := newStore(t)
	key, err := s.AddPublisher("laptop")
	if err != nil {
		t.Fatal(err)
	}
	if !s.Publisher(key) || s.Publisher(key+"x") || s.Publisher("") {
		t.Fatal("publisher check")
	}
	if s.lastContact() == nil {
		t.Fatal("contact not recorded")
	}
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM publishers WHERE key_hash=?`, key).Scan(&n)
	if n != 0 {
		t.Fatal("key stored in clear")
	}
	if err := s.RevokePublisher("laptop"); err != nil || s.Publisher(key) {
		t.Fatal("revoke")
	}
	if err := s.RevokePublisher("nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestSummaryAndDate(t *testing.T) {
	w := newWorld(t)
	r := rev(keyB, 2, Block{ID: "t", Kind: BlockText, Text: "x"})
	r.Summary, r.Date = "Cube 4 reduced; two ridges stand out.", "2026-11-03"
	out, err := w.s.PushRevision(r)
	if err != nil || out.Summary != r.Summary || out.Date != r.Date {
		t.Fatalf("push: %+v %v", out, err)
	}
	if err := w.s.PutSpace(Space{Slug: "sup", Title: "S", Reports: []string{keyA, keyB}}); err != nil {
		t.Fatal(err)
	}
	rv, err := w.s.Report(w.token, keyB, 0)
	if err != nil || rv.Revision.Summary != r.Summary || rv.Revision.Date != r.Date {
		t.Fatalf("report: %+v %v", rv.Revision, err)
	}
	sv, _ := w.s.Space(w.token)
	for _, rs := range sv.Reports {
		switch rs.Key {
		case keyB:
			if rs.Summary != r.Summary || rs.Date != r.Date {
				t.Errorf("space list: %+v", rs)
			}
		case keyA:
			if rs.Summary != "" || rs.Date != "" {
				t.Errorf("report without them: %+v", rs)
			}
		}
	}
	// old revisions hash as before: the fields vanish when empty
	if raw, _ := json.Marshal(rev(keyA, 1)); strings.Contains(string(raw), "summary") || strings.Contains(string(raw), "date") {
		t.Errorf("empty fields serialised: %s", raw)
	}
	for name, mod := range map[string]func(*Revision){
		"newline":      func(r *Revision) { r.Summary = "a\nb" },
		"long":         func(r *Revision) { r.Summary = strings.Repeat("é", 601) },
		"bad utf8":     func(r *Revision) { r.Summary = "a\xffb" },
		"date format":  func(r *Revision) { r.Date = "2026-1-3" },
		"date invalid": func(r *Revision) { r.Date = "2026-02-30" },
		"date time":    func(r *Revision) { r.Date = "2026-02-03T10:00:00Z" },
	} {
		bad := rev(keyB, 3)
		mod(&bad)
		if _, err := w.s.PushRevision(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	ok := rev(keyB, 4)
	ok.Summary = strings.Repeat("é", 600)
	if _, err := w.s.PushRevision(ok); err != nil {
		t.Errorf("600 runes: %v", err)
	}
}

func TestCommentQuote(t *testing.T) {
	w := newWorld(t)
	s := w.s
	in := func(block, quote string) CommentInput {
		return CommentInput{Report: keyA, Revision: w.revA.ID, Block: block, Body: "x", Quote: quote}
	}
	c, err := s.AddComment(w.token, in("t1", "  two\n\t words   here "))
	if err != nil || c.Quote != "two words here" {
		t.Fatalf("quote = %+v, %v", c, err)
	}
	if _, err := s.AddOwnerComment("sup", CommentInput{Report: keyA, Revision: w.revA.ID, Block: "t1", Body: "x", Quote: "owner", Author: "D"}); err != nil {
		t.Errorf("owner quote: %v", err)
	}
	if _, err := s.AddComment(w.token, in("t1", strings.Repeat("é", 500))); err != nil {
		t.Errorf("500 runes: %v", err)
	}
	for name, ci := range map[string]CommentInput{
		"figure block": in("f1", "phrase"),
		"run block":    in("r1", "phrase"),
		"too long":     in("t1", strings.Repeat("é", 501)),
		"blank":        in("t1", " \n "),
		"reply":        {ParentID: c.ID, Body: "x", Quote: "phrase"},
	} {
		if _, err := s.AddComment(w.token, ci); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	feed, _ := s.CommentsAfter(0)
	if len(feed) == 0 || feed[0].Quote != "two words here" {
		t.Errorf("feed: %+v", feed)
	}
}

func TestOwnerDeleteThreadLeavesTombstones(t *testing.T) {
	w := newWorld(t)
	s := w.s
	root, _ := s.AddComment(w.token, CommentInput{Report: keyA, Revision: w.revA.ID, Block: "t1", Body: "q"})
	r1, _ := s.AddComment(w.token, CommentInput{ParentID: root.ID, Body: "a"})
	r2, _ := s.AddOwnerComment("sup", CommentInput{ParentID: root.ID, Body: "b", Author: "D"})
	other, _ := s.AddComment(w.token, CommentInput{Report: keyA, Revision: w.revA.ID, Block: "t1", Body: "keep"})
	ids, err := s.DeleteComment(root.ID)
	if err != nil || len(ids) != 3 || ids[0] != root.ID || ids[1] != r1.ID || ids[2] != r2.ID {
		t.Fatalf("deleted %v, %v", ids, err)
	}
	ds, err := s.DeletedAfter(0)
	if err != nil || len(ds) != 3 || ds[0].Seq >= ds[1].Seq || ds[0].ID != root.ID {
		t.Fatalf("tombstones %+v, %v", ds, err)
	}
	if after, _ := s.DeletedAfter(ds[1].Seq); len(after) != 1 || after[0].ID != r2.ID {
		t.Errorf("cursor: %+v", after)
	}
	if feed, _ := s.CommentsAfter(0); len(feed) != 1 || feed[0].ID != other.ID {
		t.Errorf("remaining: %+v", feed)
	}
	if _, err := s.DeleteComment(root.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("twice: %v", err)
	}
	// deleting a reply removes only it
	rp, _ := s.AddComment(w.token, CommentInput{ParentID: other.ID, Body: "r"})
	if ids, err := s.DeleteComment(rp.ID); err != nil || len(ids) != 1 {
		t.Errorf("reply: %v %v", ids, err)
	}
}

func TestReaderDeletesOwnCommentsOnly(t *testing.T) {
	w := newWorld(t)
	s := w.s
	rd2, _ := s.CreateReader("sup", "Postdoc", nil)
	mine, _ := s.AddComment(w.token, CommentInput{Report: keyA, Revision: w.revA.ID, Block: "t1", Body: "mine"})
	theirs, _ := s.AddComment(rd2.Token, CommentInput{Report: keyA, Revision: w.revA.ID, Block: "t1", Body: "theirs"})
	owner, _ := s.AddOwnerComment("sup", CommentInput{Report: keyA, Revision: w.revA.ID, Block: "t1", Body: "o", Author: "D"})
	threaded, _ := s.AddComment(w.token, CommentInput{Report: keyA, Revision: w.revA.ID, Block: "t1", Body: "thread"})
	if _, err := s.AddComment(rd2.Token, CommentInput{ParentID: threaded.ID, Body: "reply"}); err != nil {
		t.Fatal(err)
	}
	rv, _ := s.Report(w.token, keyA, 0)
	got := map[int64]bool{}
	for _, c := range rv.Comments {
		got[c.ID] = c.Mine
	}
	if !got[mine.ID] || got[theirs.ID] || got[owner.ID] || !got[threaded.ID] || len(got) != 5 {
		t.Errorf("mine flags: %v", got)
	}
	raw, _ := json.Marshal(rv.Comments)
	if strings.Contains(string(raw), "readerID") || strings.Contains(strings.ToLower(string(raw)), "reader_id") {
		t.Errorf("link id leaked: %s", raw)
	}
	for name, id := range map[string]int64{"another link's": theirs.ID, "owner's": owner.ID, "unknown": 9999} {
		if err := s.DeleteOwnComment(w.token, id); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := s.DeleteOwnComment(w.token, threaded.ID); !errors.Is(err, ErrInvalid) {
		t.Errorf("with replies: %v", err)
	}
	if err := s.DeleteOwnComment("nonsense-token-nonsense-token", mine.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("bad token: %v", err)
	}
	if err := s.DeleteOwnComment(w.token, mine.ID); err != nil {
		t.Fatalf("own: %v", err)
	}
	if ds, _ := s.DeletedAfter(0); len(ds) != 1 || ds[0].ID != mine.ID {
		t.Errorf("tombstones: %+v", ds)
	}
	if err := s.DeleteOwnComment(w.token, mine.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("twice: %v", err)
	}
	// a link of another space cannot reach it either
	if err := s.PutSpace(Space{Slug: "other", Title: "O", Reports: []string{keyB}}); err != nil {
		t.Fatal(err)
	}
	foreign, _ := s.CreateReader("other", "F", nil)
	if err := s.DeleteOwnComment(foreign.Token, theirs.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("other space: %v", err)
	}
}

func TestWithdrawReport(t *testing.T) {
	w := newWorld(t)
	s := w.s
	// B shares its figure asset with A and has one of its own
	own := putPNG(t, s, 3)
	if _, err := s.PushRevision(rev(keyB, 2, figBlock("f2", w.asset), figBlock("f3", own))); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSpace(Space{Slug: "sup", Title: "S", Reports: []string{keyA, keyB}}); err != nil {
		t.Fatal(err)
	}
	c, _ := s.AddComment(w.token, CommentInput{Report: keyB, Revision: w.revB.ID, Block: "f2", Body: "x", Anchor: &Anchor{Kind: "point", X: .1, Y: .1}})
	if _, err := s.AddComment(w.token, CommentInput{ParentID: c.ID, Body: "y"}); err != nil {
		t.Fatal(err)
	}
	keep, _ := s.AddComment(w.token, CommentInput{Report: keyA, Revision: w.revA.ID, Block: "t1", Body: "keep"})
	if _, _, err := s.Asset(w.token, own); err != nil {
		t.Fatal(err)
	}
	ids, err := s.WithdrawReport(keyB)
	if err != nil || len(ids) != 2 {
		t.Fatalf("withdraw: %v %v", ids, err)
	}
	if _, err := s.Report(w.token, keyB, 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("report: %v", err)
	}
	if _, err := s.Report(w.token, keyB, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("revision 1: %v", err)
	}
	if _, _, err := s.Asset(w.token, own); !errors.Is(err, ErrNotFound) {
		t.Errorf("own asset: %v", err)
	}
	if _, _, err := s.Asset(w.token, w.asset); err != nil {
		t.Errorf("shared asset must stay: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.dir, "assets", own)); !os.IsNotExist(err) {
		t.Errorf("orphan file remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.dir, "assets", w.other)); !os.IsNotExist(err) { // used only by B's revision 1
		t.Errorf("orphan file of revision 1 remains: %v", err)
	}
	if _, err := os.Stat(filepath.Join(s.dir, "assets", w.asset)); err != nil {
		t.Errorf("shared asset file must stay: %v", err)
	}
	sv, _ := s.Space(w.token)
	if len(sv.Reports) != 1 || sv.Reports[0].Key != keyA || sv.Comments != 1 {
		t.Errorf("space: %+v", sv)
	}
	if rv, _ := s.Report(w.token, keyA, 0); len(rv.Comments) != 1 || rv.Comments[0].ID != keep.ID {
		t.Errorf("other report's comments: %+v", rv.Comments)
	}
	if ds, _ := s.DeletedAfter(0); len(ds) != 2 {
		t.Errorf("tombstones: %+v", ds)
	}
	if _, err := s.WithdrawReport(keyB); !errors.Is(err, ErrNotFound) {
		t.Errorf("twice: %v", err)
	}
	if _, err := s.WithdrawReport("zz"); !errors.Is(err, ErrNotFound) {
		t.Errorf("bad key: %v", err)
	}
	// the key can be published again from revision 1, even with different content
	if _, err := s.PushRevision(rev(keyB, 1, Block{ID: "n", Kind: BlockText, Text: "new"})); err != nil {
		t.Errorf("re-push: %v", err)
	}
}

func TestUpgradeAddsQuoteColumn(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`CREATE TABLE comments(id INTEGER PRIMARY KEY AUTOINCREMENT, space TEXT NOT NULL, report TEXT NOT NULL, revision TEXT NOT NULL, block TEXT NOT NULL, anchor TEXT NOT NULL DEFAULT '', parent_id INTEGER NOT NULL DEFAULT 0, author TEXT NOT NULL, from_owner INTEGER NOT NULL DEFAULT 0, body TEXT NOT NULL, created_at INTEGER NOT NULL, resolved_at INTEGER, reader_id INTEGER NOT NULL DEFAULT 0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO comments(space,report,revision,block,author,body,created_at) VALUES('sup','r','v','b','Prof','old',1)`); err != nil {
		t.Fatal(err)
	}
	s, err := Open(db, dir)
	if err != nil {
		t.Fatal(err)
	}
	cs, err := s.CommentsAfter(0)
	if err != nil || len(cs) != 1 || cs[0].Body != "old" || cs[0].Quote != "" {
		t.Fatalf("after upgrade: %+v %v", cs, err)
	}
	if _, err := Open(db, dir); err != nil { // idempotent
		t.Fatal(err)
	}
}
