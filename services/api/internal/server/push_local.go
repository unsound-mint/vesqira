package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/authz"
	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	idty "github.com/unsound-mint/vesqira/services/api/internal/identity"
	"github.com/unsound-mint/vesqira/services/api/internal/imgmeta"
	"github.com/unsound-mint/vesqira/services/api/internal/outbox"
	"github.com/unsound-mint/vesqira/services/api/internal/pushwire"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// Push to the group server (ADR 0066 sections 1 and 2), the local instance's side: link this computer to a server with a token
// minted there ("Link a computer"), freeze a board, figure, run or report into the document reviewers would see, queue it, and
// let a background pass send the queue. Enqueueing never waits on the network, and the pass never holds a row or a transaction
// across a request. Every route here is local.only: the group server never mounts them.
func (s *Server) outboxRoutes(mux *http.ServeMux) {
	s.route(mux, "GET /api/group-link", authz.LocalOnly, s.groupLinkState)
	s.route(mux, "POST /api/group-link", authz.LocalOnly, s.linkGroup)
	s.route(mux, "POST /api/group-link/unlink", authz.LocalOnly, s.unlinkGroup)
	s.route(mux, "GET /api/outbox", authz.LocalOnly, s.listOutbox)
	s.route(mux, "POST /api/outbox", authz.LocalOnly, s.enqueuePush)
	s.route(mux, "POST /api/outbox/preflight", authz.LocalOnly, s.pushPreflight)
	s.route(mux, "POST /api/outbox/retry", authz.LocalOnly, s.retryOutbox)
}

// outboxState is the background pass's state.
type outboxState struct {
	outboxOnce sync.Once
	outboxKick chan struct{}
	outboxMu   sync.Mutex // one pass at a time
	// OutboxNow is the pass's clock (tests move it); nil is time.Now.
	OutboxNow func() time.Time
}

func (s *Server) outboxNow() time.Time {
	if s.OutboxNow != nil {
		return s.OutboxNow()
	}
	return time.Now()
}

func (s *Server) kick() chan struct{} {
	s.outboxOnce.Do(func() { s.outboxKick = make(chan struct{}, 1) })
	return s.outboxKick
}

// kickOutbox asks the background pass to run now; it never blocks.
func (s *Server) kickOutbox() {
	select {
	case s.kick() <- struct{}{}:
	default:
	}
}

