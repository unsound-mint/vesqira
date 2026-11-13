package relay

import (
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// ADR 0074: the terms of a standing approval, what of an ask it covers, its inputs, and the race-safe budget.

func testGrant() Grant {
	return Grant{RequesterID: "acct-0123456789abcdef", RequesterName: "Bea", Label: testLabel, HostID: 1, Template: TemplateVGT,
		Commit: strings.Repeat("a", 40), CommitBasis: "typed", Keys: []Bound{{Key: "dn", Min: f64(2), Max: f64(6)}},
		Roots: []string{"/data/cubes"}, Files: []string{}, MaxJobs: 5, MaxCoreHours: 10,
		PerJob: Limits{MaxCPUs: 8, MaxMemGiB: 16, MaxTimeMinutes: 120}, MaxAtOnce: 2, ExpiresAt: time.Unix(t0, 0).Add(30 * 24 * time.Hour)}
}

func TestGrantKeysAreNarrowedInsideTheInboxNeverWider(t *testing.T) {
	now := time.Unix(t0, 0)
	ok := func(keys []Bound) error { g := testGrant(); g.Keys = keys; return CheckGrantTerms(vgtInbox(), g, now) }
	for _, keys := range [][]Bound{
		{{Key: "dn", Min: f64(2), Max: f64(6)}},
		{{Key: "dn", Min: f64(1), Max: f64(8)}}, // the inbox's own bound is the widest allowed
		{{Key: "noise_mode", Choices: []string{"scalar"}}},
		{},
	} {
		if err := ok(keys); err != nil {
			t.Errorf("%+v refused: %v", keys, err)
		}
	}
	for _, keys := range [][]Bound{
		{{Key: "dn", Min: f64(0), Max: f64(6)}}, // below the inbox
		{{Key: "dn", Min: f64(2), Max: f64(9)}}, // above the inbox
		{{Key: "dn", Min: f64(2)}},              // one end
		{{Key: "dn"}},                           // not narrowed
		{{Key: "noise_mode", Choices: []string{"scalar", "x"}}},
		{{Key: "noise", Min: f64(0), Max: f64(1)}}, // not listed by the inbox
		{{Key: "input_file", Choices: []string{"/etc/passwd"}}},
		{{Key: "dn", Min: f64(2), Max: f64(6)}, {Key: "dn", Min: f64(2), Max: f64(4)}},
		{{Key: "dn", Min: f64(2), Max: f64(6), Choices: []string{"3"}}},
	} {
		if err := ok(keys); err == nil {
			t.Errorf("%+v accepted", keys)
		}
	}
	// budget, expiry and commit bounds
	for _, mut := range []func(*Grant){
		func(g *Grant) { g.MaxJobs = 0 },
		func(g *Grant) { g.MaxAtOnce = g.MaxJobs + 1 },
		func(g *Grant) { g.PerJob.MaxCPUs = 17 },
		func(g *Grant) { g.ExpiresAt = now.Add(-time.Second) },
		func(g *Grant) { g.ExpiresAt = now.Add(200 * 24 * time.Hour) },
		func(g *Grant) { g.Commit = "abc" },
		func(g *Grant) { g.RequesterID = "../x" },
		func(g *Grant) { g.Label = "Other" },
	} {
		g := testGrant()
		mut(&g)
		if err := CheckGrantTerms(vgtInbox(), g, now); err == nil {
			t.Errorf("accepted %+v", g)
		}
	}
}

func TestGrantAskCoversOnlyItsKeysValuesAndCaps(t *testing.T) {
	o := testGrant()
	o.ID = strings.Repeat("c", 32)
	offer := o.Offer()
	ask := func(params map[string]string, mut func(*Ask)) Ask {
		a := vgtAsk(params)
		a.Grant = o.ID
		if mut != nil {
			mut(&a)
		}
		return a
	}
	if code, err := CheckGrantAsk(offer, ask(map[string]string{"dn": "4"}, nil)); err != nil {
		t.Fatalf("inside: %s %v", code, err)
	}
	for _, c := range []struct {
		a    Ask
		code string
	}{
		{ask(map[string]string{"dn": "7"}, nil), GrantOutside},
		{ask(map[string]string{"dn": "1"}, nil), GrantOutside},
		{ask(map[string]string{"ker": "4"}, nil), GrantOutside},
		{ask(map[string]string{"dn": "4"}, func(a *Ask) { a.Resources.CPUs = 9 }), GrantOutside},
		{ask(map[string]string{"dn": "4"}, func(a *Ask) { a.Resources.TimeMinutes = 121 }), GrantOutside},
		{ask(map[string]string{}, func(a *Ask) { a.Grid = []GridKey{{Key: "dn", Values: []string{"2", "4"}}} }), GrantGrid},
		{ask(map[string]string{"dn": "4"}, func(a *Ask) { a.Label = "Other" }), GrantUnknown},
		{ask(map[string]string{"dn": "4"}, func(a *Ask) { a.Template = TemplateROHSA }), GrantUnknown},
	} {
		if code, err := CheckGrantAsk(offer, c.a); err == nil || code != c.code {
			t.Errorf("%+v: %s %v", c.a, code, err)
		}
	}
}

func TestGrantInputsRefuseTraversalAndPathsOutsideTheRoots(t *testing.T) {
	inRoots := func(p string) bool { return strings.HasPrefix(p, "/data/") }
	if err := CheckGrantInputs([]string{"/data/cubes"}, []string{"/data/other/a.fits"}, inRoots); err != nil {
		t.Fatal(err)
	}
	for _, roots := range [][]string{{}, {"/data/cubes/../../etc"}, {"/data/cubes/"}, {"data/cubes"}, {"/"}, {"/etc"}, {"/data/a b"}} {
		if err := CheckGrantInputs(roots, nil, inRoots); err == nil {
			t.Errorf("%v accepted", roots)
		}
	}
	g := testGrant()
	g.Files = []string{"/data/x/one.fits"}
	for p, want := range map[string]bool{
		"/data/cubes/a.fits": true, "/data/cubes/sub/a.fits": true, "/data/x/one.fits": true,
		"/data/cubes": false, "/data/cubes/../secret.fits": false, "/data/cubesX/a.fits": false, "cubes/a.fits": false,
		"/data/x/two.fits": false, "": false, "/data/cubes//a.fits": false,
	} {
		if got := g.InputAllowed(p); got != want {
			t.Errorf("InputAllowed(%q) = %v", p, got)
		}
	}
}

// openLocalPool opens the catalog as the server does (WAL, a busy timeout) but with several connections, so concurrent
// reservations really race in SQLite instead of queueing on one connection.
func openLocalPool(t *testing.T, conns int) *Local {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "c.db")+"?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(conns)
	l, err := OpenLocal(db)
	if err != nil {
		t.Fatal(err)
	}
	l.Now = func() time.Time { return time.Unix(t0, 0) }
	return l
}

