package boards

import (
	"bytes"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
)

func newStore(t *testing.T) (*Store, string) {
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
	return s, dir
}

func pngBytes(t *testing.T, w, h int, shade uint8) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = shade
	}
	_ = color.Gray{}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func fig(t *testing.T, s *Store, title string, shade uint8) Figure {
	t.Helper()
	f, err := s.CreateFigure(NewFigure{DatasetID: 7, Title: title, Caption: "c", Provenance: json.RawMessage(`{"dataset":"a.fits","channel":3}`)}, pngBytes(t, 8, 6, shade))
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestFigureIsPinnedEvidenceAndTextIsEditable(t *testing.T) {
	s, _ := newStore(t)
	f := fig(t, s, "Peak channel", 10)
	if f.Width != 8 || f.Height != 6 || len(f.Revision) != 64 {
		t.Fatalf("%+v", f)
	}
	again := fig(t, s, "Another title", 10) // the same picture and provenance is the same evidence
	if again.ID != f.ID {
		t.Fatalf("identical evidence must not create a second figure: %d vs %d", again.ID, f.ID)
	}
	if err := s.SetText(f.ID, "Renamed", "new caption"); err != nil {
		t.Fatal(err)
	}
	g, _ := s.Figure(f.ID)
	if g.Title != "Renamed" || g.Revision != f.Revision || g.PreviewSHA != f.PreviewSHA {
		t.Fatalf("editing text must not change the evidence: %+v", g)
	}
	if other := fig(t, s, "Other", 99); other.Revision == f.Revision {
		t.Fatal("different pictures must have different revisions")
	}
}

func TestBadPreviewsAreRefused(t *testing.T) {
	s, _ := newStore(t)
	good := pngBytes(t, 4, 4, 1)
	cases := map[string][]byte{
		"empty":     nil,
		"not png":   []byte("GIF89a not a png"),
		"truncated": good[:len(good)-12],
		"too big":   pngBytes(t, MaxPNGSide+1, 2, 1),
	}
	for name, b := range cases {
		if _, err := s.CreateFigure(NewFigure{DatasetID: 1, Title: "t", Provenance: json.RawMessage(`{}`)}, b); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err=%v", name, err)
		}
	}
	if _, err := s.CreateFigure(NewFigure{DatasetID: 1, Provenance: json.RawMessage(`{}`)}, good); !errors.Is(err, ErrInvalid) {
		t.Errorf("untitled figure accepted: %v", err)
	}
}

func TestBoardOrderingNotesAndRemoval(t *testing.T) {
	s, _ := newStore(t)
	b, _ := s.CreateBoard("Draft")
	a, c, d := fig(t, s, "A", 1), fig(t, s, "C", 2), fig(t, s, "D", 3)
	for _, f := range []Figure{a, c, d} {
		if err := s.AddItem(b.ID, f.ID); err != nil {
			t.Fatal(err)
		}
	}
	titles := func() string {
		bb, _ := s.Board(b.ID)
		var out []string
		for _, it := range bb.Items {
			out = append(out, it.Figure.Title)
		}
		return strings.Join(out, "")
	}
	if titles() != "ACD" {
		t.Fatal(titles())
	}
	s.MoveItem(b.ID, 2, -1)
	if titles() != "ADC" {
		t.Fatal(titles())
	}
	s.SetNote(b.ID, 0, "look here")
	s.RemoveItem(b.ID, 1)
	bb, _ := s.Board(b.ID)
	if titles() != "AC" || bb.Items[0].Note != "look here" || bb.Items[1].Position != 1 {
		t.Fatalf("%s %+v", titles(), bb.Items)
	}
	if err := s.MoveItem(b.ID, 0, -1); !errors.Is(err, ErrNotFound) {
		t.Errorf("moving past the top: %v", err)
	}
	if err := s.AddItem(b.ID, 9999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown figure: %v", err)
	}
}

func TestSnapshotIsFrozenAndTokenIsNeverStored(t *testing.T) {
	s, dir := newStore(t)
	b, _ := s.CreateBoard("For my supervisor")
	f := fig(t, s, "Peak", 5)
	s.AddItem(b.ID, f.ID)
	sn, tok, err := s.Publish(b.ID, "v1", time.Hour)
	if err != nil || len(tok) < 40 || sn.Items != 1 {
		t.Fatalf("%v %v %+v", err, len(tok), sn)
	}
	// the token is shown once; only its hash is in the database
	for _, table := range []string{"snapshots", "comments", "boards", "figures"} {
		rows, _ := s.db.Query(`SELECT * FROM ` + table)
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			rows.Scan(ptrs...)
			for _, v := range vals {
				if strings.Contains(toString(v), tok) {
					t.Fatalf("token stored in plain text in %s", table)
				}
			}
		}
		rows.Close()
	}
	// editing the board and the figure afterwards does not change what the reviewer sees
	s.SetText(f.ID, "Changed after sharing", "x")
	s.SetNote(b.ID, 0, "late note")
	s.AddItem(b.ID, fig(t, s, "Late", 77).ID)
	r, err := s.Review(tok)
	if err != nil || len(r.Snapshot.Items) != 1 || r.Snapshot.Items[0].Title != "Peak" || r.Snapshot.Items[0].Note != "" {
		t.Fatalf("snapshot must be frozen: %+v %v", r, err)
	}
	_ = dir
}

