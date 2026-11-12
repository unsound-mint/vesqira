package relay

import (
	"errors"
	"strings"
	"testing"
)

// The cursor is per addressee: requests filed to someone else never take a change number from a key holder's sequence, so a
// member filing to themselves cannot push another key holder's next change past the page's jump bound.
func TestChangeNumbersArePerAddressee(t *testing.T) {
	s, _ := openStore(t)
	cy := Actor{ID: "cy", Via: "token", TokenID: "tok-cy", Instance: inst2}
	if err := s.PutInbox(wsA, "cy", cy, vgtInbox()); err != nil {
		t.Fatal(err)
	}
	first := fileReq(t, s, "bob", "ada", 1)
	for i := 0; i < 5; i++ {
		fileReq(t, s, "cy", "cy", 1)
	}
	next := fileReq(t, s, "bob", "ada", 1)
	if next.Change != first.Change+1 {
		t.Fatalf("ada's change went %d -> %d with requests to cy between", first.Change, next.Change)
	}
}

// Only a rise in rank, a first end state or a first run hash moves the cursor; counts and repeated codes are updated in place,
// so a reporting computer cannot spin the workspace's change numbers.
func TestReportCountsDoNotMoveTheCursor(t *testing.T) {
	s, _ := openStore(t)
	r := toReview(t, s, 1)
	if _, _, err := s.Claim(wsA, r.ID, "ada", adaToken, goodClaim(shaA, inst1)); err != nil {
		t.Fatal(err)
	}
	rep := func(seq int64, st string, counts map[string]int, hash string) Row {
		t.Helper()
		got, _, err := s.Report(wsA, r.ID, "ada", adaToken, Report{Instance: inst1, Seq: seq, State: st, Counts: counts, RunHash: hash})
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	running := rep(1, StateRunning, map[string]int{"running": 1}, "")
	events := len(mustEvents(t, s, r.ID))
	for i := 2; i < 30; i++ {
		got := rep(int64(i), StateRunning, map[string]int{"running": 1, "finished": i}, "")
		if got.Change != running.Change {
			t.Fatalf("a count change moved the cursor: %d -> %d", running.Change, got.Change)
		}
	}
	if n := len(mustEvents(t, s, r.ID)); n != events {
		t.Fatalf("count changes wrote %d events", n-events)
	}
	hashed := rep(40, StateRunning, nil, strings.Repeat("a", 64))
	if hashed.Change <= running.Change {
		t.Fatal("the first run hash did not move the cursor")
	}
	if again := rep(41, StateRunning, nil, strings.Repeat("b", 64)); again.Change != hashed.Change {
		t.Fatal("a second run hash moved the cursor")
	}
	if done := rep(42, StateFinished, nil, ""); done.Change <= hashed.Change {
		t.Fatal("the end state did not move the cursor")
	}
}

func TestReportsPerRequestAreCapped(t *testing.T) {
	s, _ := openStore(t)
	r := toReview(t, s, 1)
	if _, _, err := s.Claim(wsA, r.ID, "ada", adaToken, goodClaim(shaA, inst1)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Report(wsA, r.ID, "ada", adaToken, Report{Instance: inst1, Seq: MaxReports, State: StateRunning}); err != nil {
		t.Fatalf("the last allowed report: %v", err)
	}
	var le LimitError
	if _, _, err := s.Report(wsA, r.ID, "ada", adaToken, Report{Instance: inst1, Seq: MaxReports + 1, State: StateFinished}); !errors.As(err, &le) {
		t.Fatalf("a report past the cap: %v", err)
	}
}

// The server runs the same inbox check as the key holder's computer, so a relay token cannot publish what the computer would not.
func TestPutInboxRunsCheckInbox(t *testing.T) {
	s, _ := openStore(t)
	custom := vgtInbox()
	custom.Labels[0].Templates[0].Template = "custom"
	path := vgtInbox()
	path.Labels[0].Templates[0].Keys = append(path.Labels[0].Templates[0].Keys, Bound{Key: "input_file"})
	huge := vgtInbox()
	huge.Labels[0].Templates[0].Limits.MaxCPUs = 1 << 20
	for name, in := range map[string]Inbox{"custom template": custom, "path key": path, "huge caps": huge} {
		if err := s.PutInbox(wsA, "ada", adaToken, in); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestPlainTextRefusesFormatCharacters(t *testing.T) {
	for _, s := range []string{"run ‮dn=4", "zero​width", "\u2066isolate"} {
		if err := PlainText(s, "note"); !errors.Is(err, ErrInvalid) {
			t.Errorf("%q: %v", s, err)
		}
	}
	if err := PlainText("dn = 4, résumé\tok\n", "note"); err != nil {
		t.Fatal(err)
	}
}

func TestLabelSafeRefusesPartsOfTheDestination(t *testing.T) {
	dests := []string{"denis@login.hpc-a.example.edu"}
	for _, l := range []string{"hpc-a big", "denis box", "Login node", "example"} {
		if err := LabelSafe(l, dests); err == nil {
			t.Errorf("label %q naming part of the destination was accepted", l)
		}
	}
	if err := LabelSafe("Cluster A", dests); err != nil {
		t.Fatalf("an unrelated label: %v", err)
	}
}

// A compromised server cannot grow this computer's catalog without bound: refused requests past MaxRefusedKept keep no content,
// and past MaxRowsLocal nothing more is stored.
func TestLocalCatalogIsBounded(t *testing.T) {
	l := openLocal(t)
	for id := int64(1); id <= MaxRefusedKept+1; id++ {
		if _, _, err := l.Upsert(origin1, localReq(id, StateRequested), "refused"); err != nil {
			t.Fatalf("request %d: %v", id, err)
		}
	}
	// the rest up to the cap in one transaction: Upsert one by one is too slow for a unit test, and the count is all that matters
	tx, err := l.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for id := int64(MaxRefusedKept + 2); id <= MaxRowsLocal; id++ {
		if _, err := tx.Exec(`INSERT INTO relayed_requests(workspace_id, origin, server_id, request, server_state, local_state, refusal, received_at, updated_at)
			VALUES(1,?,?,'{}','requested','closed','',0,0)`, origin1, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	kept, _, _ := l.Upsert(origin1, localReq(1, StateRequested), "")
	if kept.Request.Requester != "bob" {
		t.Fatalf("an early refused request lost its content: %+v", kept.Request)
	}
	stub, _, _ := l.Upsert(origin1, localReq(MaxRefusedKept+1, StateRequested), "")
	if stub.Request.Requester != "" || stub.Request.Ask.Template != "" || !strings.Contains(stub.Refusal, "stored without its content") {
		t.Fatalf("a refused request past the cap kept its content: %+v %q", stub.Request, stub.Refusal)
	}
	if _, _, err := l.Upsert(origin1, localReq(MaxRowsLocal+1, StateRequested), ""); !errors.Is(err, ErrFull) {
		t.Fatalf("a request past the cap: %v", err)
	}
}