// RunOutbox sends the queue until ctx ends: at start, when something is queued, when an item's wait ends, and at least every
// minute while linked.
func (s *Server) RunOutbox(ctx context.Context) {
	for {
		s.OutboxPass(ctx)
		wait := time.Minute
		if next := s.Outbox.NextWake(); !next.IsZero() {
			if d := time.Until(next); d < wait {
				wait = max(d, time.Second)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-s.kick():
		case <-time.After(wait):
		}
	}
}

// ---- the client ----

// groupClient talks to the linked group server with the push token. The token is read from its 0600 file on every pass and goes
// only to the linked https origin, with certificate verification and no redirects.
type groupClient struct {
	base, token, instance string
	http                  *http.Client
}

// groupError is what the server answered (never holding the token), or that it could not be reached (Status 0).
type groupError struct {
	Status  int
	Msg     string
	Missing []string
}

func (e *groupError) Error() string {
	if e.Status == 0 {
		return "server unreachable"
	}
	return e.Msg
}

// transient: a network error, a 5xx or a 429 is retried; any other answer is final for that item (ADR 0066 retries).
func (e *groupError) transient() bool { return e.Status == 0 || e.Status >= 500 || e.Status == 429 }

var errNotLinked = errors.New("this computer is not linked to a group server: link it under Share first")

func (s *Server) httpForGroup() *http.Client {
	if s.GroupHTTP != nil {
		return s.GroupHTTP
	}
	return &http.Client{Timeout: 60 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (s *Server) groupClient() (*groupClient, outbox.Link, error) {
	if s.Outbox == nil || s.GroupKeyFile == "" {
		return nil, outbox.Link{}, errNotLinked
	}
	l, err := s.Outbox.Link()
	if err != nil {
		return nil, outbox.Link{}, errNotLinked
	}
	tok, err := readKey(s.GroupKeyFile)
	if err != nil {
		if errors.Is(err, errNotConfigured) {
			return nil, l, errNotLinked
		}
		return nil, l, err
	}
	return &groupClient{base: l.Origin, token: tok, instance: l.Instance, http: s.httpForGroup()}, l, nil
}

func (c *groupClient) do(method, path, contentType string, body []byte, out any) error {
	req, err := http.NewRequest(method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return &groupError{Status: 0, Msg: "server unreachable"}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		var e struct {
			Error   string   `json:"error"`
			Missing []string `json:"missing"`
		}
		msg := strings.TrimSpace(string(b))
		if json.Unmarshal(b, &e) == nil && e.Error != "" {
			msg = e.Error
		}
		if len(msg) > 300 {
			msg = msg[:300]
		}
		if resp.StatusCode == 401 {
			msg = "the server does not accept this computer's token (revoked or expired): link this computer again"
		}
		return &groupError{Status: resp.StatusCode, Msg: msg, Missing: e.Missing}
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			return &groupError{Status: 502, Msg: "the server's answer could not be read"}
		}
	}
	return nil
}

func (c *groupClient) postJSON(path string, in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return c.do("POST", path, "application/json", b, out)
}

func (c *groupClient) heads() (string, error) {
	var out struct {
		Workspace string `json:"workspace"`
	}
	err := c.do("GET", "/api/push/heads?instance="+url.QueryEscape(c.instance), "", nil, &out)
	return out.Workspace, err
}

// ---- linking ----

// checkGroupOrigin accepts an https origin only (ADR 0066: the token goes only to the configured https origin).
func checkGroupOrigin(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", fmt.Errorf("the group server address must look like https://lab.example.org")
	}
	return "https://" + u.Host, nil
}

type groupLinkBody struct {
	Linked    bool       `json:"linked"`
	Origin    string     `json:"origin,omitempty"`
	Workspace string     `json:"workspace,omitempty"`
	LinkedAt  *time.Time `json:"linkedAt,omitempty"`
}

func (s *Server) linkBody() groupLinkBody {
	l, err := s.Outbox.Link()
	if err != nil {
		return groupLinkBody{}
	}
	if _, err := os.Stat(s.GroupKeyFile); err != nil {
		return groupLinkBody{}
	}
	at := l.LinkedAt
	return groupLinkBody{Linked: true, Origin: l.Origin, Workspace: l.Workspace, LinkedAt: &at}
}

func (s *Server) groupLinkState(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.linkBody())
}