// Many reservations at once never take more than the budget: jobs, core-hours and jobs at once each hold exactly.
func TestGrantBudgetHoldsUnderConcurrentReservations(t *testing.T) {
	for _, conns := range []int{1, 8} {
		for _, c := range []struct {
			name                    string
			jobs, coreHours, atOnce int
			cpus, minutes, want     int
		}{
			{"jobs", 7, 1000, 16, 1, 10, 7},
			{"core-hours", 100, 2, 16, 4, 10, 3}, // 120 core-minutes, 40 each
			{"at once", 100, 1000, 3, 1, 10, 3},
		} {
			l := openLocalPool(t, conns)
			g := testGrant()
			g.MaxJobs, g.MaxCoreHours, g.MaxAtOnce = c.jobs, c.coreHours, c.atOnce
			g, err := l.CreateGrant(origin1, g)
			if err != nil {
				t.Fatal(err)
			}
			var wg sync.WaitGroup
			var mu sync.Mutex
			took := 0
			for i := 0; i < 40; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					ok, err := l.Reserve(g.ID, int64(1000+i), int64(i), c.cpus, c.minutes, "h")
					if err != nil {
						t.Error(err)
					}
					if ok {
						mu.Lock()
						took++
						mu.Unlock()
					}
				}(i)
			}
			wg.Wait()
			if took != c.want {
				t.Errorf("%s with %d connections: %d reservations taken, want %d", c.name, conns, took, c.want)
			}
			g, _ = l.GrantByID(g.ID)
			if g.JobsUsed != c.want {
				t.Errorf("%s: %d uses recorded", c.name, g.JobsUsed)
			}
		}
	}
}

func TestReservationIsOncePerRequestAndStopsAtRevocationAndExpiry(t *testing.T) {
	l := openLocalPool(t, 1)
	g, err := l.CreateGrant(origin1, testGrant())
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := l.Reserve(g.ID, 1, 1, 1, 10, "h"); !ok {
		t.Fatal("first reservation refused")
	}
	if ok, _ := l.Reserve(g.ID, 1, 1, 1, 10, "h"); ok {
		t.Fatal("the same request reserved twice")
	}
	// a released slot (claim refused) comes back; an ended job frees its jobs-at-once slot but stays counted
	_ = l.Release(1)
	if g, _ = l.GrantByID(g.ID); g.JobsUsed != 0 {
		t.Fatalf("release: %+v", g)
	}
	l.Reserve(g.ID, 2, 2, 1, 10, "h")
	us, _ := l.OpenUses()
	_ = l.EndUse(us[0].ID)
	if g, _ = l.GrantByID(g.ID); g.JobsUsed != 1 || g.AtOnce != 0 || g.CoreMinutesUsed != 10 {
		t.Fatalf("after the job ended: %+v", g)
	}
	if ok, _ := l.Reserve(strings.Repeat("0", 32), 3, 3, 1, 10, "h"); ok {
		t.Fatal("a grant that does not exist took a reservation")
	}
	l.Now = func() time.Time { return g.ExpiresAt }
	if ok, _ := l.Reserve(g.ID, 4, 4, 1, 10, "h"); ok {
		t.Fatal("an expired grant took a reservation")
	}
	l.Now = func() time.Time { return time.Unix(t0, 0) }
	if done, err := l.RevokeGrant(g.ID); !done || err != nil {
		t.Fatalf("revoke: %v %v", done, err)
	}
	if ok, _ := l.Reserve(g.ID, 5, 5, 1, 10, "h"); ok {
		t.Fatal("a revoked grant took a reservation")
	}
}
