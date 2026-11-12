package relay

import (
	"database/sql"
	"errors"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

const (
	wsA = workspace.ID(1)
	wsB = workspace.ID(2)
	t0  = int64(1_800_000_000)
)

var (
	bobSession = Actor{ID: "bob", Via: "session"}
	adaToken   = Actor{ID: "ada", Via: "token", TokenID: "tok-ada", Instance: inst1}
)

type testClock struct{ v atomic.Int64 }

func (c *testClock) Set(unix int64) { c.v.Store(unix) }
func (c *testClock) Now() time.Time { return time.Unix(c.v.Load(), 0) }

// openStore opens a store the way the catalog opens its database: one connection, foreign keys on.
func openStore(t *testing.T) (*Store, *testClock) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "c.db")+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	s, err := Open(db)
	if err != nil {
		t.Fatal(err)
	}
	clk := &testClock{}
	clk.Set(t0)
	s.Now = clk.Now
	if err := s.PutInbox(wsA, "ada", adaToken, vgtInbox()); err != nil {
		t.Fatal(err)
	}
	return s, clk
}

func fileReq(t *testing.T, s *Store, requester, addressee string, days int) Row {
	t.Helper()
	r, err := s.File(wsA, requester, addressee, vgtAsk(map[string]string{"dn": "4"}), nil, days, Actor{ID: requester, Via: "session"})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func toReview(t *testing.T, s *Store, days int) Row {
	t.Helper()
	r := fileReq(t, s, "bob", "ada", days)
	r, err := s.Review(wsA, r.ID, "ada", adaToken)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func goodClaim(hash, inst string) Claim {
	return Claim{Instance: inst, ScriptHash: hash, Params: map[string]string{"dn": "4"}, Label: testLabel}
}

func wantConflict(t *testing.T, err error, state string) {
	t.Helper()
	var ce ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("error %v (%T) is not a ConflictError", err, err)
	}
	if ce.State != state {
		t.Fatalf("conflict state %q, want %q", ce.State, state)
	}
}

func state(t *testing.T, s *Store, id int64) string {
	t.Helper()
	r, err := s.Get(wsA, id)
	if err != nil {
		t.Fatal(err)
	}
	return r.State
}

func TestFileIsRefusedWithoutAnOpenInbox(t *testing.T) {
	s, _ := openStore(t)
	_, err := s.File(wsA, "bob", "eve", vgtAsk(nil), nil, 0, bobSession)
	if !errors.Is(err, ErrNoInbox) {
		t.Fatalf("no inbox: %v", err)
	}
	_, err = s.File(wsB, "bob", "ada", vgtAsk(nil), nil, 0, bobSession)
	if !errors.Is(err, ErrNoInbox) {
		t.Fatalf("an inbox of another workspace must not count: %v", err)
	}
	paused := vgtInbox()
	paused.Paused = true
	if err := s.PutInbox(wsA, "ada", adaToken, paused); err != nil {
		t.Fatal(err)
	}
	if _, err := s.File(wsA, "bob", "ada", vgtAsk(nil), nil, 0, bobSession); !errors.Is(err, ErrNoInbox) {
		t.Fatalf("paused inbox: %v", err)
	}
}

func TestFileRunsTheServersFirstFilter(t *testing.T) {
	s, _ := openStore(t)
	for name, a := range map[string]Ask{
		"outside the bound": vgtAsk(map[string]string{"dn": "99"}),
		"forbidden key":     vgtAsk(map[string]string{"output_dir": "/tmp/x"}),
		"injected value":    vgtAsk(map[string]string{"ker": "1;id"}),
	} {
		if _, err := s.File(wsA, "bob", "ada", a, nil, 0, bobSession); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	for _, d := range []int{-1, MaxExpiryDays + 1} {
		if _, err := s.File(wsA, "bob", "ada", vgtAsk(nil), nil, d, bobSession); !errors.Is(err, ErrInvalid) {
			t.Errorf("%d days: %v", d, err)
		}
	}
	rows, _ := s.List(wsA)
	if len(rows) != 0 {
		t.Fatalf("%d requests stored from refused filings", len(rows))
	}
}

func TestFileRateLimits(t *testing.T) {
	s, _ := openStore(t)
	for i := 0; i < MaxOpenPerPair; i++ {
		fileReq(t, s, "bob", "ada", 0)
	}
	_, err := s.File(wsA, "bob", "ada", vgtAsk(nil), nil, 0, bobSession)
	var le LimitError
	if !errors.As(err, &le) {
		t.Fatalf("the 11th open request: %v", err)
	}
	// another requester is not affected
	fileReq(t, s, "cy", "ada", 0)
}

func TestARequestReachesOnlyItsAddresseeInItsWorkspace(t *testing.T) {
	s, _ := openStore(t)
	r := fileReq(t, s, "bob", "ada", 0)
	got, err := s.Page(wsA, "ada", 0)
	if err != nil || len(got) != 1 || got[0].ID != r.ID || got[0].Requester != "bob" {
		t.Fatalf("addressee's page: %v %+v", err, got)
	}
	for _, who := range []string{"bob", "eve"} {
		if got, _ := s.Page(wsA, who, 0); len(got) != 0 {
			t.Errorf("%s sees %d requests addressed to ada", who, len(got))
		}
	}
	if got, _ := s.Page(wsB, "ada", 0); len(got) != 0 {
		t.Errorf("another workspace's page holds %d requests", len(got))
	}
	if _, err := s.Get(wsB, r.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get in another workspace: %v", err)
	}
	if _, err := s.Events(wsB, r.ID); err != nil {
		t.Fatal(err)
	} else if ev, _ := s.Events(wsB, r.ID); len(ev) != 0 {
		t.Errorf("another workspace reads %d events", len(ev))
	}
	if rows, _ := s.List(wsB); len(rows) != 0 {
		t.Errorf("another workspace lists %d requests", len(rows))
	}
}

// Every action by the wrong account or in the wrong workspace is refused on a request that is otherwise in the right state,
// so the refusal is the permission check and not the state machine.
func TestOtherAccountsAndWorkspacesCannotAct(t *testing.T) {
	s, _ := openStore(t)
	r := toReview(t, s, 0)
	eve := Actor{ID: "eve", Via: "token", Instance: inst2}
	if _, err := s.Review(wsA, r.ID, "eve", eve); !errors.Is(err, ErrNotFound) {
		t.Errorf("review: %v", err)
	}
	if _, _, err := s.Claim(wsA, r.ID, "eve", eve, goodClaim(shaA, inst2)); !errors.Is(err, ErrNotFound) {
		t.Errorf("claim: %v", err)
	}
	if _, err := s.Decline(wsA, r.ID, "eve", eve, "no"); !errors.Is(err, ErrNotFound) {
		t.Errorf("decline: %v", err)
	}
	if _, _, err := s.Report(wsA, r.ID, "eve", eve, Report{Instance: inst2, Seq: 1, State: StateRunning}); !errors.Is(err, ErrNotFound) {
		t.Errorf("report: %v", err)
	}
	if _, err := s.Withdraw(wsA, r.ID, Actor{ID: "eve", Via: "session"}, false); !errors.Is(err, ErrNotYours) {
		t.Errorf("withdraw by another account: %v", err)
	}
	if _, err := s.AddNote(wsA, r.ID, Actor{ID: "eve", Via: "session"}, false, "hi"); !errors.Is(err, ErrNotYours) {
		t.Errorf("note by another account: %v", err)
	}
	// the right account in another workspace
	if _, err := s.Review(wsB, r.ID, "ada", adaToken); !errors.Is(err, ErrNotFound) {
		t.Errorf("review in another workspace: %v", err)
	}
	if _, _, err := s.Claim(wsB, r.ID, "ada", adaToken, goodClaim(shaA, inst1)); !errors.Is(err, ErrNotFound) {
		t.Errorf("claim in another workspace: %v", err)
	}
	if _, err := s.Withdraw(wsB, r.ID, bobSession, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("withdraw in another workspace: %v", err)
	}
	if _, err := s.Decline(wsB, r.ID, "ada", adaToken, "no"); !errors.Is(err, ErrNotFound) {
		t.Errorf("decline in another workspace: %v", err)
	}
	if got := state(t, s, r.ID); got != StateReview {
		t.Fatalf("state moved to %s", got)
	}
	// a Maintainer (any=true) may withdraw
	if _, err := s.Withdraw(wsA, r.ID, Actor{ID: "maint", Via: "session"}, true); err != nil {
		t.Fatal(err)
	}
}

func TestClaimNeedsAReviewedRequest(t *testing.T) {
	s, _ := openStore(t)
	r := fileReq(t, s, "bob", "ada", 0)
	_, _, err := s.Claim(wsA, r.ID, "ada", adaToken, goodClaim(shaA, inst1))
	wantConflict(t, err, StateRequested)
	if got := state(t, s, r.ID); got != StateRequested {
		t.Fatalf("state %s", got)
	}
}

func TestClaimIsIdempotentForTheSameHashAndInstance(t *testing.T) {
	s, _ := openStore(t)
	r := toReview(t, s, 0)
	// an invalid claim leaves the request waiting
	bad := goodClaim("nothex", inst1)
	if _, _, err := s.Claim(wsA, r.ID, "ada", adaToken, bad); !errors.Is(err, ErrInvalid) {
		t.Fatalf("bad hash: %v", err)
	}
	if got := state(t, s, r.ID); got != StateReview {
		t.Fatalf("state %s", got)
	}
	first, created, err := s.Claim(wsA, r.ID, "ada", adaToken, goodClaim(shaA, inst1))
	if err != nil || !created || first.State != StateApproved || first.Approval == nil || first.Approval.ScriptHash != shaA || first.Approval.Instance != inst1 {
		t.Fatalf("first claim: %v %v %+v", err, created, first)
	}
	again, created, err := s.Claim(wsA, r.ID, "ada", adaToken, goodClaim(shaA, inst1))
	if err != nil || created || again.Change != first.Change {
		t.Fatalf("same claim again: %v created=%v change %d vs %d", err, created, again.Change, first.Change)
	}
	_, _, err = s.Claim(wsA, r.ID, "ada", adaToken, goodClaim(shaB, inst1))
	wantConflict(t, err, StateApproved)
	_, _, err = s.Claim(wsA, r.ID, "ada", adaToken, goodClaim(shaA, inst2))
	wantConflict(t, err, StateApproved)
	// the recorded approval is still the first one
	got, _ := s.Get(wsA, r.ID)
	if got.Approval.ScriptHash != shaA || got.Approval.Instance != inst1 {
		t.Fatalf("approval overwritten: %+v", got.Approval)
	}
	// neither withdrawal nor decline can undo an approval
	_, err = s.Withdraw(wsA, r.ID, bobSession, false)
	wantConflict(t, err, StateApproved)
	_, err = s.Decline(wsA, r.ID, "ada", adaToken, "no")
	wantConflict(t, err, StateApproved)
}

func TestReviewIsIdempotentAndDoesNotBumpTheCursorAgain(t *testing.T) {
	s, _ := openStore(t)
	r := toReview(t, s, 0)
	again, err := s.Review(wsA, r.ID, "ada", adaToken)
	if err != nil || again.Change != r.Change {
		t.Fatalf("%v change %d vs %d", err, again.Change, r.Change)
	}
}

func TestWithdrawBeatsALaterClaim(t *testing.T) {
	s, _ := openStore(t)
	r := toReview(t, s, 0)
	if _, err := s.Withdraw(wsA, r.ID, bobSession, false); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.Claim(wsA, r.ID, "ada", adaToken, goodClaim(shaA, inst1))
	wantConflict(t, err, StateWithdrawn)
	_, _, err = s.Report(wsA, r.ID, "ada", adaToken, Report{Instance: inst1, Seq: 1, State: StateRunning})
	wantConflict(t, err, StateWithdrawn)
	if _, err := s.Review(wsA, r.ID, "ada", adaToken); err == nil {
		t.Fatal("a withdrawn request was reviewed")
	}
	if got := state(t, s, r.ID); got != StateWithdrawn {
		t.Fatalf("state %s", got)
	}
}

func TestDeclinedRequestCannotBeClaimed(t *testing.T) {
	s, _ := openStore(t)
	r := toReview(t, s, 0)
	if _, err := s.Decline(wsA, r.ID, "ada", adaToken, "not this week"); err != nil {
		t.Fatal(err)
	}
	_, _, err := s.Claim(wsA, r.ID, "ada", adaToken, goodClaim(shaA, inst1))
	wantConflict(t, err, StateDeclined)
	got, _ := s.Get(wsA, r.ID)
	if got.DeclineNote != "not this week" {
		t.Fatalf("note %q", got.DeclineNote)
	}
	if _, err := s.Decline(wsA, r.ID, "ada", adaToken, "\x00"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("control character in a decline note: %v", err)
	}
}

// The server clock decides, inside the statement that moves the request: at exactly expires_at the request is expired.
func TestExpiryBoundaryIsExclusive(t *testing.T) {
	s, clk := openStore(t)
	expires := t0 + 86400

	review := fileReq(t, s, "bob", "ada", 1)
	claim := toReview(t, s, 1)
	withdraw := toReview(t, s, 1)
	live := toReview(t, s, 1)

	clk.Set(expires - 1)
	if _, _, err := s.Claim(wsA, live.ID, "ada", adaToken, goodClaim(shaA, inst1)); err != nil {
		t.Fatalf("one second before expiry: %v", err)
	}

	clk.Set(expires)
	if _, err := s.Review(wsA, review.ID, "ada", adaToken); err == nil {
		t.Error("review accepted at expires_at")
	} else {
		wantConflict(t, err, StateExpired)
	}
	_, _, err := s.Claim(wsA, claim.ID, "ada", adaToken, goodClaim(shaA, inst1))
	wantConflict(t, err, StateExpired)
	_, err = s.Withdraw(wsA, withdraw.ID, bobSession, false)
	wantConflict(t, err, StateExpired)
	for _, id := range []int64{review.ID, claim.ID, withdraw.ID} {
		if got := state(t, s, id); got != StateExpired {
			t.Errorf("request %d is %s, want expired", id, got)
		}
		ev, _ := s.Events(wsA, id)
		last := ev[len(ev)-1]
		if last.Event != "expired" || last.Actor != "system" || last.Via != "clock" {
			t.Errorf("request %d last event %+v", id, last)
		}
	}
	// an approved request does not expire
	if got := state(t, s, live.ID); got != StateApproved {
		t.Errorf("approved request is %s", got)
	}
}

func TestExpiredRequestIsRefusedEvenIfNoSweepRan(t *testing.T) {
	s, clk := openStore(t)
	r := toReview(t, s, 1)
	clk.Set(t0 + 86400 + 3600)
	// no ExpireDue, no Page: the claim itself must see the clock
	_, _, err := s.Claim(wsA, r.ID, "ada", adaToken, goodClaim(shaA, inst1))
	wantConflict(t, err, StateExpired)
}

func TestReports(t *testing.T) {
	s, clk := openStore(t)
	r := toReview(t, s, 1)
	rep := func(seq int64, st, code string) (Row, bool, error) {
		return s.Report(wsA, r.ID, "ada", adaToken, Report{Instance: inst1, Seq: seq, State: st, Code: code})
	}
	if _, _, err := rep(1, StateRunning, ""); err == nil {
		t.Fatal("a report before approval was accepted")
	} else {
		wantConflict(t, err, StateReview)
	}
	if _, _, err := s.Claim(wsA, r.ID, "ada", adaToken, goodClaim(shaA, inst1)); err != nil {
		t.Fatal(err)
	}
	// approved requests keep reporting after the request's own expiry
	clk.Set(t0 + 10*86400)
	got, changed, err := rep(1, StateSubmitted, "")
	if err != nil || !changed || got.State != StateSubmitted || got.SubmittedAt == nil {
		t.Fatalf("submitted: %v %v %+v", err, changed, got)
	}
	got, changed, err = rep(2, StateRunning, "")
	if err != nil || !changed || got.State != StateRunning || got.RunningAt == nil {
		t.Fatalf("running: %v %v %+v", err, changed, got)
	}
	// a report from any other instance is refused, whatever it says
	_, _, err = s.Report(wsA, r.ID, "ada", adaToken, Report{Instance: inst2, Seq: 9, State: StateFinished})
	wantConflict(t, err, StateRunning)
	got, changed, err = rep(3, StateFinished, "")
	if err != nil || !changed || got.State != StateFinished || got.EndedAt == nil {
		t.Fatalf("finished: %v %v %+v", err, changed, got)
	}
	// a later "running" never moves it back, a different end never replaces the first
	for seq, st := range map[int64]string{4: StateRunning, 5: StateFailed, 6: StateSubmitted, 7: StateApproved} {
		got, changed, err = rep(seq, st, CodeTimeout)
		if err != nil || changed || got.State != StateFinished || got.Code != "" {
			t.Fatalf("late %s (seq %d): %v changed=%v state=%s code=%q", st, seq, err, changed, got.State, got.Code)
		}
	}
	// a replayed sequence number is ignored
	if _, changed, _ := rep(3, StateFailed, ""); changed {
		t.Fatal("a replayed sequence number changed the request")
	}
	if got := state(t, s, r.ID); got != StateFinished {
		t.Fatalf("state %s", got)
	}
	// an invalid report is refused before any state is read
	if _, _, err := s.Report(wsA, r.ID, "ada", adaToken, Report{Instance: inst1, Seq: 20, State: "running; id"}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("free-text state: %v", err)
	}
}

func TestEveryTransitionIsRecordedWithActorAndVia(t *testing.T) {
	s, _ := openStore(t)
	r := fileReq(t, s, "bob", "ada", 0)
	steps := []struct {
		do         func() error
		event      string
		actor, via string
		tok, inst  string
	}{
		{func() error { _, err := s.Review(wsA, r.ID, "ada", adaToken); return err }, "review", "ada", "token", "tok-ada", inst1},
		{func() error { _, err := s.AddNote(wsA, r.ID, bobSession, false, "please cancel"); return err }, "note", "bob", "session", "", ""},
		{func() error {
			_, _, err := s.Claim(wsA, r.ID, "ada", adaToken, goodClaim(shaA, inst1))
			return err
		}, "approved", "ada", "token", "tok-ada", inst1},
		{func() error {
			_, _, err := s.Report(wsA, r.ID, "ada", adaToken, Report{Instance: inst1, Seq: 1, State: StateRunning})
			return err
		}, "status", "ada", "token", "tok-ada", inst1},
	}
	ev, _ := s.Events(wsA, r.ID)
	if len(ev) != 1 || ev[0].Event != "filed" || ev[0].Actor != "bob" || ev[0].Via != "session" {
		t.Fatalf("filed event: %+v", ev)
	}
	for i, st := range steps {
		before := len(mustEvents(t, s, r.ID))
		if err := st.do(); err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		ev := mustEvents(t, s, r.ID)
		if len(ev) != before+1 {
			t.Fatalf("step %d (%s): %d events, want %d", i, st.event, len(ev), before+1)
		}
		e := ev[len(ev)-1]
		if e.Event != st.event || e.Actor != st.actor || e.Via != st.via || e.TokenID != st.tok || e.Instance != st.inst {
			t.Errorf("step %d: event %+v", i, e)
		}
	}
	// withdraw and decline on two others
	w := fileReq(t, s, "bob", "ada", 0)
	if _, err := s.Withdraw(wsA, w.ID, bobSession, false); err != nil {
		t.Fatal(err)
	}
	if e := lastEvent(t, s, w.ID); e.Event != "withdrawn" || e.Actor != "bob" || e.Via != "session" {
		t.Errorf("withdraw event %+v", e)
	}
	d := fileReq(t, s, "bob", "ada", 0)
	if _, err := s.Decline(wsA, d.ID, "ada", adaToken, "no"); err != nil {
		t.Fatal(err)
	}
	if e := lastEvent(t, s, d.ID); e.Event != "declined" || e.Actor != "ada" || e.Via != "token" || e.Detail != "no" {
		t.Errorf("decline event %+v", e)
	}
	// a refused transition writes nothing
	n := len(mustEvents(t, s, d.ID))
	_, _, _ = s.Claim(wsA, d.ID, "ada", adaToken, goodClaim(shaA, inst1))
	if len(mustEvents(t, s, d.ID)) != n {
		t.Error("a refused claim wrote an event")
	}
}

func mustEvents(t *testing.T, s *Store, id int64) []Event {
	t.Helper()
	ev, err := s.Events(wsA, id)
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

func lastEvent(t *testing.T, s *Store, id int64) Event {
	t.Helper()
	ev := mustEvents(t, s, id)
	return ev[len(ev)-1]
}

// The key holder's computer pulls with a cursor; a transition it missed must come back on the next pull.
func TestEveryTransitionBumpsTheCursor(t *testing.T) {
	s, clk := openStore(t)
	var cursor int64
	pull := func(what string, wantID int64) Row {
		t.Helper()
		rows, err := s.Page(wsA, "ada", cursor)
		if err != nil {
			t.Fatal(err)
		}
		var hit *Row
		for i := range rows {
			if rows[i].Change <= cursor {
				t.Fatalf("%s: row %d with change %d not after the cursor %d", what, rows[i].ID, rows[i].Change, cursor)
			}
			if rows[i].ID == wantID {
				hit = &rows[i]
			}
		}
		if hit == nil {
			t.Fatalf("%s: request %d missing from a pull after cursor %d", what, wantID, cursor)
		}
		cursor = rows[len(rows)-1].Change
		again, _ := s.Page(wsA, "ada", cursor)
		if len(again) != 0 {
			t.Fatalf("%s: %d rows again after the new cursor", what, len(again))
		}
		return *hit
	}
	r := fileReq(t, s, "bob", "ada", 1)
	if got := pull("file", r.ID); got.State != StateRequested {
		t.Fatalf("state %s", got.State)
	}
	if _, err := s.Review(wsA, r.ID, "ada", adaToken); err != nil {
		t.Fatal(err)
	}
	if got := pull("review", r.ID); got.State != StateReview {
		t.Fatalf("state %s", got.State)
	}
	if _, err := s.AddNote(wsA, r.ID, bobSession, false, "hello"); err != nil {
		t.Fatal(err)
	}
	pull("note", r.ID)
	if _, _, err := s.Claim(wsA, r.ID, "ada", adaToken, goodClaim(shaA, inst1)); err != nil {
		t.Fatal(err)
	}
	if got := pull("claim", r.ID); got.State != StateApproved {
		t.Fatalf("state %s", got.State)
	}
	w := fileReq(t, s, "bob", "ada", 1)
	pull("file 2", w.ID)
	if _, err := s.Withdraw(wsA, w.ID, bobSession, false); err != nil {
		t.Fatal(err)
	}
	if got := pull("withdraw", w.ID); got.State != StateWithdrawn {
		t.Fatalf("state %s", got.State)
	}
	d := fileReq(t, s, "bob", "ada", 1)
	pull("file 3", d.ID)
	if _, err := s.Decline(wsA, d.ID, "ada", adaToken, "no"); err != nil {
		t.Fatal(err)
	}
	if got := pull("decline", d.ID); got.State != StateDeclined {
		t.Fatalf("state %s", got.State)
	}
	e := fileReq(t, s, "bob", "ada", 1)
	pull("file 4", e.ID)
	clk.Set(t0 + 2*86400)
	if got := pull("expiry", e.ID); got.State != StateExpired {
		t.Fatalf("state %s", got.State)
	}
	// every change value of the workspace is distinct
	rows, _ := s.Page(wsA, "ada", 0)
	seen := map[int64]bool{}
	for _, r := range rows {
		if seen[r.Change] {
			t.Fatalf("change %d used twice", r.Change)
		}
		seen[r.Change] = true
	}
}

func TestCloseAccount(t *testing.T) {
	s, _ := openStore(t)
	if err := s.PutInbox(wsA, "bob", Actor{ID: "bob", Via: "token"}, vgtInbox()); err != nil {
		t.Fatal(err)
	}
	toAda1 := fileReq(t, s, "bob", "ada", 0)
	toAda2 := toReview(t, s, 0)
	fromAda, err := s.File(wsA, "ada", "bob", vgtAsk(nil), nil, 0, Actor{ID: "ada", Via: "session"})
	if err != nil {
		t.Fatal(err)
	}
	approved := toReview(t, s, 0)
	if _, _, err := s.Claim(wsA, approved.ID, "ada", adaToken, goodClaim(shaA, inst1)); err != nil {
		t.Fatal(err)
	}
	unrelated := fileReq(t, s, "cy", "bob", 0)
	cursor := int64(0)
	if rows, _ := s.Page(wsA, "bob", 0); len(rows) > 0 {
		cursor = rows[len(rows)-1].Change
	}

	if err := s.CloseAccount(wsA, "ada", "ada left the workspace"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{toAda1.ID, toAda2.ID} {
		r, _ := s.Get(wsA, id)
		if r.State != StateDeclined || r.DeclineNote != "ada left the workspace" {
			t.Errorf("request to ada %d: %s %q", id, r.State, r.DeclineNote)
		}
		if e := lastEvent(t, s, id); e.Event != "declined" || e.Actor != "system" || e.Via != "membership" {
			t.Errorf("event %+v", e)
		}
	}
	if r, _ := s.Get(wsA, fromAda.ID); r.State != StateWithdrawn {
		t.Errorf("request from ada is %s", r.State)
	} else if e := lastEvent(t, s, fromAda.ID); e.Event != "withdrawn" || e.Actor != "system" || e.Via != "membership" {
		t.Errorf("event %+v", e)
	}
	if r, _ := s.Get(wsA, approved.ID); r.State != StateApproved || r.ClosedNote != "no further reports expected" {
		t.Errorf("approved request: %s %q", r.State, r.ClosedNote)
	}
	if r, _ := s.Get(wsA, unrelated.ID); r.State != StateRequested {
		t.Errorf("unrelated request is %s", r.State)
	}
	if _, err := s.InboxOf(wsA, "ada"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ada's inbox still published: %v", err)
	}
	// bob's computer sees that the request from ada was withdrawn
	rows, _ := s.Page(wsA, "bob", cursor)
	found := false
	for _, r := range rows {
		found = found || (r.ID == fromAda.ID && r.State == StateWithdrawn)
	}
	if !found {
		t.Errorf("the withdrawn request did not reach bob's next pull: %+v", rows)
	}
	// nothing can be filed to ada any more
	if _, err := s.File(wsA, "bob", "ada", vgtAsk(nil), nil, 0, bobSession); !errors.Is(err, ErrNoInbox) {
		t.Errorf("filing after the account closed: %v", err)
	}
	// the other workspace is untouched by a close in this one
	if err := s.PutInbox(wsB, "ada", adaToken, vgtInbox()); err != nil {
		t.Fatal(err)
	}
	if err := s.CloseAccount(wsA, "ada", "again"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InboxOf(wsB, "ada"); err != nil {
		t.Errorf("another workspace's inbox was closed: %v", err)
	}
}

// ---- races: one connection, one winner, every loser a ConflictError ----

const raceRounds = 20

func raceStore(t *testing.T) (*Store, *testClock) { return openStore(t) }

// A report is a transition like any other: the pull from the cursor before it carries it.
func TestReportBumpsTheCursor(t *testing.T) {
	s, _ := openStore(t)
	r := toReview(t, s, 0)
	if _, _, err := s.Claim(wsA, r.ID, "ada", adaToken, goodClaim(shaA, inst1)); err != nil {
		t.Fatal(err)
	}
	rows, _ := s.Page(wsA, "ada", 0)
	cursor := rows[len(rows)-1].Change
	if _, _, err := s.Report(wsA, r.ID, "ada", adaToken, Report{Instance: inst1, Seq: 1, State: StateRunning}); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.Page(wsA, "ada", cursor)
	if len(rows) != 1 || rows[0].State != StateRunning {
		t.Fatalf("pull after the report: %+v", rows)
	}
}

func startTogether(n int, f func(i int)) {
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			f(i)
		}(i)
	}
	close(start)
	wg.Wait()
}

func TestRaceSameClaimHasOneWinner(t *testing.T) {
	const n = 8
	for round := 0; round < raceRounds; round++ {
		s, _ := raceStore(t)
		r := toReview(t, s, 0)
		var created atomic.Int32
		errs := make([]error, n)
		startTogether(n, func(i int) {
			_, c, err := s.Claim(wsA, r.ID, "ada", adaToken, goodClaim(shaA, inst1))
			errs[i] = err
			if c {
				created.Add(1)
			}
		})
		for i, err := range errs {
			if err != nil {
				t.Fatalf("round %d claimer %d: %v (an identical claim is answered, never an error)", round, i, err)
			}
		}
		if created.Load() != 1 {
			t.Fatalf("round %d: %d claims created, want 1", round, created.Load())
		}
		if ev := mustEvents(t, s, r.ID); countEvents(ev, "approved") != 1 {
			t.Fatalf("round %d: %d approved events", round, countEvents(ev, "approved"))
		}
	}
}

func countEvents(ev []Event, name string) int {
	n := 0
	for _, e := range ev {
		if e.Event == name {
			n++
		}
	}
	return n
}

func TestRaceDifferentClaimsHaveOneWinner(t *testing.T) {
	const n = 8
	for round := 0; round < raceRounds; round++ {
		s, _ := raceStore(t)
		r := toReview(t, s, 0)
		errs := make([]error, n)
		created := make([]bool, n)
		startTogether(n, func(i int) {
			hash := strconv.FormatInt(int64(i)+1, 16) + shaA[1:] // eight different valid hashes
			_, created[i], errs[i] = s.Claim(wsA, r.ID, "ada", adaToken, goodClaim(hash, inst1))
		})
		winners := 0
		for i := range errs {
			switch {
			case errs[i] == nil && created[i]:
				winners++
			default:
				var ce ConflictError
				if !errors.As(errs[i], &ce) || ce.State != StateApproved {
					t.Fatalf("round %d loser %d: %v", round, i, errs[i])
				}
			}
		}
		if winners != 1 {
			t.Fatalf("round %d: %d winners", round, winners)
		}
	}
}

func TestRaceWithdrawAgainstClaim(t *testing.T) {
	for round := 0; round < raceRounds; round++ {
		s, _ := raceStore(t)
		r := toReview(t, s, 0)
		var claimErr, withdrawErr error
		var created bool
		startTogether(2, func(i int) {
			if i == 0 {
				_, created, claimErr = s.Claim(wsA, r.ID, "ada", adaToken, goodClaim(shaA, inst1))
			} else {
				_, withdrawErr = s.Withdraw(wsA, r.ID, bobSession, false)
			}
		})
		final := state(t, s, r.ID)
		switch final {
		case StateApproved:
			if claimErr != nil || !created {
				t.Fatalf("round %d: approved but the claim says %v created=%v", round, claimErr, created)
			}
			wantConflict(t, withdrawErr, StateApproved)
		case StateWithdrawn:
			if withdrawErr != nil {
				t.Fatalf("round %d: withdrawn but the withdraw says %v", round, withdrawErr)
			}
			wantConflict(t, claimErr, StateWithdrawn)
		default:
			t.Fatalf("round %d: final state %s", round, final)
		}
	}
}

// The clock moves past expires_at while claimers and sweeps run: either the claim is recorded before it (and no sweep expires an
// approved request), or the request expires and every claimer is refused.
func TestRaceExpiryAgainstClaim(t *testing.T) {
	const claimers = 4
	for round := 0; round < raceRounds; round++ {
		s, clk := raceStore(t)
		r := toReview(t, s, 1)
		expires := t0 + 86400
		clk.Set(expires - 1)
		errs := make([]error, claimers)
		created := make([]bool, claimers)
		var sweepErrs [2]error
		startTogether(claimers+2, func(i int) {
			switch {
			case i < claimers:
				_, created[i], errs[i] = s.Claim(wsA, r.ID, "ada", adaToken, goodClaim(shaA, inst1))
			case i == claimers:
				clk.Set(expires)
				sweepErrs[0] = s.ExpireDue(wsA)
			default:
				_, sweepErrs[1] = s.Page(wsA, "ada", 0)
			}
		})
		for i, err := range sweepErrs {
			if err != nil {
				t.Fatalf("round %d sweep %d: %v", round, i, err)
			}
		}
		wins := 0
		for i := range errs {
			if errs[i] == nil {
				if created[i] {
					wins++
				}
				continue
			}
			wantConflict(t, errs[i], StateExpired)
		}
		final := state(t, s, r.ID)
		switch final {
		case StateApproved:
			if wins != 1 {
				t.Fatalf("round %d: approved with %d winners", round, wins)
			}
		case StateExpired:
			if wins != 0 {
				t.Fatalf("round %d: expired with %d winners", round, wins)
			}
		default:
			t.Fatalf("round %d: final state %s", round, final)
		}
		if ev := mustEvents(t, s, r.ID); countEvents(ev, "approved")+countEvents(ev, "expired") != 1 {
			t.Fatalf("round %d: events %+v", round, ev)
		}
	}
}