// linkGroup links this computer: the address and the token minted with "Link a computer" on the server. The token is written to
// a fresh 0600 file beside the catalog (never the catalog), then checked against the server; a token the server refuses is
// removed again. Linking to another address starts a new instance and empties the queue.
func (s *Server) linkGroup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Origin string `json:"origin"`
		Token  string `json:"token"`
	}
	if !body(w, r, 4<<10, &in, true) {
		return
	}
	origin, err := checkGroupOrigin(in.Origin)
	if err != nil {
		fail(w, 422, err.Error())
		return
	}
	tok := strings.TrimSpace(in.Token)
	if !strings.HasPrefix(tok, "vsq_") || len(tok) > 100 || strings.ContainsAny(tok, " \t\r\n") {
		fail(w, 422, "paste the token the group server showed under Link a computer (it starts with vsq_)")
		return
	}
	if old, err := s.Outbox.Link(); err == nil && old.Origin != origin {
		_ = os.Remove(s.GroupKeyFile) // a changed server address never inherits the token
	}
	if err := writeKey(s.GroupKeyFile, tok); err != nil {
		log.Printf("group key: %v", err)
		fail(w, 500, "the token could not be saved")
		return
	}
	l, err := s.Outbox.SetLink(origin, "")
	if err != nil {
		_ = os.Remove(s.GroupKeyFile)
		fail(w, 500, "internal error")
		return
	}
	c := &groupClient{base: origin, token: tok, instance: l.Instance, http: s.httpForGroup()}
	name, err := c.heads()
	if err != nil {
		_ = os.Remove(s.GroupKeyFile)
		_ = s.Outbox.Unlink()
		var ge *groupError
		if errors.As(err, &ge) && ge.Status == 0 {
			fail(w, 502, "the group server could not be reached; check the address and the network")
			return
		}
		fail(w, 422, "the group server refused the token: "+err.Error())
		return
	}
	if _, err := s.Outbox.SetLink(origin, name); err != nil {
		fail(w, 500, "internal error")
		return
	}
	s.audit(r.Context(), "linked to a group server", "group", 0, origin+" workspace "+name)
	s.kickOutbox()
	writeJSON(w, 200, s.linkBody())
}

func (s *Server) unlinkGroup(w http.ResponseWriter, r *http.Request) {
	if err := s.Outbox.Unlink(); err != nil {
		fail(w, 500, "internal error")
		return
	}
	if err := os.Remove(s.GroupKeyFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		fail(w, 500, "the token file could not be removed")
		return
	}
	s.audit(r.Context(), "unlinked from the group server", "group", 0, "")
	writeJSON(w, 200, s.linkBody())
}

// ---- freezing ----

// frozenPush is one item ready to queue: the request body, the cleaned pictures, and what the preflight shows.
type frozenPush struct {
	Push     pushwire.Push
	Assets   map[string][]byte
	Warnings []string
}

// cleanPicture strips a picture's metadata (internal/imgmeta), as publishing does, and returns the hash of the bytes sent.
func cleanPicture(b []byte) (string, []byte, error) {
	clean, _, _, err := imgmeta.CleanWithNotes(b)
	if err != nil {
		return "", nil, errCannotClean
	}
	sum := sha256.Sum256(clean)
	return hex.EncodeToString(sum[:]), clean, nil
}

// referenceOf is the reference and the push-only identity of one dataset of this computer: the file name and size, and the
// fingerprints of its primary location. Never its path, host or id.
func (s *Server) referenceOf(ws workspace.ID, role string, datasetID int64) (pushwire.Reference, pushwire.Identity, bool) {
	d, err := s.Store.Get(ws, datasetID)
	if err != nil {
		return pushwire.Reference{}, pushwire.Identity{}, false
	}
	ref := pushwire.Reference{Role: role, Name: d.Name, SizeBytes: d.SizeBytes}
	var id pushwire.Identity
	if locs, err := s.Store.Locations(ws, d.ID); err == nil {
		if p, ok := primaryOf(locs); ok {
			if p.Sampled != nil {
				id.Sampled, id.Algorithm = p.Sampled.Value, idty.SampledAlgorithm
			}
			if p.Full != nil {
				id.Full = p.Full.Value
			}
		}
	}
	return ref, id, true
}

