package server

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/hosts"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
)

// Which sacct columns a host's sacct accepts (ADR 0044). A job's state must never depend on the optional usage columns:
// a site whose sacct rejects one (older Slurm, other accounting setup) answers with an error and no rows, so the
// extended call falls back, within the same refresh, to the minimal columns the status parser always needed, and the
// host remembers it. Usage is then recorded with the columns the site lacks named, not as zeros.

// sacctReprobe is how long what was learned about a host's sacct is trusted before it is asked again.
const sacctReprobe = 6 * time.Hour

type sacctCap struct {
	fields      []string  // the columns to ask for (always starts with the minimal six)
	unsupported []string  // optional columns this host's sacct does not have
	at          time.Time // when this was learned
}

// sacctMemory is that knowledge per host, in memory only: a restart asks again.
type sacctMemory struct {
	mu     sync.Mutex
	byHost map[int64]sacctCap
}

func (m *sacctMemory) get(id int64, now time.Time) (sacctCap, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.byHost[id]
	return c, ok && now.Sub(c.at) < sacctReprobe
}

func (m *sacctMemory) set(id int64, c sacctCap) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.byHost == nil {
		m.byHost = map[int64]sacctCap{}
	}
	m.byHost[id] = c
}

// capFrom builds the column list from the optional columns known to be missing.
func capFrom(missing map[string]bool, now time.Time) sacctCap {
	c := sacctCap{fields: jobs.Fields(jobs.MinimalFields), at: now}
	for _, f := range jobs.Fields(jobs.UsageFields)[len(c.fields):] {
		if missing[strings.ToLower(f)] {
			c.unsupported = append(c.unsupported, f)
		} else {
			c.fields = append(c.fields, f)
		}
	}
	return c
}

// probeSacct asks the host which columns its sacct knows (sacct --helpformat). Output it cannot read (a sacct without
// the option, a login banner only) is not an answer: all columns are tried, and the fallback catches a rejection.
func (s *Server) probeSacct(ctx context.Context, h hosts.Host, now time.Time) (sacctCap, error) {
	out, err := s.Remote.RunShell(ctx, h, `sacct --helpformat 2>/dev/null; true`, nil, 30*time.Second)
	if err != nil {
		return sacctCap{}, err
	}
	listed := map[string]bool{}
	for _, w := range strings.Fields(string(out)) {
		listed[strings.ToLower(w)] = true
	}
	if !listed["jobid"] || !listed["state"] {
		return capFrom(nil, now), nil
	}
	missing := map[string]bool{}
	for _, f := range jobs.Fields(jobs.UsageFields) {
		if !listed[strings.ToLower(f)] {
			missing[strings.ToLower(f)] = true
		}
	}
	return capFrom(missing, now), nil
}

// sacctTry runs the status command with the given columns: the answer to parse, sacct's exit status and its stderr.
func (s *Server) sacctTry(ctx context.Context, h hosts.Host, ids, fields []string) (body string, exit int, stderr string, err error) {
	cmd, err := jobs.BatchStatusCommandFields(ids, fields)
	if err != nil {
		return "", 0, "", err
	}
	out, err := s.Remote.RunShell(ctx, h, cmd, nil, 30*time.Second)
	if err != nil {
		return "", 0, "", err
	}
	body, exit, stderr = jobs.SplitSacctExit(string(out))
	return body, exit, stderr, nil
}

// hostStatus is the one status call of a host for the watcher and the Refresh button: the state of each Slurm id, with
// usage where the host's sacct reports it. It errors only when the host cannot be asked or even the minimal sacct fails.
func (s *Server) hostStatus(ctx context.Context, h hosts.Host, ids []string, now time.Time) (map[string]jobs.Status, error) {
	c, ok := s.sacct.get(h.ID, now)
	if !ok {
		var err error
		if c, err = s.probeSacct(ctx, h, now); err != nil {
			return nil, err
		}
		s.sacct.set(h.ID, c)
	}
	// Ask with the known-good columns. If sacct refuses and names a column, ask once more without that one; any other
	// failure (or a second one) goes to the minimal columns, which every sacct accepts, so states keep updating.
	minimal := jobs.Fields(jobs.MinimalFields)
	fields, missing, named := c.fields, map[string]bool{}, false
	for _, f := range c.unsupported {
		missing[strings.ToLower(f)] = true
	}
	var body, stderr string
	var exit int
	for {
		var err error
		if body, exit, stderr, err = s.sacctTry(ctx, h, ids, fields); err != nil {
			return nil, err
		}
		if (exit == 0 && jobs.RowsFit(body, len(fields))) || len(fields) <= len(minimal) {
			break
		}
		if r := jobs.RejectedField(stderr); r != "" && !named && contains(fields[len(minimal):], r) {
			named, missing[strings.ToLower(r)] = true, true
			fields = without(fields, r)
			continue
		}
		for _, f := range fields[len(minimal):] {
			missing[strings.ToLower(f)] = true
		}
		fields = minimal
	}
	if exit == 0 && len(fields) < len(c.fields) {
		c = sacctCap{fields: fields, unsupported: reasons(missing), at: now} // remembered: later calls skip the rejected columns
		s.sacct.set(h.ID, c)
	}
	if exit != 0 {
		if stderr == "" {
			stderr = fmt.Sprintf("exit status %d", exit)
		}
		return nil, fmt.Errorf("sacct failed on the host: %s", stderr)
	}
	st := jobs.ParseBatchStatusFields(body, ids, fields)
	for id, v := range st {
		if v.Usage != nil {
			v.Usage.Unsupported = append([]string{}, c.unsupported...)
			st[id] = v
		}
	}
	return st, nil
}

// reasons lists the missing columns in the order sacct is asked for them.
func reasons(missing map[string]bool) []string {
	out := []string{}
	for _, f := range jobs.Fields(jobs.UsageFields) {
		if missing[strings.ToLower(f)] {
			out = append(out, f)
		}
	}
	return out
}

func contains(list []string, f string) bool {
	for _, x := range list {
		if strings.EqualFold(x, f) {
			return true
		}
	}
	return false
}

func without(list []string, f string) []string {
	out := make([]string, 0, len(list))
	for _, x := range list {
		if !strings.EqualFold(x, f) {
			out = append(out, x)
		}
	}
	return out
}