func toString(v any) string {
	switch x := v.(type) {
	case []byte:
		return string(x)
	case string:
		return x
	}
	return ""
}

func TestWhatAReviewerCanSeeIsAWhitelist(t *testing.T) {
	want := map[string]bool{"revision": true, "title": true, "caption": true, "note": true, "previewSha": true, "width": true, "height": true, "provenance": true}
	rt := reflect.TypeOf(FrozenItem{})
	for i := 0; i < rt.NumField(); i++ {
		tag := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]
		if !want[tag] {
			t.Errorf("FrozenItem exposes %q: new reviewer-visible fields need a deliberate review", tag)
		}
	}
	for _, banned := range []string{"path", "datasetId", "token", "credential"} {
		for _, typ := range []reflect.Type{reflect.TypeOf(FrozenItem{}), reflect.TypeOf(Frozen{}), reflect.TypeOf(Review{}), reflect.TypeOf(Comment{})} {
			for i := 0; i < typ.NumField(); i++ {
				if strings.EqualFold(strings.Split(typ.Field(i).Tag.Get("json"), ",")[0], banned) {
					t.Errorf("%s has a %q field", typ.Name(), banned)
				}
			}
		}
	}
}

func TestTokensExpireRevokeAndNeverCrossSnapshots(t *testing.T) {
	s, _ := newStore(t)
	b1, _ := s.CreateBoard("one")
	b2, _ := s.CreateBoard("two")
	f1, f2 := fig(t, s, "F1", 1), fig(t, s, "F2", 2)
	s.AddItem(b1.ID, f1.ID)
	s.AddItem(b2.ID, f2.ID)
	sn1, tok1, _ := s.Publish(b1.ID, "", time.Hour)
	_, tok2, _ := s.Publish(b2.ID, "", time.Hour)

	if _, err := s.ReviewPreview(tok1, f1.PreviewSHA); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReviewPreview(tok1, f2.PreviewSHA); !errors.Is(err, ErrNotFound) {
		t.Errorf("a valid token opened a figure of another snapshot: %v", err)
	}
	if _, err := s.AddComment(tok1, CommentInput{FigureRevision: f2.Revision, Author: "x", Body: "hello"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("comment on another snapshot's figure: %v", err)
	}
	c, err := s.AddComment(tok2, CommentInput{FigureRevision: f2.Revision, Author: "Supervisor", Body: "Why this stretch?"})
	if err != nil || c.Author != "Supervisor" {
		t.Fatal(err)
	}
	if r, _ := s.Review(tok1); len(r.Comments) != 0 {
		t.Error("comments leaked across snapshots")
	}
	// unknown, malformed, revoked and expired tokens are one indistinguishable error
	s.Revoke(sn1.ID)
	_, errRevoked := s.Review(tok1)
	_, errUnknown := s.Review("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	_, errShort := s.Review("x")
	if !errors.Is(errRevoked, ErrNotFound) || errRevoked.Error() != errUnknown.Error() || errUnknown.Error() != errShort.Error() {
		t.Errorf("token failures must look identical: %v %v %v", errRevoked, errUnknown, errShort)
	}
	if _, err := s.ReviewPreview(tok1, f1.PreviewSHA); !errors.Is(err, ErrNotFound) {
		t.Error("a revoked token still reads previews")
	}
	_, short, _ := s.Publish(b2.ID, "", time.Millisecond*1500)
	s.db.Exec(`UPDATE snapshots SET expires_at = ? WHERE token_hash = ?`, time.Now().Add(-time.Minute).Unix(), hashToken(short))
	if _, err := s.Review(short); !errors.Is(err, ErrNotFound) {
		t.Error("an expired token still works")
	}
	if _, _, err := s.Publish(b2.ID, "", 365*24*time.Hour); !errors.Is(err, ErrInvalid) {
		t.Error("expiry beyond 90 days accepted")
	}
	empty, _ := s.CreateBoard("empty")
	if _, _, err := s.Publish(empty.ID, "", time.Hour); !errors.Is(err, ErrInvalid) {
		t.Error("an empty board was published")
	}
}

func TestPreviewLookupRefusesPathTricks(t *testing.T) {
	s, dir := newStore(t)
	os.WriteFile(filepath.Join(dir, "secret.png"), pngBytes(t, 2, 2, 1), 0o600)
	for _, bad := range []string{"../secret", "../../etc/passwd", strings.Repeat("a", 63) + "/", strings.Repeat("z", 64), ""} {
		if _, err := s.Preview(bad); !errors.Is(err, ErrNotFound) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestClipNeverSplitsACharacterAndTheCommentCapHolds(t *testing.T) {
	if got := clip("héllo wörld", 4); got != "héll" {
		t.Errorf("%q", got)
	}
	s, _ := newStore(t)
	b, _ := s.CreateBoard("b")
	f := fig(t, s, "F", 1)
	s.AddItem(b.ID, f.ID)
	_, tok, _ := s.Publish(b.ID, "", time.Hour)
	for i := 0; i < MaxCommentsPS; i++ {
		if _, err := s.AddComment(tok, CommentInput{FigureRevision: f.Revision, Author: "r", Body: "c"}); err != nil {
			t.Fatalf("comment %d: %v", i, err)
		}
	}
	if _, err := s.AddComment(tok, CommentInput{FigureRevision: f.Revision, Author: "r", Body: "one too many"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("limit not enforced: %v", err)
	}
}

func TestPreviewIsWrittenAtomically(t *testing.T) {
	s, dir := newStore(t)
	fig(t, s, "A", 9)
	entries, _ := os.ReadDir(filepath.Join(dir, "figures"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Errorf("temporary file left behind: %s", e.Name())
		}
	}
}

func TestThreadsAnchorsRepliesAndResolve(t *testing.T) {
	s, _ := newStore(t)
	b, _ := s.CreateBoard("one")
	b2, _ := s.CreateBoard("two")
	f, f2 := fig(t, s, "F1", 1), fig(t, s, "F2", 2)
	s.AddItem(b.ID, f.ID)
	s.AddItem(b2.ID, f2.ID)
	sn, tok, _ := s.Publish(b.ID, "", time.Hour)
	sn2, tok2, _ := s.Publish(b2.ID, "", time.Hour)

	// a comment can be placed on the picture, with the place validated
	for name, a := range map[string]*Anchor{
		"outside":      {Kind: "point", X: 1.2, Y: 0.5},
		"negative":     {Kind: "point", X: -0.1, Y: 0.5},
		"nan":          {Kind: "point", X: math.NaN(), Y: 0.5},
		"unknown kind": {Kind: "circle", X: 0.5, Y: 0.5},
		"inverted box": {Kind: "box", X: 0.6, Y: 0.1, X1: 0.4, Y1: 0.3},
	} {
		if _, err := s.AddComment(tok, CommentInput{FigureRevision: f.Revision, Body: "here", Anchor: a}); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	root, err := s.AddComment(tok, CommentInput{FigureRevision: f.Revision, Author: "Prof", Body: "What is this feature?", Anchor: &Anchor{Kind: "box", X: 0.2, Y: 0.2, X1: 0.4, Y1: 0.5}})
	if err != nil || root.Anchor == nil || root.ParentID != 0 {
		t.Fatalf("anchored comment: %+v %v", root, err)
	}
	// replies stay in the thread, take its figure, and cannot be placed again
	if _, err := s.AddComment(tok, CommentInput{ParentID: root.ID, Author: "Prof", Body: "also the edge", Anchor: &Anchor{Kind: "point", X: .1, Y: .1}}); !errors.Is(err, ErrInvalid) {
		t.Errorf("a reply with its own anchor: %v", err)
	}
	reply, err := s.OwnerReply(sn.ID, CommentInput{ParentID: root.ID, Body: "It is the beam sidelobe."})
	if err != nil || !reply.FromOwner || reply.FigureRevision != f.Revision || reply.Author != "Author" {
		t.Fatalf("owner reply: %+v %v", reply, err)
	}
	if _, err := s.AddComment(tok, CommentInput{ParentID: reply.ID, Body: "reply to a reply"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("threads are one level deep: %v", err)
	}
	// a thread of another snapshot is not reachable with this token or from this snapshot's owner view
	other, _ := s.AddComment(tok2, CommentInput{FigureRevision: f2.Revision, Body: "elsewhere"})
	if _, err := s.AddComment(tok, CommentInput{ParentID: other.ID, Body: "cross"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("a reply into another snapshot's thread: %v", err)
	}
	if _, err := s.OwnerReply(sn.ID, CommentInput{ParentID: other.ID, Body: "cross"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("an owner reply into another snapshot's thread: %v", err)
	}
	_ = sn2

	// only the author resolves, and only a thread's first comment can be resolved
	if err := s.SetResolved(reply.ID, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("resolving a reply: %v", err)
	}
	if err := s.SetResolved(root.ID, true); err != nil {
		t.Fatal(err)
	}
	cs, _ := s.Comments(sn.ID)
	if len(cs) != 2 || cs[0].ResolvedAt == nil || cs[1].ResolvedAt != nil {
		t.Fatalf("resolved mark: %+v", cs)
	}
	if rv, _ := s.Review(tok); rv.Comments[0].Anchor == nil || rv.Comments[0].ResolvedAt == nil || !rv.Comments[1].FromOwner {
		t.Errorf("the reviewer sees the thread, its place and whether it is closed: %+v", rv.Comments)
	}
	s.SetResolved(root.ID, false)
	if cs, _ := s.Comments(sn.ID); cs[0].ResolvedAt != nil {
		t.Error("a thread can be reopened")
	}
}