// freezePush builds the item for one local object with the code that already freezes it for reviewers.
func (s *Server) freezePush(ws workspace.ID, l outbox.Link, kind string, id int64) (frozenPush, error) {
	f := frozenPush{Assets: map[string][]byte{}, Warnings: []string{}}
	it := pushwire.Item{Kind: kind, Origin: pushwire.OriginKey(l.Secret, kind, id), Assets: []string{}, References: []pushwire.Reference{}}
	ids := []pushwire.Identity{}
	var doc any
	dropped := 0 // run settings that look like a machine, account or address, left out
	switch kind {
	case pushwire.KindFigure:
		if s.Boards == nil {
			return f, errors.New("figures are not available")
		}
		fig, err := s.Boards.Figure(ws, id)
		if err != nil {
			return f, err
		}
		raw, err := s.Boards.Preview(fig.PreviewSHA)
		if err != nil {
			return f, fmt.Errorf("the picture of %q cannot be read", fig.Title)
		}
		sha, clean, err := cleanPicture(raw)
		if err != nil {
			return f, fmt.Errorf("the picture of %q could not be cleaned of its metadata", fig.Title)
		}
		f.Assets[sha] = clean
		it.Title = fig.Title
		var fid figureIdentity
		if len(fig.Identity) > 0 && json.Unmarshal(fig.Identity, &fid) == nil {
			for _, d := range fid.Datasets {
				// the identity recorded when the figure was saved, whitelisted: name, size and the two fingerprints only
				ref := pushwire.Reference{Role: d.Role, Name: d.Name, SizeBytes: d.SizeBytes}
				var e pushwire.Identity
				if d.Sampled != nil {
					e.Sampled, e.Algorithm = d.Sampled.Value, idty.SampledAlgorithm
				}
				if d.Full != nil {
					e.Full = d.Full.Value
				}
				it.References, ids = append(it.References, ref), append(ids, e)
			}
		}
		prov, n := dropMachineParamsIn(fig.Provenance, false)
		dropped += n
		doc = pushedFigure{Title: fig.Title, Caption: fig.Caption, Revision: fig.Revision, Asset: sha, Width: fig.Width, Height: fig.Height, Provenance: prov}
	case pushwire.KindRun:
		if s.Runs == nil {
			return f, errors.New("runs are not available")
		}
		sum, err := s.Runs.SummaryOf(ws, id, "")
		if err != nil {
			return f, err
		}
		var n int
		sum.Params, n = dropMachineParams(sum.Params)
		dropped += n
		doc, it.Title = sum, sum.Name
		if run, err := s.Runs.Get(ws, id); err == nil {
			for _, o := range run.Outputs {
				if ref, e, ok := s.referenceOf(ws, "output", o.DatasetID); ok {
					it.References, ids = append(it.References, ref), append(ids, e)
				}
			}
		}
	case pushwire.KindBoard:
		if s.Boards == nil {
			return f, errors.New("boards are not available")
		}
		b, err := s.Boards.Board(ws, id)
		if err != nil {
			return f, err
		}
		if len(b.Items) == 0 || len(b.Items) > pushwire.MaxBoardItems {
			return f, fmt.Errorf("a board is pushed with 1 to %d panels", pushwire.MaxBoardItems)
		}
		items := boards.Freeze(b.Items, s.jobSnapshots(b), s.outboxNow())
		for i := range items {
			if items[i].Kind == boards.KindJob {
				continue
			}
			raw, err := s.Boards.Preview(items[i].PreviewSHA)
			if err != nil {
				return f, fmt.Errorf("the picture of %q cannot be read", items[i].Title)
			}
			sha, clean, err := cleanPicture(raw)
			if err != nil {
				return f, fmt.Errorf("the picture of %q could not be cleaned of its metadata", items[i].Title)
			}
			items[i].PreviewSHA, f.Assets[sha] = sha, clean
			var n int
			items[i].Provenance, n = dropMachineParamsIn(items[i].Provenance, false)
			dropped += n
		}
		doc, it.Title = pushedBoard{Title: b.Title, Items: items}, b.Title
	case pushwire.KindReport:
		if s.Reports == nil || s.Boards == nil {
			return f, errors.New("reports are not available")
		}
		rep, err := s.Reports.Get(ws, actorIDOrOwner(), id)
		if err != nil {
			return f, err
		}
		fr, err := s.freeze(ws, rep, "", true)
		if err != nil {
			return f, err
		}
		if len(fr.Errors) > 0 {
			return f, errors.New(strings.Join(fr.Errors, " "))
		}
		// a working revision of the report, not a publication: its number is the working revision and its time the last save, so
		// pushing an unchanged report again is the same hash
		fr.Revision.Number, fr.Revision.PublishedAt = max(rep.Revision, 1), rep.UpdatedAt.UTC().Truncate(time.Second)
		for i, b := range fr.Revision.Blocks {
			var n int
			switch {
			case b.Figure != nil:
				fr.Revision.Blocks[i].Figure.Provenance, n = dropMachineParamsIn(b.Figure.Provenance, false)
			case len(b.Run) > 0:
				fr.Revision.Blocks[i].Run, n = dropMachineParamsIn(b.Run, true)
			}
			dropped += n
		}
		for sha, b := range fr.Assets {
			f.Assets[sha] = b
		}
		f.Warnings = append(f.Warnings, fr.Warnings...)
		doc, it.Title = fr.Revision, rep.Title
	default:
		return f, fmt.Errorf("push a board, a figure, a run or a report")
	}
	if dropped > 0 {
		f.Warnings = append(f.Warnings, fmt.Sprintf("%d run setting(s) that look like a machine, an account or an address (partition, user, host, an e-mail, a dotted host name) are left out of what is pushed.", dropped))
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return f, err
	}
	it.Doc, it.Assets = raw, assetList(f.Assets)
	f.Push = pushwire.Push{Instance: l.Instance, Item: it, Identity: ids}
	// the server's checks, run here first: what would be refused there is never queued
	if err := pushwire.Check(f.Push); err != nil {
		return f, fmt.Errorf("this cannot be pushed: %v", err)
	}
	if err := checkPushedDoc(it); err != nil {
		return f, fmt.Errorf("this cannot be pushed: %v", err)
	}
	if f.Push.Item.Hash, err = pushwire.ContentHash(it); err != nil {
		return f, err
	}
	return f, nil
}

