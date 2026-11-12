package accounts

import (
	"sync"
	"time"
)

// Limiter slows down password and second-factor guessing (ADR 0050 section 2: rate limited per account and per client
// address). It is a backoff, not a hard lock: on a server whose only account may be its admin, a lock anyone can trigger by
// typing the admin's name would be a way to shut the admin out.
//
//   - Per name: failures are counted against the name typed, whether or not an account has it, so the answer for an unknown
//     name and a known one is the same. From the fifth failure within the window, each further attempt waits 30 s, doubling
//     per failure, at most the window.
//   - Per client address: at most ClientMax failures within the window, whatever names they were for; checked before any
//     password is hashed. An empty address is not counted: the server passes none when it cannot tell clients apart (a
//     proxy on a private address with no client-address header), so a proxy can never turn 30 failures into a lockout of
//     everyone. The per-name backoff applies in every case.
//
// The counts live in memory: a restart forgets them, which costs an attacker one restart's worth of guesses, not more.
type Limiter struct {
	Window    time.Duration // default 15 minutes
	NameFree  int           // failures per name before the backoff starts; default 5
	ClientMax int           // failures per client within the window; default 30
	Now       func() time.Time

	mu     sync.Mutex
	names  map[string][]time.Time
	client map[string][]time.Time
}

const limiterKeys = 10000 // bound on remembered names and addresses each

func (l *Limiter) defaults() (time.Duration, int, int, time.Time) {
	w, nf, cm := l.Window, l.NameFree, l.ClientMax
	if w <= 0 {
		w = 15 * time.Minute
	}
	if nf <= 0 {
		nf = 5
	}
	if cm <= 0 {
		cm = 30
	}
	now := time.Now()
	if l.Now != nil {
		now = l.Now()
	}
	return w, nf, cm, now
}

func recent(ts []time.Time, since time.Time) []time.Time {
	i := 0
	for i < len(ts) && !ts[i].After(since) {
		i++
	}
	return ts[i:]
}

// Allow reports whether an attempt for this name from this client may be checked now, and if not, how long to wait.
func (l *Limiter) Allow(name, client string) (bool, time.Duration) {
	w, nf, cm, now := l.defaults()
	l.mu.Lock()
	defer l.mu.Unlock()
	if c := recent(l.client[client], now.Add(-w)); client != "" && len(c) >= cm {
		return false, c[0].Add(w).Sub(now)
	}
	n := recent(l.names[name], now.Add(-w))
	if len(n) < nf {
		return true, 0
	}
	wait := 30 * time.Second << min(len(n)-nf, 10)
	if wait > w {
		wait = w
	}
	if until := n[len(n)-1].Add(wait); now.Before(until) {
		return false, until.Sub(now)
	}
	return true, 0
}

// Fail records a failed attempt.
func (l *Limiter) Fail(name, client string) {
	w, _, _, now := l.defaults()
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.names == nil {
		l.names, l.client = map[string][]time.Time{}, map[string][]time.Time{}
	}
	add(l.names, name, now, w)
	if client != "" {
		add(l.client, client, now, w)
	}
}

// Succeed forgets the name's failures (the client's stay: one success does not excuse a spray of guesses).
func (l *Limiter) Succeed(name string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.names, name)
}

func add(m map[string][]time.Time, k string, now time.Time, w time.Duration) {
	if _, ok := m[k]; !ok && len(m) >= limiterKeys {
		for key, ts := range m {
			if len(recent(ts, now.Add(-w))) == 0 {
				delete(m, key)
			}
		}
		for key := range m { // still full of live entries: drop an arbitrary one rather than grow without bound
			if len(m) < limiterKeys {
				break
			}
			delete(m, key)
		}
	}
	ts := append(recent(m[k], now.Add(-w)), now)
	if len(ts) > 64 {
		ts = ts[len(ts)-64:]
	}
	m[k] = ts
}
