package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/accounts"
	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/outbox"
	"github.com/unsound-mint/vesqira/services/api/internal/pushed"
	"github.com/unsound-mint/vesqira/services/api/internal/pushwire"
	"github.com/unsound-mint/vesqira/services/api/internal/reports"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// ADR 0066 sections 1 and 2, slice 4: a local instance pushes frozen work to a group server.

// wire records every request a local instance sends to the group server and every answer, and can cut the network.
type wire struct {
	mu   sync.Mutex
	base http.RoundTripper
	down bool
	log  []exchange
}

type exchange struct {
	method, url string
	header      http.Header
	body, reply []byte
	status      int
}

func (w *wire) RoundTrip(q *http.Request) (*http.Response, error) {
	var b []byte
	if q.Body != nil {
		b, _ = io.ReadAll(q.Body)
		q.Body = io.NopCloser(bytes.NewReader(b))
	}
	w.mu.Lock()
	down := w.down
	w.mu.Unlock()
	if down {
		return nil, errors.New("dial tcp: connect: network is unreachable")
	}
	resp, err := w.base.RoundTrip(q)
	if err != nil {
		return nil, err
	}
	rb, _ := io.ReadAll(resp.Body)
	resp.Body = io.NopCloser(bytes.NewReader(rb))
	w.mu.Lock()
	w.log = append(w.log, exchange{method: q.Method, url: q.URL.String(), header: q.Header.Clone(), body: b, reply: rb, status: resp.StatusCode})
	w.mu.Unlock()
	return resp, nil
}

func (w *wire) setDown(v bool) { w.mu.Lock(); w.down = v; w.mu.Unlock() }

func (w *wire) exchanges() []exchange {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]exchange(nil), w.log...)
}

// pushGroup is a group server on a real TLS listener, so the local instance reaches it as it reaches a real one: https, with the
// test server's certificate trusted and the Host checked against the public origin.
func pushGroup(t *testing.T) (*group, *httptest.Server, *pushed.Store) {
	t.Helper()
	g := newGroup(t)
	ts := httptest.NewUnstartedServer(nil)
	addr := ts.Listener.Addr().String()
	g.host, g.origin = addr, "https://"+addr
	g.s.MultiUser.PublicOrigin = g.origin
	ps, err := pushed.Open(g.cat.DB(), filepath.Join(t.TempDir(), "pushed"))
	if err != nil {
		t.Fatal(err)
	}
	ps.Now = g.clock.now
	g.s.Pushed = ps
	g.h = g.s.Handler()
	ts.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { g.h.ServeHTTP(w, r) })
	ts.StartTLS()
	t.Cleanup(ts.Close)
	return g, ts, ps
}

// mintPush makes a push token for the session's account in its current workspace, over HTTP as "Link a computer" does.
func (g *group) mintPush(session string) string {
	g.t.Helper()
	w := g.do(req{method: "POST", path: "/api/account/tokens", body: `{"name":"laptop","scopes":["push"],"days":30}`, cookie: session})
	if w.Code != 201 {
		g.t.Fatalf("mint push token: %d %s", w.Code, w.Body)
	}
	return decode[struct {
		Token string `json:"token"`
	}](g.t, w).Token
}

// pngWithText is a small PNG that carries a text chunk (the kind of metadata a picture must lose before it leaves the computer).
func pngWithText(t *testing.T, text string) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, 16, 12))); err != nil {
		t.Fatal(err)
	}
	raw := b.Bytes()
	data := append([]byte("tEXt"), []byte("Author\x00"+text)...)
	chunk := make([]byte, 4, 12+len(data))
	binary.BigEndian.PutUint32(chunk, uint32(len(data)-4))
	chunk = append(chunk, data...)
	chunk = binary.BigEndian.AppendUint32(chunk, crc32.ChecksumIEEE(data))
	iend := len(raw) - 12
	out := append(append(append([]byte{}, raw[:iend]...), chunk...), raw[iend:]...)
	return out
}

// pushLocal is a local instance with a dataset under a distinctively named folder, a figure of it whose picture carries
// metadata, a run whose settings include a partition and an account, a board and a report. Its local ids start at distinctive
// numbers, so any id on the wire can be found.
type pushLocal struct {
	s                                   *Server
	ts                                  *httptest.Server
	base, root, folder, runDir          string
	dataset, figure, board, run, report int64
	wire                                *wire
	clock                               *groupClock
}

const (
	pictureMark  = "LEAKAUTHOR-q7"
	refusedToken = "vsq_not-a-real-token"
)

