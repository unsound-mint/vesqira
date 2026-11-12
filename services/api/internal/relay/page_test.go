package relay

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

var hostsA = map[string]int64{testLabel: 1}

func pageOf(cursor int64, changes ...int64) Page {
	p := Page{Workspace: "lab", Cursor: cursor, Requests: []Request{}}
	for i, c := range changes {
		r := localReq(int64(i+1), StateRequested)
		r.Change = c
		p.Requests = append(p.Requests, r)
	}
	return p
}

func encode(t *testing.T, p Page) []byte {
	t.Helper()
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestDecodePageAcceptsAWellFormedPage(t *testing.T) {
	p, err := DecodePage(encode(t, pageOf(3, 1, 3)), 0)
	if err != nil || len(p.Requests) != 2 || p.Cursor != 3 {
		t.Fatalf("%v %+v", err, p)
	}
	// an empty page may repeat the cursor
	if _, err := DecodePage(encode(t, pageOf(7)), 7); err != nil {
		t.Fatal(err)
	}
	// the largest allowed page and jump
	var ch []int64
	for i := int64(1); i <= MaxPage; i++ {
		ch = append(ch, i)
	}
	if _, err := DecodePage(encode(t, pageOf(MaxPage, ch...)), 0); err != nil {
		t.Fatalf("100 requests: %v", err)
	}
	if _, err := DecodePage(encode(t, pageOf(MaxCursorJump)), 0); err != nil {
		t.Fatalf("a jump of exactly %d: %v", MaxCursorJump, err)
	}
}

func TestDecodePageRefusesAWholePageThatIsNotStrictlyWhatWasAgreed(t *testing.T) {
	good := string(encode(t, pageOf(2, 1, 2)))
	bigPage := pageOf(1, 1)
	bigPage.Requests[0].Ask.Note = strings.Repeat("x", MaxPageBytes)
	var many []int64
	for i := int64(1); i <= MaxPage+1; i++ {
		many = append(many, i)
	}
	// a request id of zero or below is never valid
	zeroID := pageOf(1, 1)
	zeroID.Requests[0].ID = 0

	cases := map[string]struct {
		body  []byte
		after int64
	}{
		"larger than 2 MiB":         {encode(t, bigPage), 0},
		"just over 2 MiB of spaces": {[]byte(good + strings.Repeat(" ", MaxPageBytes)), 0},
		"unknown field":             {[]byte(strings.Replace(good, `"workspace"`, `"extra":1,"workspace"`, 1)), 0},
		"unknown nested field":      {[]byte(strings.Replace(good, `"requester"`, `"admin":true,"requester"`, 1)), 0},
		"trailing object":           {[]byte(good + ` {}`), 0},
		"trailing text":             {[]byte(good + ` x`), 0},
		"not json":                  {[]byte(`<html>`), 0},
		"empty body":                {nil, 0},
		"wrong type":                {[]byte(`{"workspace":"lab","cursor":"2","requests":[]}`), 0},
		"101 requests":              {encode(t, pageOf(MaxPage+1, many...)), 0},
		"cursor goes back":          {encode(t, pageOf(5)), 10},
		"cursor jumps too far":      {encode(t, pageOf(MaxCursorJump+1)), 0},
		"jump counted from after":   {encode(t, pageOf(10+MaxCursorJump+1)), 10},
		"requests without progress": {encode(t, pageOf(10, 10)), 10},
		"changes repeat":            {encode(t, pageOf(5, 5, 5)), 0},
		"changes fall":              {encode(t, pageOf(6, 6, 5)), 0},
		"change not after cursor":   {encode(t, pageOf(5, 3, 2)), 3},
		"change above the cursor":   {encode(t, pageOf(2, 1, 3)), 0},
		"cursor is not last change": {encode(t, pageOf(3, 1, 2)), 0},
		"change before after":       {encode(t, pageOf(8, 4, 8)), 5},
		"request id zero":           {encode(t, zeroID), 0},
	}
	for name, c := range cases {
		if _, err := DecodePage(c.body, c.after); err == nil {
			t.Errorf("%s: accepted", name)
		} else if !isInvalid(err) {
			t.Errorf("%s: %v does not wrap ErrInvalid", name, err)
		}
	}
}

// json.Decoder.More reports false before a closing brace or bracket, so the decoder must read to the end to see a stray "}" or "]".
func TestDecodePageRefusesAStrayClosingBraceAfterThePage(t *testing.T) {
	good := string(encode(t, pageOf(2, 1, 2)))
	for _, tail := range []string{"}", "]", " }"} {
		if _, err := DecodePage([]byte(good+tail), 0); err == nil {
			t.Errorf("a page followed by %q was accepted", tail)
		}
	}
}

func isInvalid(err error) bool { return strings.HasPrefix(err.Error(), ErrInvalid.Error()) }

func validRequest() Request {
	r := localReq(1, StateRequested)
	return r
}

func TestCheckRequestAcceptsAWellFormedRequest(t *testing.T) {
	r := validRequest()
	r.Base = &Base{Origin: strings.Repeat("a", 32), Hash: shaA, Title: "VGT run, LMC", Instance: inst1}
	r.Notes = []FollowUp{{By: "bob", Text: "thanks"}}
	if err := CheckRequest(vgtInbox(), hostsA, r); err != nil {
		t.Fatal(err)
	}
}

func TestCheckRequestIsTheLocalDefence(t *testing.T) {
	long := func(n int) string { return strings.Repeat("x", n) }
	cases := map[string]func(*Request){
		"unknown state":          func(r *Request) { r.State = "approved-ish" },
		"empty state":            func(r *Request) { r.State = "" },
		"label not mapped":       func(r *Request) { r.Ask.Label = "Other" },
		"ask outside the inbox":  func(r *Request) { r.Ask.Params = map[string]string{"dn": "100"} },
		"forbidden key":          func(r *Request) { r.Ask.Params = map[string]string{"output_dir": "/tmp/x"} },
		"injected value":         func(r *Request) { r.Ask.Params = map[string]string{"ker": "$(id)"} },
		"custom template":        func(r *Request) { r.Ask.Template = "custom" },
		"resources over limits":  func(r *Request) { r.Ask.Resources.CPUs = 1000 },
		"note with control char": func(r *Request) { r.Ask.Note = "a\x1b[31mred" },
		"requester newline":      func(r *Request) { r.Requester = "bob\nadmin" },
		"requester tab":          func(r *Request) { r.Requester = "bob\tx" },
		"requester control":      func(r *Request) { r.Requester = "bob\x00" },
		"requester too long":     func(r *Request) { r.Requester = long(101) },
		"requester invalid utf8": func(r *Request) { r.Requester = "bob\xff" },
		"follow-up control":      func(r *Request) { r.Notes = []FollowUp{{By: "bob", Text: "a\x00"}} },
		"follow-up author":       func(r *Request) { r.Notes = []FollowUp{{By: "b\x07", Text: "a"}} },
		"follow-up too long":     func(r *Request) { r.Notes = []FollowUp{{By: "bob", Text: long(MaxNote + 1)}} },
		"21 follow-ups": func(r *Request) {
			for i := 0; i <= MaxNotes; i++ {
				r.Notes = append(r.Notes, FollowUp{By: "bob", Text: "x"})
			}
		},
		"expires before creation": func(r *Request) { r.ExpiresAt = r.CreatedAt.Add(-time.Hour) },
		"expires at creation":     func(r *Request) { r.ExpiresAt = r.CreatedAt },
		"zero times":              func(r *Request) { r.CreatedAt, r.ExpiresAt = time.Time{}, time.Time{} },
		"base origin":             func(r *Request) { r.Base = &Base{Origin: "../../etc", Hash: shaA} },
		"base hash":               func(r *Request) { r.Base = &Base{Origin: strings.Repeat("a", 32), Hash: "abc"} },
		"base instance":           func(r *Request) { r.Base = &Base{Origin: strings.Repeat("a", 32), Hash: shaA, Instance: "x"} },
		"base title control":      func(r *Request) { r.Base = &Base{Origin: strings.Repeat("a", 32), Hash: shaA, Title: "a\x00"} },
		"base title too long":     func(r *Request) { r.Base = &Base{Origin: strings.Repeat("a", 32), Hash: shaA, Title: long(201)} },
	}
	for name, mod := range cases {
		r := validRequest()
		mod(&r)
		if err := CheckRequest(vgtInbox(), hostsA, r); err == nil {
			t.Errorf("%s: accepted", name)
		} else if !isInvalid(err) {
			t.Errorf("%s: %v does not wrap ErrInvalid", name, err)
		}
	}
	// the host map is this computer's own: a label it does not map is refused even when the inbox lists it
	if err := CheckRequest(vgtInbox(), map[string]int64{}, validRequest()); err == nil {
		t.Error("a label without a host was accepted")
	}
	if err := CheckRequest(vgtInbox(), nil, validRequest()); err == nil {
		t.Error("a nil host map was accepted")
	}
}

// The bounds are the local inbox's, never anything the server echoes: a request is checked against what this computer stored.
func TestCheckRequestUsesTheLocalInboxNotTheRequestsClaims(t *testing.T) {
	loose := openInbox()
	strict := vgtInbox()
	r := validRequest()
	r.Ask.Params = map[string]string{"dn": "50"}
	if err := CheckRequest(loose, hostsA, r); err != nil {
		t.Fatalf("loose inbox: %v", err)
	}
	if err := CheckRequest(strict, hostsA, r); err == nil {
		t.Fatal("a request outside the stored inbox was accepted")
	}
}
