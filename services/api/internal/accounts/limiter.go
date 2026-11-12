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
//   - One attempt at a time: Begin reserves the attempt under the lock before anything is checked, and a name or an address
//     with an attempt still in flight is refused until it ends. A parallel burst is therefore limited exactly like a
//     sequence; without the reservation every request of a burst would pass the check before the first failure landed.
//
// The counts live in memory: a restart forgets them, which costs an attacker one restart's worth of guesses, not more. The
// tables are bounded.
type Limiter struct {
	Window    time.Duration // default 15 minutes
	NameFree  int           // failures per name before the backoff starts; default 5
	ClientMax int           // failures per client within the window; default 30
	Now       func() time.Time
	// MaxKeys bounds each table (names, addresses); default 10000.
	MaxKeys int

	mu     sync.Mutex
	names  map[string]*limitEntry
	client map[string]*limitEntry
}

type limitEntry struct {
	fails    []time.Time
	inflight bool
}

// Attempt is a reserved try. Exactly one of Fail, Succeed or Done must be called when its outcome is known.
type Attempt struct {
	l            *Limiter
	name, client string
	once         sync.Once
}

// inflightWait is the Retry-After for a request refused because another attempt for the same name or address is running.
const inflightWait = time.Second

func (l *Limiter) defaults() (time.Duration, int, int, int, time.Time) {
	w, nf, cm, mk := l.Window, l.NameFree, l.ClientMax, l.MaxKeys
	if w <= 0 {
		w = 15 * time.Minute
	}
	if nf <= 0 {
		nf = 5
	}
	if cm <= 0 {
		cm = 30
	}
	if mk <= 0 {
		mk = 10000
	}
	now := time.Now()
	if l.Now != nil {
		now = l.Now()
	}
	return w, nf, cm, mk, now
}

func recent(ts []time.Time, since time.Time) []time.Time {
	i := 0
	for i < len(ts) && !ts[i].After(since) {
		i++
	}
	return ts[i:]
}

// room makes space for one more key.
func room(m map[string]*limitEntry, since time.Time, max int) bool {
	if len(m) < max {
		return true
	}
	for k, e := range m {
		if !e.inflight && len(recent(e.fails, since)) == 0 {
			delete(m, k)
		}
	}
	for k := range m { // still full of live entries: drop an arbitrary one rather than grow without bound
		if len(m) < max {
			break
		}
		delete(m, k)
	}
	return true
}

// Begin reserves an attempt for this name from this client (client "" is not counted per address), or says how long to wait.
func (l *Limiter) Begin(name, client string) (*Attempt, bool, time.Duration) {
	w, nf, cm, mk, now := l.defaults()
	since := now.Add(-w)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.names == nil {
		l.names, l.client = map[string]*limitEntry{}, map[string]*limitEntry{}
	}
	ce := l.client[client]
	if client != "" && ce != nil {
		ce.fails = recent(ce.fails, since)
		if ce.inflight {
			return nil, false, inflightWait
		}
		if len(ce.fails) >= cm {
			return nil, false, ce.fails[0].Add(w).Sub(now)
		}
	}
	ne := l.names[name]
	if ne != nil {
		ne.fails = recent(ne.fails, since)
		if ne.inflight {
			return nil, false, inflightWait
		}
		if n := len(ne.fails); n >= nf {
			wait := 30 * time.Second << min(n-nf, 10)
			if wait > w {
				wait = w
			}
			if until := ne.fails[n-1].Add(wait); now.Before(until) {
				return nil, false, until.Sub(now)
			}
		}
	}
	if ne == nil {
		if !room(l.names, since, mk) {
			return nil, false, w
		}
		ne = &limitEntry{}
		l.names[name] = ne
	}
	if client != "" && ce == nil {
		if !room(l.client, since, mk) {
			if len(ne.fails) == 0 {
				delete(l.names, name)
			}
			return nil, false, w
		}
		ce = &limitEntry{}
		l.client[client] = ce
	}
	ne.inflight = true
	if client != "" {
		ce.inflight = true
	}
	return &Attempt{l: l, name: name, client: client}, true, 0
}

func (a *Attempt) end(fail, success bool) {
	a.once.Do(func() {
		l := a.l
		w, _, _, _, now := l.defaults()
		l.mu.Lock()
		defer l.mu.Unlock()
		if e := l.names[a.name]; e != nil {
			e.inflight = false
			switch {
			case success:
				delete(l.names, a.name) // one success does not excuse the address's spray, so only the name is cleared
			case fail:
				e.fails = appendFail(recent(e.fails, now.Add(-w)), now)
			}
		}
		if a.client != "" {
			if e := l.client[a.client]; e != nil {
				e.inflight = false
				if fail {
					e.fails = appendFail(recent(e.fails, now.Add(-w)), now)
				}
			}
		}
	})
}

func appendFail(ts []time.Time, now time.Time) []time.Time {
	ts = append(ts, now)
	if len(ts) > 64 {
		ts = ts[len(ts)-64:]
	}
	return ts
}

// Fail records the attempt as a failure.
func (a *Attempt) Fail() { a.end(true, false) }

// Succeed forgets the name's failures.
func (a *Attempt) Succeed() { a.end(false, true) }

// Done ends an attempt with no verdict (the server failed, or the request was malformed).
func (a *Attempt) Done() { a.end(false, false) }