// machineLike reports whether a run setting looks like a machine, an account or an address (the preflight's rule for published
// reports, identifyingSettings): partition, account, user, host, an e-mail, a dotted host name.
func machineLike(key, value string) bool {
	return identifyingKey.MatchString(key) || identifyingValue.MatchString(value)
}

// dropMachineParams returns the run line without the settings that look like a machine, account or address, and how many it
// left out. A pushed run line never carries partition or account values, cluster usernames or host names (ADR 0066 "what a push
// never carries"); a published report only warns about them, because there the owner decides per reader.
func dropMachineParams(params []runs.Param) ([]runs.Param, int) {
	out := make([]runs.Param, 0, len(params))
	for _, p := range params {
		if !machineLike(p.Key, p.Value) {
			out = append(out, p)
		}
	}
	return out, len(params) - len(out)
}

// dropMachineParamsIn does the same inside a document that carries a run line under "run" (a figure's provenance) or is one (a
// report's run block). Only a document that changes is re-encoded.
func dropMachineParamsIn(raw json.RawMessage, isRun bool) (json.RawMessage, int) {
	var doc map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &doc) != nil {
		return raw, 0
	}
	target := doc
	if !isRun {
		var run map[string]json.RawMessage
		if doc["run"] == nil || json.Unmarshal(doc["run"], &run) != nil || run == nil {
			return raw, 0
		}
		target = run
	}
	var params []runs.Param
	if target["params"] == nil || json.Unmarshal(target["params"], &params) != nil {
		return raw, 0
	}
	kept, n := dropMachineParams(params)
	if n == 0 {
		return raw, 0
	}
	target["params"], _ = json.Marshal(kept)
	if !isRun {
		doc["run"], _ = json.Marshal(target)
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return raw, 0
	}
	return out, n
}

// actorIDOrOwner is the account whose read state a report freeze uses; on a local instance, the local owner.
func actorIDOrOwner() string { return authz.LocalOwnerID }

type pushTarget struct {
	Kind string `json:"kind"`
	ID   int64  `json:"id"`
}

