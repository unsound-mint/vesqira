package relay

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// DecodePage reads one pull as the key holder's computer receives it, refusing the whole page for anything it does not expect
// (ADR 0066 section 3, as ADR 0036 does for a reader's sync): more than MaxPageBytes, an unknown field, trailing data, more than
// MaxPage requests, a cursor that goes back, or that jumps more than MaxCursorJump, or changes that do not strictly increase up
// to it. The server may be compromised: a refused page changes nothing here.
func DecodePage(b []byte, after int64) (Page, error) {
	var p Page
	if len(b) > MaxPageBytes {
		return p, refuse("the server's answer is larger than %d bytes", MaxPageBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return Page{}, refuse("the server's answer could not be read strictly (%s)", err.Error())
	}
	if _, err := dec.Token(); err != io.EOF { // More() is false before a stray ] or }, so read to the end instead
		return Page{}, refuse("the server's answer carries trailing data")
	}
	if len(p.Requests) > MaxPage {
		return Page{}, refuse("the server sent more than %d requests in one answer", MaxPage)
	}
	if p.Cursor < after || p.Cursor-after > MaxCursorJump || (len(p.Requests) > 0 && p.Cursor <= after) {
		return Page{}, refuse("the server's cursor went from %d to %d", after, p.Cursor)
	}
	last := after
	for _, r := range p.Requests {
		if r.ID <= 0 || r.Change <= last || r.Change > p.Cursor {
			return Page{}, refuse("the server's changes are out of order")
		}
		last = r.Change
	}
	if len(p.Requests) > 0 && last != p.Cursor {
		return Page{}, refuse("the server's cursor is not its last change")
	}
	return p, nil
}

var knownStates = map[string]bool{StateRequested: true, StateReview: true, StateApproved: true, StateSubmitted: true, StateRunning: true,
	StateFinished: true, StateFailed: true, StateUnknown: true, StateDeclined: true, StateWithdrawn: true, StateExpired: true}

// CheckRequest is the key holder's computer's own validation of one pulled request, the real defence (ADR 0066 section 3): the ask
// against the inbox this computer stored (never bounds the server echoes), a label this computer maps to a host, a base named only
// by its opaque lineage key and hash, and plain text for every name and note. A refusal is shown to the key holder and sent back as
// the decline's note.
func CheckRequest(in Inbox, hosts map[string]int64, r Request) error {
	if !knownStates[r.State] {
		return refuse("the request's state %q is not one this computer knows", r.State)
	}
	if err := CheckAsk(in, r.Ask); err != nil {
		return err
	}
	if _, ok := hosts[strings.TrimSpace(r.Ask.Label)]; !ok {
		return refuse("the label %q is not one this computer runs requests for", r.Ask.Label)
	}
	if r.Base != nil {
		if !ValidOrigin(r.Base.Origin) || !ValidSHA(r.Base.Hash) || (r.Base.Instance != "" && !ValidInstance(r.Base.Instance)) {
			return refuse("the request's base is not a pushed run")
		}
		if err := PlainText(r.Base.Title, "the base's title"); err != nil || len([]rune(r.Base.Title)) > 200 {
			return refuse("the base's title is not plain text of at most 200 characters")
		}
	}
	if err := PlainText(r.Requester, "the requester's name"); err != nil || strings.ContainsAny(r.Requester, "\n\t") || len([]rune(r.Requester)) > 100 {
		return refuse("the requester's name is not plain text of at most 100 characters")
	}
	if len(r.Notes) > MaxNotes {
		return refuse("a request carries at most %d follow-up notes", MaxNotes)
	}
	for _, n := range r.Notes {
		if err := PlainText(n.Text, "a follow-up note"); err != nil {
			return err
		}
		if err := PlainText(n.By, "a note's author"); err != nil {
			return err
		}
	}
	if !r.ExpiresAt.After(r.CreatedAt) {
		return refuse("the request expires before it was filed")
	}
	return nil
}

// LabelSafe is the local check on a label the key holder types: besides ValidLabel's letters, digits, spaces and -, it may not
// contain a host's ssh destination or name, nor any part of them of 3 or more characters between dots and @ (the user, the login
// node, the site), so no host name reaches the server as a label (ADR 0066 section 3).
func LabelSafe(label string, dests []string) error {
	if !ValidLabel(label) {
		return refuse("a label is 1 to 40 letters, digits, spaces and -")
	}
	l := strings.ToLower(strings.TrimSpace(label))
	for _, d := range dests {
		d = strings.ToLower(strings.TrimSpace(d))
		if d != "" && strings.Contains(l, d) {
			return refuse("the label %q contains a host's address or name; choose a word that does not name the machine", label)
		}
		for _, part := range strings.FieldsFunc(d, func(r rune) bool { return r == '.' || r == '@' }) {
			if len(part) >= 3 && strings.Contains(l, part) {
				return refuse("the label %q contains %q from a host's address; choose a word that does not name the machine", label, part)
			}
		}
	}
	return nil
}

// Short is the first 12 characters of a hash, as the record shows it.
func Short(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// ResourceText renders resources for a Slurm script: memory in G and a time limit of D-HH:MM:SS or HH:MM:SS; "" for zero.
func ResourceText(r Resources) (mem, tm string) {
	if r.MemGiB > 0 {
		mem = fmt.Sprintf("%dG", r.MemGiB)
	}
	if m := r.TimeMinutes; m > 0 {
		d, h, mm := m/1440, (m%1440)/60, m%60
		if d > 0 {
			tm = fmt.Sprintf("%d-%02d:%02d:00", d, h, mm)
		} else {
			tm = fmt.Sprintf("%02d:%02d:00", h, mm)
		}
	}
	return mem, tm
}
