package boards

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
)

// Two publishes of one board at the same moment must never take the same round number, and the round that loses the race must be
// rebuilt against the snapshot that won, not only renumbered (ADR 0073). The store is on a file with the production catalog's
// connection settings. Two Store values share the database, so their in-process mutexes do not help: the test then relies on the
// insert's own condition, which is what protects two processes.
func TestConcurrentPublishesOfOneBoardNeverShareARound(t *testing.T) {
	for _, stores := range []int{1, 2} {
		t.Run(fmt.Sprintf("%d stores on the file", stores), func(t *testing.T) {
			dir := t.TempDir()
			cat, err := catalog.Open("file:" + filepath.Join(dir, "t.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer cat.Close()
			ss := make([]*Store, stores)
			for i := range ss {
				if ss[i], err = Open(cat.DB(), dir); err != nil {
					t.Fatal(err)
				}
			}
			s := ss[0]
			b, _ := s.CreateBoard(ws1, "local-owner", "Phase separation")
			a, c := fig(t, s, "A", 1), fig(t, s, "C", 3)
			setItems(t, s, b, a, c)
			publish(t, s, b, "first") // round 1, so every later round carries a comparison

			const n = 12
			var gate, done sync.WaitGroup
			gate.Add(1)
			type result struct {
				label string
				sn    Snapshot
				err   error
			}
			results := make([]result, n)
			for i := 0; i < n; i++ {
				done.Add(1)
				go func() {
					defer done.Done()
					gate.Wait()
					label := fmt.Sprintf("p%02d", i)
					sn, _, err := ss[i%stores].Publish(ws1, "local-owner", b.ID, label, time.Hour)
					results[i] = result{label, sn, err}
				}()
			}
			gate.Done()
			done.Wait()

			won := map[string]bool{}
			for _, r := range results {
				switch {
				case r.err == nil:
					won[r.label] = true
				case errors.Is(r.err, ErrConflict):
					// a clear refusal is allowed; a database error is not
				default:
					t.Fatalf("publish %s failed with %v", r.label, r.err)
				}
			}
			if len(won) == 0 {
				t.Fatal("no publish succeeded")
			}

			// the stored rounds are 1..k+1 with no number twice, and each one's comparison is against the round just below it
			rows, err := cat.DB().Query(`SELECT id, frozen FROM snapshots WHERE board_id=? AND workspace_id=? ORDER BY id`, b.ID, ws1)
			if err != nil {
				t.Fatal(err)
			}
			type stored struct {
				id int64
				fr Frozen
			}
			var all []stored
			for rows.Next() {
				var x stored
				var raw string
				if err := rows.Scan(&x.id, &raw); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal([]byte(raw), &x.fr); err != nil {
					t.Fatal(err)
				}
				all = append(all, x)
			}
			rows.Close()
			if len(all) != len(won)+1 {
				t.Fatalf("%d snapshots stored for %d successful publishes", len(all), len(won))
			}
			seen := map[int]bool{}
			for i, x := range all {
				if x.fr.Round == nil || seen[x.fr.Round.Number] || x.fr.Round.Number != i+1 {
					t.Fatalf("snapshot %d (position %d) holds round %+v; rounds so far %v", x.id, i+1, x.fr.Round, seen)
				}
				seen[x.fr.Round.Number] = true
				if i == 0 {
					continue
				}
				if x.fr.Round.Since == nil || x.fr.Round.Since.Label != all[i-1].fr.Label || !x.fr.Round.Since.At.Equal(all[i-1].fr.CreatedAt) {
					t.Fatalf("round %d compares with %+v, not with round %d (%q)", i+1, x.fr.Round.Since, i, all[i-1].fr.Label)
				}
			}

			// the owner's list numbers by position, and must agree with the stored numbers
			list, err := s.Snapshots(ws1, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			var got []int
			for _, sn := range list {
				got = append(got, sn.Round)
			}
			sort.Ints(got)
			for i, r := range got {
				if r != i+1 {
					t.Fatalf("the list shows rounds %v", got)
				}
			}
			for i, sn := range list {
				if want := all[len(all)-1-i].fr.Round.Number; sn.Round != want {
					t.Fatalf("list entry %d shows round %d, the snapshot holds %d", i, sn.Round, want)
				}
			}
		})
	}
}
