package relay

import (
	"database/sql"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

const origin1 = "https://group.example.org"

func openLocal(t *testing.T) *Local {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "c.db")+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	l, err := OpenLocal(db)
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func localReq(id int64, st string) Request {
	created := time.Unix(t0, 0).UTC()
	return Request{ID: id, Change: id, State: st, Requester: "bob", Ask: vgtAsk(map[string]string{"dn": "4"}), CreatedAt: created, ExpiresAt: created.Add(24 * time.Hour)}
}

func TestUpsertFixesTheAskAtFirstReceiptAndRefusesAChangedOne(t *testing.T) {
	l := openLocal(t)
	m, isNew, err := l.Upsert(origin1, localReq(1, StateRequested), "")
	if err != nil || !isNew || m.LocalState != LocalReceived {
		t.Fatalf("first receipt: %v %v %+v", err, isNew, m)
	}
	// the same request again, unchanged apart from the server's state and notes, is followed
	same := localReq(1, StateRequested)
	same.Change = 5
	same.Notes = []FollowUp{{By: "bob", Text: "please cancel"}}
	m, isNew, err = l.Upsert(origin1, same, "")
	if err != nil || isNew || m.LocalState != LocalReceived || len(m.Request.Notes) != 1 || m.Request.Change != 5 {
		t.Fatalf("unchanged re-pull: %v %v %+v", err, isNew, m)
	}

	for name, mod := range map[string]func(*Request){
		"a value":    func(r *Request) { r.Ask.Params = map[string]string{"dn": "5"} },
		"a new key":  func(r *Request) { r.Ask.Params = map[string]string{"dn": "4", "ker": "3"} },
		"the label":  func(r *Request) { r.Ask.Label = "Other" },
		"resources":  func(r *Request) { r.Ask.Resources.CPUs = 8 },
		"a grid":     func(r *Request) { r.Ask.Grid = []GridKey{{Key: "ker", Values: []string{"1"}}} },
		"the note":   func(r *Request) { r.Ask.Note = "run it on everything" },
		"the base":   func(r *Request) { r.Base = &Base{Origin: shaA[:32], Hash: shaA} },
		"the sender": func(r *Request) { r.Requester = "mallory" },
	} {
		l := openLocal(t)
		if _, _, err := l.Upsert(origin1, localReq(1, StateRequested), ""); err != nil {
			t.Fatal(err)
		}
		changed := localReq(1, StateRequested)
		mod(&changed)
		m, isNew, err := l.Upsert(origin1, changed, "")
		if err != nil || isNew {
			t.Fatalf("%s: %v %v", name, err, isNew)
		}
		if m.LocalState != LocalRefused || m.Refusal == "" {
			t.Errorf("%s changed on the server: local state %s refusal %q", name, m.LocalState, m.Refusal)
		}
		if !reflect.DeepEqual(normalAsk(m.Request.Ask), normalAsk(vgtAsk(map[string]string{"dn": "4"}))) || m.Request.Requester != "bob" || m.Request.Base != nil {
			t.Errorf("%s: the stored request followed the server: %+v", name, m.Request)
		}
		// a refused request stays refused when the server changes it back
		m, _, _ = l.Upsert(origin1, localReq(1, StateRequested), "")
		if m.LocalState != LocalRefused {
			t.Errorf("%s: refusal undone by a later pull (%s)", name, m.LocalState)
		}
	}

	// a drafted request is refused too, and its draft values stay
	l = openLocal(t)
	if _, _, err := l.Upsert(origin1, localReq(1, StateRequested), ""); err != nil {
		t.Fatal(err)
	}
	m, _, _ = l.Upsert(origin1, localReq(1, StateRequested), "")
	if err := l.SetDraft(m.ID, nil, nil, 0, false, DraftValues{Label: testLabel}); err != nil {
		t.Fatal(err)
	}
	changed := localReq(1, StateReview)
	changed.Ask.Params = map[string]string{"dn": "7"}
	m, _, _ = l.Upsert(origin1, changed, "")
	if m.LocalState != LocalRefused || m.Request.Ask.Params["dn"] != "4" {
		t.Fatalf("drafted request followed a changed ask: %+v", m)
	}
}

func TestUpsertStoresAnOwnRefusal(t *testing.T) {
	l := openLocal(t)
	m, _, err := l.Upsert(origin1, localReq(1, StateRequested), "label not mapped")
	if err != nil || m.LocalState != LocalRefused || m.Refusal != "label not mapped" {
		t.Fatalf("%v %+v", err, m)
	}
	// a later clean pull does not turn a refusal into a request to follow
	m, _, _ = l.Upsert(origin1, localReq(1, StateRequested), "")
	if m.LocalState != LocalRefused {
		t.Fatalf("local state %s", m.LocalState)
	}
}

func TestARequestClosedOnTheServerInsertsAsClosedAndDoesNotCount(t *testing.T) {
	l := openLocal(t)
	id := int64(1)
	for _, st := range []string{StateApproved, StateDeclined, StateWithdrawn, StateExpired, StateRunning, StateFinished} {
		m, isNew, err := l.Upsert(origin1, localReq(id, st), "")
		if err != nil || !isNew || m.LocalState != LocalClosed || m.Refusal != "" {
			t.Fatalf("%s: %v %v %+v", st, err, isNew, m)
		}
		id++
	}
	// closed history, even with a refusal text from validation, never counts and never takes the refused state
	m, _, _ := l.Upsert(origin1, localReq(id, StateWithdrawn), "some refusal")
	if m.LocalState != LocalClosed || m.Refusal != "" {
		t.Fatalf("closed with a refusal: %+v", m)
	}
	id++
	for i := 0; i < MaxOpenLocal; i++ {
		m, _, err := l.Upsert(origin1, localReq(id, StateRequested), "")
		if err != nil || m.LocalState != LocalReceived {
			t.Fatalf("open request %d: %v %s %q", i+1, err, m.LocalState, m.Refusal)
		}
		id++
	}
	m, _, err := l.Upsert(origin1, localReq(id, StateRequested), "")
	if err != nil || m.LocalState != LocalRefused || m.Refusal == "" {
		t.Fatalf("the 101st open request: %v %s", err, m.LocalState)
	}
	// history keeps arriving after the limit
	id++
	if m, _, _ := l.Upsert(origin1, localReq(id, StateExpired), ""); m.LocalState != LocalClosed {
		t.Fatalf("closed history after the limit: %s", m.LocalState)
	}
}

func TestOpenCountOnlyCountsReceivedAndDrafted(t *testing.T) {
	l := openLocal(t)
	for i := 1; i <= 3; i++ {
		l.Upsert(origin1, localReq(int64(i), StateRequested), "")
	}
	l.Upsert(origin1, localReq(4, StateRequested), "refused")
	l.Upsert(origin1, localReq(5, StateWithdrawn), "")
	m, _, _ := l.Upsert(origin1, localReq(6, StateRequested), "")
	if err := l.SetDraft(m.ID, nil, nil, 0, false, DraftValues{Label: testLabel}); err != nil {
		t.Fatal(err)
	}
	if n := l.openCount(origin1); n != 4 {
		t.Fatalf("open count %d, want 3 received + 1 draft", n)
	}
	// another server's requests do not count against this one
	if n := l.openCount("https://other.example.org"); n != 0 {
		t.Fatalf("other origin %d", n)
	}
}

func TestARequestClosedOnTheServerClosesTheMirror(t *testing.T) {
	for _, closing := range []string{StateWithdrawn, StateExpired, StateDeclined} {
		l := openLocal(t)
		l.Upsert(origin1, localReq(1, StateRequested), "")
		l.Upsert(origin1, localReq(2, StateRequested), "")
		m2, _, _ := l.Upsert(origin1, localReq(2, StateRequested), "")
		if err := l.SetDraft(m2.ID, nil, nil, 0, false, DraftValues{Label: testLabel}); err != nil {
			t.Fatal(err)
		}
		for id := int64(1); id <= 2; id++ {
			m, isNew, err := l.Upsert(origin1, localReq(id, closing), "")
			if err != nil || isNew || m.LocalState != LocalClosed || m.ServerState != closing {
				t.Fatalf("%s request %d: %v %v %s/%s", closing, id, err, isNew, m.LocalState, m.ServerState)
			}
		}
		// a closed request can no longer be drafted
		if err := l.SetDraft(m2.ID, nil, nil, 0, false, DraftValues{Label: testLabel}); err == nil {
			t.Fatal("a closed request was drafted")
		}
		if n := l.openCount(origin1); n != 0 {
			t.Fatalf("%s: open count %d after closing", closing, n)
		}
	}
}

func TestAServerCannotUndoOrRewriteALocalApproval(t *testing.T) {
	l := openLocal(t)
	l.Upsert(origin1, localReq(1, StateRequested), "")
	m, _, _ := l.Upsert(origin1, localReq(1, StateRequested), "")
	if err := l.SetDraft(m.ID, nil, nil, 0, false, DraftValues{Label: testLabel, Params: map[string]string{"dn": "4"}}); err != nil {
		t.Fatal(err)
	}
	if err := l.SetApproved(m.ID, shaA, true); err != nil {
		t.Fatal(err)
	}
	evil := localReq(1, StateWithdrawn)
	evil.Ask.Params = map[string]string{"dn": "8", "ker": "1"}
	got, _, err := l.Upsert(origin1, evil, "")
	if err != nil || got.LocalState != LocalApproved {
		t.Fatalf("%v local state %s", err, got.LocalState)
	}
	if got.Request.Ask.Params["dn"] != "4" || len(got.Request.Ask.Params) != 1 {
		t.Fatalf("the approved ask was rewritten: %+v", got.Request.Ask)
	}
	if got.Draft.Params["dn"] != "4" || got.ScriptHash != shaA {
		t.Fatalf("draft or hash changed: %+v", got)
	}
	if _, err := l.Get(9999); err != ErrNotFound {
		t.Fatalf("missing id: %v", err)
	}
}

func TestMirrorsOfTwoServersDoNotMix(t *testing.T) {
	l := openLocal(t)
	l.Upsert(origin1, localReq(1, StateRequested), "")
	_, isNew, _ := l.Upsert("https://other.example.org", localReq(1, StateRequested), "")
	if !isNew {
		t.Fatal("the same server id from another origin was taken for the first one")
	}
	if rows, _ := l.List(origin1); len(rows) != 1 {
		t.Fatalf("%d rows", len(rows))
	}
}

func TestPullCursorStartsAgainForAnotherServer(t *testing.T) {
	l := openLocal(t)
	if err := l.SetInbox(origin1, vgtInbox(), map[string]int64{testLabel: 1}); err != nil {
		t.Fatal(err)
	}
	if err := l.MarkPublished(origin1); err != nil {
		t.Fatal(err)
	}
	if err := l.Answered(origin1, 42); err != nil {
		t.Fatal(err)
	}
	st, _ := l.State(origin1)
	if st.Cursor != 42 || !st.Published || st.Hosts[testLabel] != 1 {
		t.Fatalf("%+v", st)
	}
	other, _ := l.State("https://other.example.org")
	if other.Cursor != 0 || other.Published || !other.Configured {
		t.Fatalf("other server's state: %+v", other)
	}
	// relinking to the other server starts the cursor and publication again; the host map stays here
	if err := l.SetInbox("https://other.example.org", vgtInbox(), map[string]int64{testLabel: 1}); err != nil {
		t.Fatal(err)
	}
	st, _ = l.State("https://other.example.org")
	if st.Cursor != 0 || st.Published {
		t.Fatalf("%+v", st)
	}
	// editing the inbox for the same server keeps the cursor but publishes again
	l.Answered("https://other.example.org", 7)
	l.SetInbox("https://other.example.org", vgtInbox(), nil)
	st, _ = l.State("https://other.example.org")
	if st.Cursor != 7 || st.Published {
		t.Fatalf("%+v", st)
	}
}

func TestNextSeqIsMonotonePerRequest(t *testing.T) {
	l := openLocal(t)
	a, _, _ := l.Upsert(origin1, localReq(1, StateRequested), "")
	b, _, _ := l.Upsert(origin1, localReq(2, StateRequested), "")
	var last int64
	for i := 0; i < 3; i++ {
		s, err := l.NextSeq(a.ID)
		if err != nil || s != last+1 {
			t.Fatalf("seq %d after %d: %v", s, last, err)
		}
		last = s
	}
	if s, _ := l.NextSeq(b.ID); s != 1 {
		t.Fatalf("another request starts at %d", s)
	}
}