type preflightReference struct {
	Role        string `json:"role"`
	Name        string `json:"name"`
	SizeBytes   int64  `json:"sizeBytes"`
	Fingerprint string `json:"fingerprint"` // full | sampled | none: what the server will be able to compare
}

// pushPreflight shows what would go out (ADR 0066: nothing is pushed without that confirmation): the item, its pictures, settings
// that look like a machine, account or address, and the datasets the server can only match by fingerprint (a name never links).
func (s *Server) pushPreflight(w http.ResponseWriter, r *http.Request) {
	var in pushTarget
	if !body(w, r, 1<<10, &in, true) {
		return
	}
	l, err := s.Outbox.Link()
	if err != nil {
		fail(w, 409, errNotLinked.Error())
		return
	}
	f, err := s.freezePush(wsOf(r), l, in.Kind, in.ID)
	if err != nil {
		s.pushErr(w, err)
		return
	}
	refs := []preflightReference{}
	for i, ref := range f.Push.Item.References {
		fp := "none"
		switch id := f.Push.Identity[i]; {
		case id.Full != "":
			fp = "full"
		case id.Sampled != "":
			fp = "sampled"
		}
		refs = append(refs, preflightReference{Role: ref.Role, Name: ref.Name, SizeBytes: ref.SizeBytes, Fingerprint: fp})
	}
	size := len(f.Push.Item.Doc)
	for _, b := range f.Assets {
		size += len(b)
	}
	writeJSON(w, 200, map[string]any{"kind": in.Kind, "title": f.Push.Item.Title, "workspace": l.Workspace, "origin": l.Origin,
		"pictures": len(f.Assets), "bytes": size, "warnings": f.Warnings, "references": refs})
}

func (s *Server) pushErr(w http.ResponseWriter, err error) {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "not found") || errors.Is(err, os.ErrNotExist):
		fail(w, 404, "not found")
	default:
		fail(w, 422, msg)
	}
}

// enqueuePush freezes the object as it is now and queues it; the background pass sends it. It answers at once.
func (s *Server) enqueuePush(w http.ResponseWriter, r *http.Request) {
	var in pushTarget
	if !body(w, r, 1<<10, &in, true) {
		return
	}
	l, err := s.Outbox.Link()
	if err != nil {
		fail(w, 409, errNotLinked.Error())
		return
	}
	f, err := s.freezePush(wsOf(r), l, in.Kind, in.ID)
	if err != nil {
		s.pushErr(w, err)
		return
	}
	b, err := json.Marshal(f.Push)
	if err != nil {
		fail(w, 500, "internal error")
		return
	}
	e, err := s.Outbox.Enqueue(wsOf(r), in.Kind, in.ID, f.Push.Item.Origin, f.Push.Item.Title, f.Push.Item.Hash, b, f.Assets)
	if err != nil {
		log.Printf("outbox: %v", err)
		fail(w, 500, "internal error")
		return
	}
	s.kickOutbox()
	writeJSON(w, 202, outboxEntry(e))
}

// outboxEntryBody is a queue entry with the sentence the UI shows.
type outboxEntryBody struct {
	outbox.Entry
	Status string `json:"status"`
}

func outboxEntry(e outbox.Entry) outboxEntryBody {
	st := "pushed"
	switch e.State {
	case outbox.Waiting:
		st = "waiting: " + e.Message
	case outbox.Refused:
		st = "refused: " + e.Message
	}
	return outboxEntryBody{e, st}
}

func (s *Server) listOutbox(w http.ResponseWriter, r *http.Request) {
	es, err := s.Outbox.List(wsOf(r))
	if err != nil {
		fail(w, 500, "internal error")
		return
	}
	out := make([]outboxEntryBody, 0, len(es))
	for _, e := range es {
		out = append(out, outboxEntry(e))
	}
	writeJSON(w, 200, map[string]any{"link": s.linkBody(), "entries": out})
}