func newPushLocal(t *testing.T, group *httptest.Server) *pushLocal {
	t.Helper()
	base := t.TempDir()
	var rnd [4]byte
	_, _ = rand.Read(rnd[:])
	folder := "LEAKDIR-" + hex.EncodeToString(rnd[:])
	root := filepath.Join(base, folder)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	rs, _ := sourceroot.New([]string{root})
	cat, err := catalog.Open("file:" + filepath.Join(base, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cat.Close() })
	bs, _ := boards.Open(cat.DB(), base)
	rn, _ := runs.Open(cat.DB())
	rp, err := reports.Open(cat.DB(), base)
	if err != nil {
		t.Fatal(err)
	}
	ob, err := outbox.Open(cat.DB(), filepath.Join(base, "outbox"))
	if err != nil {
		t.Fatal(err)
	}
	for tbl, seq := range map[string]int{"datasets": 7340000, "figures": 7350000, "boards": 7360000, "runs": 7370000, "reports": 7380000} {
		if _, err := cat.DB().Exec(`INSERT INTO sqlite_sequence(name, seq) VALUES(?, ?)`, tbl, seq); err != nil {
			t.Fatal(err)
		}
	}
	clock := &groupClock{t: time.Unix(1_800_000_000, 0)}
	ob.Now = clock.now
	w := &wire{base: group.Client().Transport}
	s := &Server{Roots: rs, Store: cat, Worker: hdrWorker{}, Boards: bs, Runs: rn, Reports: rp, Outbox: ob, GroupKeyFile: filepath.Join(base, "group.key"),
		GroupHTTP: &http.Client{Transport: w, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	s.OutboxNow = clock.now
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	l := &pushLocal{s: s, ts: ts, base: base, root: root, folder: folder, runDir: "run-dir-LEAK-" + hex.EncodeToString(rnd[:]), wire: w, clock: clock}

	file := filepath.Join(root, "cube.fits")
	_ = os.WriteFile(file, []byte("SIMPLE  = T"), 0o644)
	var d catalog.Dataset
	json.NewDecoder(post(t, ts, file, nil).Body).Decode(&d)
	if d.ID != 7340001 {
		t.Fatalf("dataset id %d: the distinctive sequence did not take", d.ID)
	}
	l.dataset = d.ID
	view := map[string]any{"kind": "plane", "channel": 3, "colormap": "viridis", "stretch": "linear", "range": "percentile"}
	r, out := postJSON(t, ts.URL+"/api/figures", map[string]any{"datasetId": d.ID, "title": "Peak channel", "caption": "H I at 8 km/s", "view": view,
		"previewPng": base64.StdEncoding.EncodeToString(pngWithText(t, pictureMark))})
	if r.StatusCode != 201 {
		t.Fatalf("figure: %d %s", r.StatusCode, out)
	}
	var fig boards.Figure
	json.Unmarshal(out, &fig)
	l.figure = fig.ID
	run, err := rn.Save(workspace.Local, "local-owner", filepath.Join(root, "runs", l.runDir), runs.Parsed{Run: runs.Run{Name: "2026-11-08_a1f3c", Kind: "vgt",
		Params: []runs.Param{{Key: "block_size", Value: "32", Source: "config"}, {Key: "partition", Value: "LEAKPART", Source: "config"}, {Key: "notify", Value: "someone@LEAKMAIL.example.org", Source: "config"}}}},
		[]runs.Output{{Role: "psi", DatasetID: d.ID, Name: d.Name}})
	if err != nil {
		t.Fatal(err)
	}
	l.run = run.ID
	_, bout := postJSON(t, ts.URL+"/api/boards", map[string]any{"title": "For the group"})
	var b boards.Board
	json.Unmarshal(bout, &b)
	l.board = b.ID
	postJSON(t, ts.URL+"/api/boards/"+itoa(int(b.ID))+"/items", map[string]any{"figureId": fig.ID})
	body := "# Block size 32\n\nWe compare [[run:" + itoa(int(run.ID)) + "]].\n\n![[figure:" + fig.Revision + "]]\n\n[[run:" + itoa(int(run.ID)) + "]]"
	st, rb := callJSON(t, "POST", ts.URL+"/api/reports", map[string]any{"title": "Block size", "body": body, "author": "owner"})
	if st != 201 {
		t.Fatalf("report: %d %s", st, rb)
	}
	var rep reports.Report
	json.Unmarshal(rb, &rep)
	l.report = rep.ID
	return l
}

func (l *pushLocal) link(t *testing.T, origin, token string) {
	t.Helper()
	if st, b := callJSON(t, "POST", l.ts.URL+"/api/group-link", map[string]string{"origin": origin, "token": token}); st != 200 {
		t.Fatalf("link: %d %s", st, b)
	}
}

// preflight answers what pushing would send, with the hash queueing must name.
func (l *pushLocal) preflight(t *testing.T, kind string, id int64) (string, string) {
	t.Helper()
	st, b := callJSON(t, "POST", l.ts.URL+"/api/outbox/preflight", map[string]any{"kind": kind, "id": id})
	if st != 200 {
		t.Fatalf("preflight %s: %d %s", kind, st, b)
	}
	var p struct {
		Hash string `json:"hash"`
	}
	json.Unmarshal(b, &p)
	return p.Hash, string(b)
}

// enqueue previews the object and queues what the preview showed, as the push button does.
func (l *pushLocal) enqueue(t *testing.T, kind string, id int64) outboxEntryBody {
	t.Helper()
	h, _ := l.preflight(t, kind, id)
	st, b := callJSON(t, "POST", l.ts.URL+"/api/outbox", map[string]any{"kind": kind, "id": id, "hash": h})
	if st != 202 {
		t.Fatalf("enqueue %s: %d %s", kind, st, b)
	}
	var e outboxEntryBody
	json.Unmarshal(b, &e)
	return e
}

func (l *pushLocal) queue(t *testing.T) []outboxEntryBody {
	t.Helper()
	_, b := callJSON(t, "GET", l.ts.URL+"/api/outbox", nil)
	var q struct {
		Entries []outboxEntryBody `json:"entries"`
	}
	json.Unmarshal(b, &q)
	return q.Entries
}

// The end-to-end path: link, preflight, queue the four kinds, send, read them on the server as a member; then every byte that
// crossed is checked for what the ADR forbids, and the second push of the same content is the same row.
func TestPushEndToEndSendsOnlyFrozenReviewerSafeItems(t *testing.T) {
	g, ts, ps := pushGroup(t)
	full, _ := g.setupAdmin("ada")
	token := g.mintPush(full)
	l := newPushLocal(t, ts)

	if st, b := callJSON(t, "POST", l.ts.URL+"/api/group-link", map[string]string{"origin": strings.Replace(g.origin, "https", "http", 1), "token": token}); st != 422 {
		t.Fatalf("a plain http origin was accepted: %d %s", st, b)
	}
	l.link(t, g.origin, token)
	if fi, err := os.Stat(l.s.GroupKeyFile); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the token file must be 0600 beside the catalog: %v %v", fi, err)
	}
	_, state := callJSON(t, "GET", l.ts.URL+"/api/group-link", nil)
	if strings.Contains(string(state), token) || !strings.Contains(string(state), "Main workspace") {
		t.Fatalf("link state: %s", state)
	}
	var tokRows int
	_ = l.s.Store.DB().QueryRow(`SELECT COUNT(*) FROM group_link WHERE secret LIKE ? OR origin LIKE ?`, "%"+token+"%", "%"+token+"%").Scan(&tokRows)
	if tokRows != 0 {
		t.Fatal("the token is in the catalog")
	}

	runHash, pf := l.preflight(t, "run", l.run)
	if !strings.Contains(pf, "2 run setting(s)") || !strings.Contains(pf, `"fingerprint":"full"`) {
		t.Fatalf("preflight of the run: %s", pf)
	}
	if st, _ := callJSON(t, "POST", l.ts.URL+"/api/outbox", map[string]any{"kind": "run", "id": l.run}); st != 422 {
		t.Errorf("queued without a preview: %d", st)
	}
	if st, _ := callJSON(t, "POST", l.ts.URL+"/api/outbox", map[string]any{"kind": "run", "id": l.run, "hash": strings.Repeat("0", 64)}); st != 409 {
		t.Errorf("queued what the preview did not show: %d", st)
	}
	_ = runHash
	// a token the server refuses changes nothing here: the link, its instance and its token file stay
	before, _ := l.s.Outbox.Link()
	keyBefore, _ := os.ReadFile(l.s.GroupKeyFile)
	if st, b := callJSON(t, "POST", l.ts.URL+"/api/group-link", map[string]string{"origin": g.origin, "token": refusedToken}); st != 422 {
		t.Fatalf("a refused token: %d %s", st, b)
	}
	after, _ := l.s.Outbox.Link()
	keyAfter, _ := os.ReadFile(l.s.GroupKeyFile)
	if after.Instance != before.Instance || !bytes.Equal(keyBefore, keyAfter) {
		t.Fatal("a refused re-link replaced the working link")
	}
	for _, k := range []struct {
		kind string
		id   int64
	}{{"report", l.report}, {"board", l.board}, {"figure", l.figure}, {"run", l.run}} {
		if e := l.enqueue(t, k.kind, k.id); e.State != outbox.Waiting {
			t.Fatalf("%s queued as %+v", k.kind, e)
		}
	}
	l.s.OutboxPass(context.Background())
	for _, e := range l.queue(t) {
		if e.Status != "pushed" {
			t.Errorf("%s %q: %s", e.Kind, e.Title, e.Status)
		}
	}

	// unlinking deletes the token and empties the queue; linking again to the same server continues the same instance
	if st, _ := callJSON(t, "POST", l.ts.URL+"/api/group-link/unlink", map[string]any{}); st != 200 {
		t.Fatal("unlink")
	}
	if _, err := os.Stat(l.s.GroupKeyFile); !os.IsNotExist(err) {
		t.Fatal("the token file survived unlinking")
	}
	l.link(t, g.origin, token)
	if again, _ := l.s.Outbox.Link(); again.Instance != before.Instance {
		t.Fatal("linking again to the same server started a new instance")
	}
	for _, k := range []struct {
		kind string
		id   int64
	}{{"report", l.report}, {"board", l.board}, {"figure", l.figure}, {"run", l.run}} {
		l.enqueue(t, k.kind, k.id)
	}
	l.s.OutboxPass(context.Background())

	// what members see on the server
	list := decode[struct {
		Items []pushedSummary `json:"items"`
	}](t, g.do(req{path: "/api/pushed", cookie: full}))
	if len(list.Items) != 4 {
		t.Fatalf("pushed list: %+v", list.Items)
	}
	var figID, runID int64
	for _, it := range list.Items {
		if it.Kind == "run" {
			runID = it.ID
		}
		if it.PushedByName != "Ada" {
			t.Errorf("pushed by %q", it.PushedByName)
		}
		if it.Kind == "figure" {
			figID = it.ID
		}
	}
	var det struct {
		Item pushed.Detail `json:"item"`
	}
	json.Unmarshal(g.do(req{path: "/api/pushed/" + itoa(int(runID)), cookie: full}).Body.Bytes(), &det)
	if det.Item.Item.Omitted != 2 || !strings.Contains(string(det.Item.Item.Doc), "block_size") {
		t.Fatalf("the pushed run line does not say two settings were left out: %+v", det.Item.Item)
	}
	w := g.do(req{path: "/api/pushed/" + itoa(int(figID)), cookie: full})
	json.Unmarshal(w.Body.Bytes(), &det)
	if w.Code != 200 || len(det.Item.References) != 1 || det.Item.References[0].Match != "none" || det.Item.References[0].Name != "cube.fits" {
		t.Fatalf("figure detail: %d %s", w.Code, w.Body)
	}
	pic := g.do(req{path: "/api/pushed/" + itoa(int(figID)) + "/assets/" + det.Item.Item.Assets[0], cookie: full})
	if pic.Code != 200 || bytes.Contains(pic.Body.Bytes(), []byte(pictureMark)) {
		t.Fatalf("the picture on the server: %d, carries the metadata: %v", pic.Code, bytes.Contains(pic.Body.Bytes(), []byte(pictureMark)))
	}
	if g.do(req{path: "/api/pushed/" + itoa(int(figID)) + "/assets/" + strings.Repeat("0", 64), cookie: full}).Code != 404 {
		t.Error("a picture the item does not show was served")
	}
	// the fingerprints stay on the server: no answer carries them
	var sampled, fullHash string
	_ = g.cat.DB().QueryRow(`SELECT sampled_fp, full_sha256 FROM pushed_references WHERE item_id=?`, figID).Scan(&sampled, &fullHash)
	if fullHash == "" {
		t.Fatal("the figure's reference carries no full hash (a small file is hashed at registration)")
	}
	for _, path := range []string{"/api/pushed", "/api/pushed/" + itoa(int(figID))} {
		body := g.do(req{path: path, cookie: full}).Body.String()
		if strings.Contains(body, fullHash) || (sampled != "" && strings.Contains(body, sampled)) {
			t.Errorf("%s answers with a fingerprint", path)
		}
	}
	got := g.auditActions(g.mustAccount("ada").ID)
	n := 0
	for _, a := range got {
		if a == "push.item" {
			n++
		}
	}
	if n != 4 {
		t.Errorf("audit holds %d push.item entries, want 4: %v", n, got)
	}

	// The payload-leak test: every byte the local instance sent, and every answer, holds no path, folder, host, user name,
	// local id, secret, partition or account value, picture metadata, and the token only in the Authorization header.
	link, _ := l.s.Outbox.Link()
	hostname, _ := os.Hostname()
	banned := []string{l.base, l.root, l.folder, l.runDir, "LEAKPART", "LEAKMAIL", pictureMark, hex.EncodeToString(link.Secret), token, accounts.Hash(token), refusedToken,
		`"datasetId"`, `"path"`, `"scene"`, `"host`, "7340001", "7350001", "7360001", "7370001", "7380001"}
	if hostname != "" {
		banned = append(banned, hostname)
	}
	if u, err := user.Current(); err == nil && len(u.Username) >= 4 {
		banned = append(banned, "/"+u.Username)
	}
	ex := l.wire.exchanges()
	if len(ex) < 8 {
		t.Fatalf("only %d exchanges recorded", len(ex))
	}
	var all bytes.Buffer
	for _, e := range ex {
		all.Write(e.body)
	}
	for _, must := range []string{"Peak channel", "block_size", "cube.fits", "2026-11-08_a1f3c", `"kind":"report"`, `"kind":"board"`} {
		if !strings.Contains(all.String(), must) { // the control: what may cross did cross, so the scan below reads real payloads
			t.Fatalf("no request carried %q", must)
		}
	}
	for _, e := range ex {
		hdr := e.header.Clone()
		if a := hdr.Get("Authorization"); a != "Bearer "+token && a != "Bearer "+refusedToken {
			t.Errorf("%s %s without the token in Authorization", e.method, e.url)
		}
		hdr.Del("Authorization")
		var hb strings.Builder
		_ = hdr.Write(&hb)
		for _, part := range []string{e.url, hb.String(), string(e.body), string(e.reply)} {
			for _, bad := range banned {
				if strings.Contains(part, bad) {
					t.Errorf("%s %s carries %q: %.300s", e.method, e.url, bad, part)
				}
			}
		}
	}

	// the same content again is the same row: the queue answers the confirmed entry, and a raw resend stores nothing new
	sentBefore := len(l.wire.exchanges())
	if e := l.enqueue(t, "figure", l.figure); e.Status != "pushed" {
		t.Fatalf("an unchanged figure was queued again: %+v", e)
	}
	l.s.OutboxPass(context.Background())
	if len(l.wire.exchanges()) != sentBefore {
		t.Error("an unchanged, confirmed item was sent again")
	}
	var figBody []byte
	for _, e := range ex {
		if strings.HasSuffix(e.url, "/api/push/items") && bytes.Contains(e.body, []byte(`"kind":"figure"`)) {
			figBody = e.body
		}
	}
	c := l.s.httpForGroup()
	rq, _ := http.NewRequest("POST", g.origin+"/api/push/items", bytes.NewReader(figBody))
	rq.Header.Set("Authorization", "Bearer "+token)
	rq.Header.Set("Content-Type", "application/json")
	resp, err := c.Do(rq)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var sent pushwire.Push
	json.Unmarshal(figBody, &sent)
	if resp.StatusCode != 200 || ps.Count(workspace.Local, sent.Item.Hash) != 1 {
		t.Fatalf("the same hash twice: %d, %d rows", resp.StatusCode, ps.Count(workspace.Local, sent.Item.Hash))
	}

	// supersession: a changed caption is a new hash in the same lineage; the old item stays, superseded
	if st, b := callJSON(t, "POST", l.ts.URL+"/api/figures/"+itoa(int(l.figure))+"/text", map[string]string{"title": "Peak channel", "caption": "H I at 8 km/s, smoothed"}); st != 200 {
		t.Fatalf("caption: %d %s", st, b)
	}
	l.enqueue(t, "figure", l.figure)
	l.s.OutboxPass(context.Background())
	list = decode[struct {
		Items []pushedSummary `json:"items"`
	}](t, g.do(req{path: "/api/pushed", cookie: full}))
	if len(list.Items) != 4 {
		t.Fatalf("after a new version the list shows %d heads", len(list.Items))
	}
	w = g.do(req{path: "/api/pushed/" + itoa(int(figID)), cookie: full})
	json.Unmarshal(w.Body.Bytes(), &det)
	if len(det.Item.Lineage) != 2 || det.Item.SupersededBy == 0 || det.Item.Lineage[1].SupersededBy != 0 {
		t.Fatalf("the old figure is not superseded by the new one: %s", w.Body)
	}

	// a revoked token is refused at once, and the queue says so without retrying
	toks := decode[struct {
		Tokens []accounts.Token `json:"tokens"`
	}](t, g.do(req{path: "/api/account/tokens", cookie: full}))
	for _, tk := range toks.Tokens {
		g.do(req{method: "POST", path: "/api/account/tokens/" + tk.ID + "/revoke", body: `{}`, cookie: full})
	}
	if st, b := callJSON(t, "POST", l.ts.URL+"/api/figures/"+itoa(int(l.figure))+"/text", map[string]string{"title": "Peak channel", "caption": "third"}); st != 200 {
		t.Fatalf("caption: %d %s", st, b)
	}
	l.enqueue(t, "figure", l.figure)
	l.s.OutboxPass(context.Background())
	q := l.queue(t)
	if q[0].State != outbox.Refused || !strings.Contains(q[0].Status, "refused: ") || !strings.Contains(q[0].Status, "link this computer again") {
		t.Fatalf("after revocation: %+v", q[0])
	}
}

// The queue waits when the server cannot be reached, retries the same bytes later with a growing wait, and never blocks the app.
func TestPushQueueRetriesTheSameBytesAfterANetworkFailure(t *testing.T) {
	g, ts, ps := pushGroup(t)
	full, _ := g.setupAdmin("ada")
	token := g.mintPush(full)
	l := newPushLocal(t, ts)
	l.link(t, g.origin, token)
	l.wire.setDown(true)
	l.enqueue(t, "run", l.run)
	l.s.OutboxPass(context.Background())
	q := l.queue(t)
	if q[0].Status != "waiting: server unreachable" || q[0].Attempts != 1 || q[0].NextAt == nil || q[0].NextAt.Sub(l.clock.now()) != time.Minute {
		t.Fatalf("offline: %+v", q[0])
	}
	l.s.OutboxPass(context.Background()) // not due yet: nothing is tried
	if l.queue(t)[0].Attempts != 1 {
		t.Fatal("an item was retried before its wait ended")
	}
	l.clock.add(time.Minute)
	l.s.OutboxPass(context.Background())
	if q := l.queue(t); q[0].Attempts != 2 || q[0].NextAt.Sub(l.clock.now()) != 2*time.Minute {
		t.Fatalf("the wait did not double: %+v", q[0])
	}
	if backoff(10) != 15*time.Minute {
		t.Errorf("the wait is capped at 15 minutes, got %v", backoff(10))
	}
	// while it waits, the app answers at once
	start := time.Now()
	if st, _ := callJSON(t, "GET", l.ts.URL+"/api/outbox", nil); st != 200 || time.Since(start) > 2*time.Second {
		t.Fatal("the queue blocked the app")
	}
	queued, _ := l.s.Outbox.Due(l.clock.now().Add(time.Hour))
	l.wire.setDown(false)
	l.clock.add(2 * time.Minute)
	l.s.OutboxPass(context.Background())
	if q := l.queue(t); q[0].Status != "pushed" {
		t.Fatalf("back online: %+v", q[0])
	}
	var sent []byte
	for _, e := range l.wire.exchanges() {
		if strings.HasSuffix(e.url, "/api/push/items") {
			sent = e.body
		}
	}
	if !bytes.Equal(sent, queued[0].Body) {
		t.Error("the retry did not send the bytes that were frozen when the item was queued")
	}
	var p pushwire.Push
	json.Unmarshal(sent, &p)
	if ps.Count(workspace.Local, p.Item.Hash) != 1 {
		t.Error("the run is not on the server once")
	}

	// a lost answer: the server stored the item but the reply never came; the next pass asks first and uploads nothing twice
	l.enqueue(t, "board", l.board)
	due, _ := l.s.Outbox.Due(l.clock.now())
	c, _, _ := l.s.groupClient()
	for _, sha := range due[0].Assets {
		b, _ := l.s.Outbox.Asset(sha)
		if err := c.do("PUT", "/api/push/assets/"+sha, "application/octet-stream", b, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.do("POST", "/api/push/items", "application/json", due[0].Body, nil); err != nil {
		t.Fatal(err)
	}
	before := len(l.wire.exchanges())
	l.s.OutboxPass(context.Background())
	after := l.wire.exchanges()[before:]
	if len(after) != 1 || !strings.HasSuffix(after[0].url, "/api/push/have") || l.queue(t)[0].Status != "pushed" {
		t.Fatalf("after a lost answer the pass sent %d requests", len(after))
	}
}

// pushRaw sends a push request with a bearer token.
func (g *group) bearer(token string, r req) *httptest.ResponseRecorder {
	if r.hdr == nil {
		r.hdr = map[string]string{}
	}
	r.hdr["Authorization"] = "Bearer " + token
	r.hdr["Origin"] = "-"
	r.hdr["Sec-Fetch-Site"] = "-"
	return g.do(r)
}

func cleanPNG(t *testing.T, w, h int) ([]byte, string) {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b.Bytes())
	return b.Bytes(), hex.EncodeToString(sum[:])
}

func putAsset(g *group, token, sha string, body []byte) *httptest.ResponseRecorder {
	q := httptest.NewRequest("PUT", "/api/push/assets/"+sha, bytes.NewReader(body))
	q.Host = g.host
	q.RemoteAddr = "203.0.113.5:4000"
	q.Header.Set("Authorization", "Bearer "+token)
	q.Header.Set("Content-Type", "application/octet-stream")
	w := httptest.NewRecorder()
	g.h.ServeHTTP(w, q)
	return w
}

const testInstance = "0123456789abcdef0123456789abcdef"

func figureItem(t *testing.T, asset string, refs []pushwire.Reference, ids []pushwire.Identity, caption string) string {
	t.Helper()
	doc, _ := json.Marshal(pushedFigure{Title: "Peak", Caption: caption, Revision: "r1", Asset: asset, Width: 16, Height: 12, Provenance: json.RawMessage(`{"source":{"name":"cube.fits"}}`)})
	if refs == nil {
		refs, ids = []pushwire.Reference{}, []pushwire.Identity{}
	}
	b, _ := json.Marshal(pushwire.Push{Instance: testInstance, Item: pushwire.Item{Kind: "figure", Origin: strings.Repeat("a", 32), Title: "Peak", Doc: doc, Assets: []string{asset}, References: refs}, Identity: ids})
	return string(b)
}

// Scope: a push token reads nothing, acts only in its workspace, needs Researcher; a session never pushes; a demotion or a removal
// counts at the next request; an unbound push token (minted before binding) allows nothing.
func TestPushTokenIsNarrowAndBoundToItsWorkspace(t *testing.T) {
	g, _, _ := pushGroup(t)
	full, _ := g.setupAdmin("ada")
	ada := g.mustAccount("ada")
	other, err := g.acc.CreateWorkspace("Other lab", ada.ID, ada.ID)
	if err != nil {
		t.Fatal(err)
	}
	token := g.mintPush(full)
	have := `{"instance":"` + testInstance + `","hashes":[]}`
	if w := g.bearer(token, req{method: "POST", path: "/api/push/have", body: have}); w.Code != 200 {
		t.Fatalf("have: %d %s", w.Code, w.Body)
	}
	for _, p := range []string{"/api/pushed", "/api/datasets", "/api/boards", "/api/reports", "/api/members", "/api/capabilities", "/api/runs"} {
		if w := g.bearer(token, req{path: p}); w.Code/100 == 2 {
			t.Errorf("a push token read %s: %d", p, w.Code)
		}
	}
	if w := g.bearer(token, req{method: "POST", path: "/api/push/have", body: have, hdr: map[string]string{WorkspaceHeader: strconv.FormatInt(int64(other.ID), 10)}}); w.Code != 403 {
		t.Errorf("a push token acted in another workspace: %d", w.Code)
	}
	// an item pushed in the other workspace is not found through the token's
	res0, err := g.cat.DB().Exec(`INSERT INTO pushed_items(workspace_id,created_by,token_id,instance,kind,origin,hash,title,doc,bytes,pushed_at) VALUES(?,?,'t',?,'run',?,?,'OTHERWS','{}',2,1)`,
		other.ID, ada.ID, testInstance, strings.Repeat("d", 32), strings.Repeat("e", 64))
	if err != nil {
		t.Fatal(err)
	}
	otherItem, _ := res0.LastInsertId()
	if w := g.bearer(token, req{path: "/api/pushed/" + itoa(int(otherItem))}); w.Code != 404 {
		t.Errorf("a push token reached another workspace's item: %d", w.Code)
	}
	if w := g.do(req{method: "POST", path: "/api/push/have", body: have, cookie: full}); w.Code != 403 {
		t.Errorf("a session pushed: %d", w.Code)
	}
	if w := g.do(req{method: "POST", path: "/api/account/tokens", body: `{"name":"x","scopes":["push","read"]}`, cookie: full}); w.Code != 422 {
		t.Errorf("push mixed with read: %d", w.Code)
	}
	// Viewer cannot mint; Researcher can; demotion and removal count at once
	viewer := g.invite(workspace.Local, "viewer", "vic")
	if w := g.do(req{method: "POST", path: "/api/account/tokens", body: `{"name":"x","scopes":["push"]}`, cookie: viewer}); w.Code != 403 {
		t.Errorf("a Viewer minted a push token: %d %s", w.Code, w.Body)
	}
	res := g.invite(workspace.Local, "researcher", "rita")
	rtok := g.mintPush(res)
	rita := g.mustAccount("rita")
	if w := g.bearer(rtok, req{method: "POST", path: "/api/push/have", body: have}); w.Code != 200 {
		t.Fatalf("a Researcher's token: %d", w.Code)
	}
	if _, err := g.acc.SetRole(workspace.Local, rita.ID, "viewer"); err != nil {
		t.Fatal(err)
	}
	if w := g.bearer(rtok, req{method: "POST", path: "/api/push/have", body: have}); w.Code != 403 {
		t.Errorf("a demoted account's token still pushes: %d", w.Code)
	}
	if _, err := g.acc.SetRole(workspace.Local, rita.ID, "researcher"); err != nil {
		t.Fatal(err)
	}
	if _, err := g.acc.RemoveMember(workspace.Local, rita.ID); err != nil {
		t.Fatal(err)
	}
	if w := g.bearer(rtok, req{method: "POST", path: "/api/push/have", body: have}); w.Code != 403 {
		t.Errorf("a removed account's token still pushes: %d", w.Code)
	}
	// a push token from before binding
	if _, err := g.cat.DB().Exec(`UPDATE access_tokens SET workspace_id=NULL WHERE token_hash=?`, accounts.Hash(token)); err != nil {
		t.Fatal(err)
	}
	if w := g.bearer(token, req{method: "POST", path: "/api/push/have", body: have}); w.Code != 403 {
		t.Errorf("an unbound push token pushed: %d", w.Code)
	}
}

// Missing references, size limits, metadata, the whitelist of each kind, the daily limit and the quota.
func TestPushRefusalsSayWhy(t *testing.T) {
	g, _, ps := pushGroup(t)
	full, _ := g.setupAdmin("ada")
	token := g.mintPush(full)
	pic, sha := cleanPNG(t, 16, 12)

	w := g.bearer(token, req{method: "POST", path: "/api/push/items", body: figureItem(t, sha, nil, nil, "c")})
	if w.Code != 409 || !strings.Contains(w.Body.String(), sha) {
		t.Fatalf("an item whose picture is missing: %d %s", w.Code, w.Body)
	}
	if w := putAsset(g, token, sha, pic); w.Code != 201 {
		t.Fatalf("asset: %d %s", w.Code, w.Body)
	}
	if w := putAsset(g, token, sha, pic); w.Code != 200 {
		t.Fatalf("the same asset again: %d", w.Code)
	}
	if w := putAsset(g, token, strings.Repeat("0", 64), pic); w.Code != 422 {
		t.Errorf("an asset under the wrong hash: %d", w.Code)
	}
	big, bigSHA := cleanPNG(t, 4097, 2)
	if w := putAsset(g, token, bigSHA, big); w.Code != 413 {
		t.Errorf("a picture wider than 4096: %d %s", w.Code, w.Body)
	}
	huge := make([]byte, pushwire.MaxAssetBytes+1)
	hs := sha256.Sum256(huge)
	if w := putAsset(g, token, hex.EncodeToString(hs[:]), huge); w.Code != 413 {
		t.Errorf("a picture over 8 MiB: %d", w.Code)
	}
	meta := pngWithText(t, "someone")
	ms := sha256.Sum256(meta)
	if w := putAsset(g, token, hex.EncodeToString(ms[:]), meta); w.Code != 422 || !strings.Contains(w.Body.String(), "metadata") {
		t.Errorf("a picture with metadata: %d %s", w.Code, w.Body)
	}
	if w := g.bearer(token, req{method: "POST", path: "/api/push/items", body: figureItem(t, sha, nil, nil, "c")}); w.Code != 201 {
		t.Fatalf("figure: %d %s", w.Code, w.Body)
	}

	for name, body := range map[string]string{
		"a path in the provenance": strings.Replace(figureItem(t, sha, nil, nil, "c2"), `{"source":{"name":"cube.fits"}}`, `{"source":{"name":"/home/x/data/cube.fits"}}`, 1),
		"a path field":             strings.Replace(figureItem(t, sha, nil, nil, "c3"), `{"source":{"name":"cube.fits"}}`, `{"source":{"name":"cube.fits","localPath":"cube"}}`, 1),
		"an unknown field":         strings.Replace(figureItem(t, sha, nil, nil, "c4"), `"caption":`, `"datasetId":7,"caption":`, 1),
		"a folder in a name":       figureItem(t, sha, []pushwire.Reference{{Role: "source", Name: "data/cube.fits", SizeBytes: 1}}, []pushwire.Identity{{}}, "c5"),
		"pictures not shown":       strings.Replace(figureItem(t, sha, nil, nil, "c6"), `"assets":["`+sha+`"]`, `"assets":[]`, 1),
		"a wrong hash":             strings.Replace(figureItem(t, sha, nil, nil, "c7"), `"hash":""`, `"hash":"`+strings.Repeat("1", 64)+`"`, 1),
	} {
		if w := g.bearer(token, req{method: "POST", path: "/api/push/items", body: body}); w.Code != 422 {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	// a board of more than 200 panels
	items := make([]boards.FrozenItem, pushwire.MaxBoardItems+1)
	for i := range items {
		items[i] = boards.FrozenItem{Title: "p", PreviewSHA: sha, Width: 1, Height: 1, Provenance: json.RawMessage(`{}`)}
	}
	bdoc, _ := json.Marshal(pushedBoard{Title: "big", Items: items})
	bb, _ := json.Marshal(pushwire.Push{Instance: testInstance, Item: pushwire.Item{Kind: "board", Origin: strings.Repeat("b", 32), Title: "big", Doc: bdoc, Assets: []string{sha}, References: []pushwire.Reference{}}, Identity: []pushwire.Identity{}})
	if w := g.bearer(token, req{method: "POST", path: "/api/push/items", body: string(bb)}); w.Code != 422 {
		t.Errorf("a board of 201 panels: %d %s", w.Code, w.Body)
	}
	// a document over 4 MiB
	long := figureItem(t, sha, nil, nil, strings.Repeat("x", pushwire.MaxDocBytes))
	if w := g.bearer(token, req{method: "POST", path: "/api/push/items", body: long}); w.Code != 400 && w.Code != 422 {
		t.Errorf("a document over 4 MiB: %d", w.Code)
	}
	// the daily limit
	ada := g.mustAccount("ada")
	for i := 0; i < pushwire.DailyItems; i++ {
		if _, err := g.cat.DB().Exec(`INSERT INTO pushed_items(workspace_id,created_by,token_id,instance,kind,origin,hash,title,doc,bytes,pushed_at) VALUES(1,?,'t',?,'run',?,?,'x','{}',2,?)`,
			ada.ID, testInstance, strings.Repeat("c", 32), fmt.Sprintf("%064d", i), g.clock.now().Unix()); err != nil {
			t.Fatal(err)
		}
	}
	if w := g.bearer(token, req{method: "POST", path: "/api/push/items", body: figureItem(t, sha, nil, nil, "after the limit")}); w.Code != 429 || w.Header().Get("Retry-After") == "" {
		t.Errorf("past the daily limit: %d %s", w.Code, w.Body)
	}
	// the daily limit holds for pictures too, before they are stored
	pic3, sha3 := cleanPNG(t, 30, 30)
	if w := putAsset(g, token, sha3, pic3); w.Code != 429 {
		t.Errorf("a picture past the daily item limit: %d %s", w.Code, w.Body)
	}
	if _, err := g.cat.DB().Exec(`DELETE FROM pushed_items WHERE bytes=2`); err != nil {
		t.Fatal(err)
	}
	// the quota
	if err := g.acc.SetQuota(workspace.Local, 10); err != nil {
		t.Fatal(err)
	}
	other, otherSHA := cleanPNG(t, 20, 20)
	if w := putAsset(g, token, otherSHA, other); w.Code != 507 {
		t.Errorf("a picture over the workspace quota: %d %s", w.Code, w.Body)
	}
	if ps.AssetBytes(workspace.Local) != int64(len(pic)) {
		t.Errorf("asset bytes %d", ps.AssetBytes(workspace.Local))
	}
	var refused int
	_ = g.cat.DB().QueryRow(`SELECT COUNT(*) FROM audit WHERE action='push.refused'`).Scan(&refused)
	if refused < 5 {
		t.Errorf("refusals audited: %d", refused)
	}
}

// Fingerprint linking at each tier: equal full SHA-256 links (verified); equal size and sampled fingerprint with no full hash on
// both sides is probable and never linked for reading; a file name alone, or any difference, is "not on this server"; and a
// re-push with a full hash upgrades probable to verified without changing the item.
func TestPushedReferencesMatchByFingerprintOnly(t *testing.T) {
	g, _, _ := pushGroup(t)
	full, _ := g.setupAdmin("ada")
	token := g.mintPush(full)
	pic, sha := cleanPNG(t, 16, 12)
	putAsset(g, token, sha, pic)
	h := func(s string) string { x := sha256.Sum256([]byte(s)); return hex.EncodeToString(x[:]) }
	db := g.cat.DB()
	server := func(name string, size int64, sampled, fullSHA string) int64 {
		res, err := db.Exec(`INSERT INTO datasets(path,name,size_bytes,modified_at,added_at,header,workspace_id,created_by) VALUES(?,?,?,1,1,'{}',1,'x')`, "/srv/"+name, name, size)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		if _, err := db.Exec(`INSERT INTO dataset_locations(dataset_id,host_id,path,is_primary,size_bytes,modified_at,added_at,sampled_fp,full_sha256,workspace_id) VALUES(?,0,?,1,?,1,1,?,?,1)`,
			id, "/srv/"+name, size, sampled, fullSHA); err != nil {
			t.Fatal(err)
		}
		return id
	}
	verified := server("server-a.fits", 10, h("sa"), h("fa"))
	probable := server("server-b.fits", 20, h("sb"), "")
	server("same-name.fits", 30, h("sc"), h("fc"))
	sampled := "sampled/1"
	refs := []pushwire.Reference{
		{Role: "source", Name: "local-a.fits", SizeBytes: 10},
		{Role: "overlay", Name: "local-b.fits", SizeBytes: 20},
		{Role: "overlay", Name: "same-name.fits", SizeBytes: 30},
		{Role: "reliability", Name: "different.fits", SizeBytes: 10},
	}
	ids := []pushwire.Identity{
		{Algorithm: sampled, Sampled: h("sa"), Full: h("fa")},
		{Algorithm: sampled, Sampled: h("sb")},
		{Algorithm: sampled, Sampled: h("other")},
		{Algorithm: sampled, Sampled: h("sa"), Full: h("not-fa")},
	}
	w := g.bearer(token, req{method: "POST", path: "/api/push/items", body: figureItem(t, sha, refs, ids, "tiers")})
	if w.Code != 201 {
		t.Fatalf("push: %d %s", w.Code, w.Body)
	}
	id := decode[pushed.Result](t, w).ID
	read := func() []pushed.Reference {
		var d struct {
			Item pushed.Detail `json:"item"`
		}
		json.Unmarshal(g.do(req{path: "/api/pushed/" + itoa(int(id)), cookie: full}).Body.Bytes(), &d)
		return d.Item.References
	}
	got := read()
	want := []struct {
		match string
		ds    int64
	}{{"verified", verified}, {"probable", probable}, {"none", 0}, {"none", 0}}
	for i, r := range got {
		if r.Match != want[i].match || r.DatasetID != want[i].ds {
			t.Errorf("reference %d (%s): %s %d, want %s %d", i, r.Name, r.Match, r.DatasetID, want[i].match, want[i].ds)
		}
	}
	// the same item pushed again with a full hash for the probable reference: evidence added, the server copy has none yet, so still
	// probable; once the server's copy is fully hashed too, the next read finds them verified
	ids[1].Full = h("fb")
	if w := g.bearer(token, req{method: "POST", path: "/api/push/items", body: figureItem(t, sha, refs, ids, "tiers")}); w.Code != 200 {
		t.Fatalf("the same item again: %d %s", w.Code, w.Body)
	}
	if got := read(); got[1].Match != "probable" {
		t.Errorf("one full hash is not proof: %+v", got[1])
	}
	// another member replays the item with a full hash of their choosing for the reference the server could verify: ignored
	res := g.invite(workspace.Local, "researcher", "rita")
	ritaTok := g.mintPush(res)
	forged := append([]pushwire.Identity(nil), ids...)
	forged[2] = pushwire.Identity{Algorithm: sampled, Sampled: h("other"), Full: h("fc")}
	if w := g.bearer(ritaTok, req{method: "POST", path: "/api/push/items", body: figureItem(t, sha, refs, forged, "tiers")}); w.Code != 200 {
		t.Fatalf("replay: %d %s", w.Code, w.Body)
	}
	if got := read(); got[2].Match != "none" {
		t.Errorf("another member's replay changed the evidence: %+v", got[2])
	}
	if _, err := db.Exec(`UPDATE dataset_locations SET full_sha256=? WHERE dataset_id=?`, h("fb"), probable); err != nil {
		t.Fatal(err)
	}
	if got := read(); got[1].Match != "verified" || got[1].DatasetID != probable {
		t.Errorf("after the server copy gained a full hash: %+v", got[1])
	}
	var matches int
	_ = db.QueryRow(`SELECT COUNT(*) FROM audit WHERE action='push.match'`).Scan(&matches)
	if matches < 3 {
		t.Errorf("push.match audited %d times", matches)
	}
}

// Local mode is unchanged: the push routes do not exist there, and the local link and queue do not exist on the group server.
func TestPushRoutesBelongToTheirSide(t *testing.T) {
	dir := t.TempDir()
	cat, err := catalog.Open("file:" + filepath.Join(dir, "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer cat.Close()
	ps, _ := pushed.Open(cat.DB(), filepath.Join(dir, "pushed"))
	ob, _ := outbox.Open(cat.DB(), filepath.Join(dir, "outbox"))
	local := httptest.NewServer((&Server{Store: cat, Pushed: ps, Outbox: ob, GroupKeyFile: filepath.Join(dir, "group.key")}).Handler())
	defer local.Close()
	for _, p := range []string{"/api/push/have", "/api/push/items"} {
		if st, _ := callJSON(t, "POST", local.URL+p, map[string]any{}); st != 404 {
			t.Errorf("local mode answers %s with %d", p, st)
		}
	}
	if st, _ := callJSON(t, "GET", local.URL+"/api/pushed", nil); st != 404 {
		t.Errorf("local mode lists pushed items: %d", st)
	}
	if st, b := callJSON(t, "GET", local.URL+"/api/group-link", nil); st != 200 || !strings.Contains(string(b), `"linked":false`) {
		t.Errorf("an unlinked local instance: %d %s", st, b)
	}
	if st, b := callJSON(t, "POST", local.URL+"/api/outbox", map[string]any{"kind": "run", "id": 1, "hash": strings.Repeat("0", 64)}); st != 409 {
		t.Errorf("pushing while unlinked: %d %s", st, b)
	}
	g := newGroup(t)
	full, _ := g.setupAdmin("ada")
	g.s.Outbox, g.s.GroupKeyFile = ob, filepath.Join(dir, "group.key")
	g.h = g.s.Handler()
	for _, p := range []string{"/api/outbox", "/api/group-link"} {
		if w := g.do(req{path: p, cookie: full}); w.Code != 404 {
			t.Errorf("the group server answers %s with %d", p, w.Code)
		}
	}
}
