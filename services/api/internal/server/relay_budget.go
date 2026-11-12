package server

import (
	"sync"
	"time"
)

// auditBudget caps how many audit rows of one kind a single source writes per hour: refused run-request filings on the group
// server (per account) and refused pulled requests on the key holder's computer (per server). The refusal itself is still
// answered or recorded; only the audit row is skipped past the cap, so a misbehaving client cannot grow the audit log without
// bound. The whole map is dropped when the hour turns, so it never holds more than an hour of sources.
type auditBudget struct {
	mu     sync.Mutex
	window time.Time
	used   map[string]int
}

const auditsPerHour = 20

// take reports whether source may write one more audit row now, and counts it.
func (b *auditBudget) take(source string, now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if hour := now.Truncate(time.Hour); !hour.Equal(b.window) || b.used == nil {
		b.window, b.used = hour, map[string]int{}
	}
	if b.used[source] >= auditsPerHour {
		return false
	}
	b.used[source]++
	return true
}