func (s *Server) retryOutbox(w http.ResponseWriter, r *http.Request) {
	if err := s.Outbox.ResetWaits(); err != nil {
		fail(w, 500, "internal error")
		return
	}
	s.kickOutbox()
	writeJSON(w, 202, map[string]any{"ok": true})
}

// ---- the pass ----

// backoff is the wait before the n-th retry: 1, 2, 4 … minutes, at most 15 (ADR 0066 retries).
func backoff(attempts int) time.Duration {
	d := time.Minute
	for i := 0; i < attempts && d < 15*time.Minute; i++ {
		d *= 2
	}
	return min(d, 15*time.Minute)
}

var kindOrder = map[string]int{pushwire.KindRun: 0, pushwire.KindFigure: 1, pushwire.KindBoard: 2, pushwire.KindReport: 3}

// OutboxPass sends every waiting item that is due, in dependency order (runs, figures, boards, reports), once. Tests call it
// directly; RunOutbox calls it in the background.
func (s *Server) OutboxPass(ctx context.Context) {
	if s.Outbox == nil {
		return
	}
	s.outboxMu.Lock()
	defer s.outboxMu.Unlock()
	c, _, err := s.groupClient()
	if err != nil {
		return
	}
	due, err := s.Outbox.Due(s.outboxNow())
	if err != nil {
		log.Printf("outbox: %v", err)
		return
	}
	sort.SliceStable(due, func(i, j int) bool { return kindOrder[due[i].Kind] < kindOrder[due[j].Kind] })
	for _, d := range due {
		if ctx.Err() != nil {
			return
		}
		s.sendOne(c, d)
	}
}

func (s *Server) sendOne(c *groupClient, d outbox.Due) {
	settle := func(err error) {
		var ge *groupError
		if !errors.As(err, &ge) {
			ge = &groupError{Status: 0, Msg: "server unreachable"}
		}
		if ge.transient() {
			msg := "server unreachable"
			switch {
			case ge.Status == 429:
				msg = "server busy: " + ge.Msg
			case ge.Status >= 500:
				msg = fmt.Sprintf("server error %d", ge.Status)
			}
			_ = s.Outbox.MarkRetry(d.ID, msg, s.outboxNow().Add(backoff(d.Attempts)))
			return
		}
		_ = s.Outbox.MarkRefused(d.ID, ge.Msg)
		log.Printf("outbox: %s %q refused: %s", d.Kind, d.Title, ge.Msg)
	}
	var have struct {
		Missing []string `json:"missing"`
	}
	if err := c.postJSON("/api/push/have", pushwire.Have{Instance: c.instance, Hashes: append([]string{d.Hash}, d.Assets...)}, &have); err != nil {
		settle(err)
		return
	}
	missing := map[string]bool{}
	for _, h := range have.Missing {
		missing[h] = true
	}
	for _, sha := range d.Assets {
		if !missing[sha] {
			continue
		}
		b, err := s.Outbox.Asset(sha)
		if err != nil {
			_ = s.Outbox.MarkRefused(d.ID, "a picture of this item is no longer queued; push it again")
			return
		}
		if err := c.do("PUT", "/api/push/assets/"+sha, "application/octet-stream", b, nil); err != nil {
			settle(err)
			return
		}
	}
	var serverID int64
	if missing[d.Hash] {
		var res struct {
			ID int64 `json:"id"`
		}
		if err := c.do("POST", "/api/push/items", "application/json", d.Body, &res); err != nil {
			settle(err)
			return
		}
		serverID = res.ID
	} // else the server already holds this hash: an earlier answer was lost, and nothing is uploaded twice
	if err := s.Outbox.MarkSent(d.ID, serverID); err != nil {
		log.Printf("outbox: %v", err)
		return
	}
	_ = s.Outbox.ResetWaits() // any success resets the wait of the others
	log.Printf("outbox: %s %q pushed", d.Kind, d.Title)
}
