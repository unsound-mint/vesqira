package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/authz"
	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/offline"
	"github.com/unsound-mint/vesqira/services/api/internal/regions"
)

// "Prepare for offline" (ADR 0059). While the host answers, the owner chooses datasets (or a board's datasets), a channel range and
// the detail of the planes, and sees an estimate against a byte budget. Preparing then asks the real view, header, plane and
// spectrum handlers for exactly what the researcher chose and keeps each reply as the handler produced it (offline_serve.go
// capture), so a stored answer has the key, the validation and the bytes of a live one. Each item is listed as one of three:
// works offline, works partially, or needs the remote, with the reason. Nothing here copies a raw cube.

const (
	statusOffline = "offline"
	statusPartial = "partial"
	statusRemote  = "remote"

	defaultBudget int64 = 256 << 20
	maxChannels         = 4096 // channels one preparation may ask for per dataset; the byte budget is the real bound
	planeHeadSize int64 = 1024 // what a plane frame's JSON header weighs, for the estimate
)

type offlineItem struct {
	Kind          string `json:"kind"` // dataset | figure | job
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Status        string `json:"status"` // offline | partial | remote
	Reason        string `json:"reason"`
	EstimateBytes int64  `json:"estimateBytes"`
	KeptBytes     int64  `json:"keptBytes"`
}

type offlineRequest struct {
	DatasetIDs []int64 `json:"datasetIds"`
	BoardID    *int64  `json:"boardId"`
	Channels   *struct {
		From int `json:"from"`
		To   int `json:"to"`
	} `json:"channels"`
	Detail      string `json:"detail"` // "preview" (the reduced plane the viewer shows first) or "full"
	BudgetBytes int64  `json:"budgetBytes"`
}

type offlinePlan struct {
	Items         []offlineItem `json:"items"`
	EstimateBytes int64         `json:"estimateBytes"`
	BudgetBytes   int64         `json:"budgetBytes"`
	StoreCapBytes int64         `json:"storeCapBytes"`
	StoreUsed     int64         `json:"storeUsedBytes"`
	Fits          bool          `json:"fits"`
	Notes         []string      `json:"notes"`
	datasets      []*dsPlan
}

// dsPlan is what will be asked of one dataset on a host.
type dsPlan struct {
	d                 catalog.Dataset
	item              int // index into Items
	host, size, mtime int64
	width, height     int
	nchan             int
	axial             bool
	step              int
	preview, full     []int       // channels kept at the preview step, and at full resolution
	regions           [][2]string // spectrum queries of the saved regions, and the canonical description each is keyed by
	estimate          int64
}

// previewStepFor is the reduction the viewer asks for as a first look at a plane of this size (apps/web/src/api/planes.ts
// previewStep): about a quarter of a megapixel, never below 2. It is the same number so a stored preview is the one asked for.
func previewStepFor(w, h int) int {
	v := int(math.Ceil(math.Sqrt(float64(w) * float64(h) / 262144)))
	return min(64, max(2, v))
}

func planeBytes(w, h, step int) int64 {
	if step < 1 {
		step = 1
	}
	return int64((w+step-1)/step)*int64((h+step-1)/step)*4 + planeHeadSize
}

// offlineHandler is the four read routes prepare calls in process. It has no other route and no middleware: a request here never
// leaves the process.
func (s *Server) offlineHandler() http.Handler {
	mux := http.NewServeMux()
	s.route(mux, "GET /api/datasets/{id}/view", authz.DatasetRead, s.view)
	s.route(mux, "GET /api/datasets/{id}/header-cards", authz.DatasetRead, s.headerCards)
	s.route(mux, "GET /api/datasets/{id}/plane", authz.DatasetRead, s.plane)
	s.route(mux, "GET /api/datasets/{id}/spectrum", authz.DatasetRead, s.spectrum)
	return withLocalOwner(mux)
}

func fetch(ctx context.Context, h http.Handler, path string) (int, []byte) {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil).WithContext(ctx))
	return rec.Code, rec.Body.Bytes()
}

func errorText(body []byte, code int) string {
	var e struct{ Error string }
	if json.Unmarshal(body, &e) == nil && e.Error != "" {
		return e.Error
	}
	return "answered " + strconv.Itoa(code)
}

// spectrumQuery is the query string of a region's spectrum, and q the canonical description the spectrum route keys its reply by.
func spectrumQuery(sh regions.Shape) (query, q string) {
	if sh.Shape == "rect" {
		return fmt.Sprintf("shape=rect&x0=%d&y0=%d&x1=%d&y1=%d", sh.X0, sh.Y0, sh.X1, sh.Y1), fmt.Sprintf("rect,%d,%d,%d,%d", sh.X0, sh.Y0, sh.X1, sh.Y1)
	}
	return fmt.Sprintf("shape=circle&cx=%d&cy=%d&r=%d", sh.CX, sh.CY, sh.R), fmt.Sprintf("circle,%d,%d,%d", sh.CX, sh.CY, sh.R)
}

// freshEntries are a dataset's copies made from the given signature and not seen to be stale.
func (s *Server) freshEntries(id, host, size, mtime int64) []offline.Entry {
	es, _ := s.Offline.Entries(id)
	var out []offline.Entry
	for _, e := range es {
		if e.HostID == host && e.Size == size && e.MTime == mtime && !e.Stale() {
			out = append(out, e)
		}
	}
	return out
}

// classifyOffline says what a set of copies of one cube gives: all planes at full resolution with the view and header is "works
// offline" (browsing); anything less is partial; nothing is "needs the remote". The reason names what is kept and what is not.
func classifyOffline(nchan int, es []offline.Entry, extra string) (status, reason string) {
	var view, cards bool
	full := map[int]bool{}
	seenCh := map[int]bool{}
	for _, e := range es {
		switch e.Op {
		case "view":
			view = true
		case "cards":
			cards = true
		case "plane":
			seenCh[e.Channel] = true
			if e.Step <= 1 {
				full[e.Channel] = true
			}
		}
	}
	kept := coverage(es, nil)
	needs := []string{}
	allFull := nchan > 0 && len(full) >= nchan
	if !allFull {
		needs = append(needs, "full-resolution planes")
	}
	if len(seenCh) < nchan {
		needs = append(needs, "planes of other channels")
	}
	needs = append(needs, "spectra of regions not saved when this was prepared", "coordinate readouts", "analyses")
	switch {
	case len(es) == 0 || !view:
		status = statusRemote
		if kept == "" {
			kept = "nothing"
		}
	case allFull && cards:
		status = statusOffline
	default:
		status = statusPartial
	}
	reason = "Kept: " + kept + ". Needs the host: " + joinWords(needs) + "."
	if extra != "" {
		reason += " " + extra
	}
	return status, reason
}

func joinWords(ws []string) string {
	out := ""
	for i, w := range ws {
		switch {
		case i == 0:
			out = w
		case i == len(ws)-1:
			out += " and " + w
		default:
			out += ", " + w
		}
	}
	return out
}

// planOffline works out, without copying anything, what preparing would keep and what each item would then give. It talks to the
// host (to learn the cube's shape and that the file is the one catalogued), so it is a "while connected" step.
func (s *Server) planOffline(ctx context.Context, in offlineRequest) (offlinePlan, int, string) {
	if in.Detail == "" {
		in.Detail = "preview"
	}
	if in.Detail != "preview" && in.Detail != "full" {
		return offlinePlan{}, 422, "detail must be preview or full"
	}
	if in.Channels != nil && (in.Channels.From < 0 || in.Channels.To < in.Channels.From || in.Channels.To-in.Channels.From >= maxChannels) {
		return offlinePlan{}, 422, fmt.Sprintf("channels needs a first channel of 0 or more and a last one not before it, at most %d channels", maxChannels)
	}
	if in.BudgetBytes < 0 {
		return offlinePlan{}, 422, "budgetBytes cannot be negative"
	}
	capacity, used := s.Offline.Cap(), s.Offline.Used()
	p := offlinePlan{StoreCapBytes: capacity, StoreUsed: used, BudgetBytes: in.BudgetBytes, Notes: []string{}}
	if p.BudgetBytes == 0 {
		p.BudgetBytes = min(defaultBudget, max(0, capacity-used))
	}
	ids := append([]int64(nil), in.DatasetIDs...)
	seen := map[int64]bool{}
	if in.BoardID != nil {
		b, err := s.Boards.Board(*in.BoardID)
		if errors.Is(err, boards.ErrNotFound) {
			return p, 404, "board not found"
		}
		if err != nil {
			return p, 500, "internal error"
		}
		for _, it := range b.Items {
			switch {
			case it.Figure != nil:
				f := it.Figure
				item := offlineItem{Kind: "figure", ID: f.ID, Name: f.Title}
				if _, err := s.Boards.Preview(f.PreviewSHA); err != nil {
					item.Status, item.Reason = statusRemote, "The rendered picture is missing from Vesqira's data folder."
				} else {
					item.Status, item.Reason = statusOffline, "The rendered picture is stored by Vesqira, so nothing is copied. Redrawing it from the data works only for the planes kept below."
				}
				p.Items = append(p.Items, item)
				if f.DatasetID != nil && !seen[*f.DatasetID] {
					ids = append(ids, *f.DatasetID)
					seen[*f.DatasetID] = true
				}
			case it.Job != nil:
				p.Items = append(p.Items, offlineItem{Kind: "job", ID: it.Job.ID, Name: it.Job.Name, Status: statusPartial,
					Reason: "The last state Vesqira saw (" + it.Job.State + ") is kept; refreshing it needs the host."})
			}
		}
	}
	if len(ids) == 0 && in.BoardID == nil {
		return p, 422, "choose a board or at least one dataset"
	}
	done := map[int64]bool{}
	h := s.offlineHandler()
	for _, id := range ids {
		if done[id] {
			continue
		}
		done[id] = true
		d, err := s.Store.Get(id)
		if err != nil {
			p.Items = append(p.Items, offlineItem{Kind: "dataset", ID: id, Name: "dataset " + strconv.FormatInt(id, 10), Status: statusRemote, Reason: "This dataset is not in the catalog."})
			continue
		}
		dp, item := s.planDataset(ctx, h, d, in)
		item.Kind, item.ID, item.Name = "dataset", d.ID, d.Name
		p.Items = append(p.Items, item)
		if dp != nil {
			dp.item = len(p.Items) - 1
			p.datasets = append(p.datasets, dp)
			p.EstimateBytes += dp.estimate
		}
	}
	p.Fits = p.EstimateBytes <= p.BudgetBytes && p.EstimateBytes <= max(0, capacity-used)
	if p.EstimateBytes > p.BudgetBytes {
		p.Notes = append(p.Notes, "The estimate is over the budget: narrow the channel range, choose preview detail, or raise the budget.")
	} else if p.EstimateBytes > max(0, capacity-used) {
		p.Notes = append(p.Notes, "The offline store has less room than this needs: forget a copy or raise its limit.")
	}
	return p, 0, ""
}

// planDataset classifies one dataset and, when it is on a host that answers, says what to copy.
func (s *Server) planDataset(ctx context.Context, h http.Handler, d catalog.Dataset, in offlineRequest) (*dsPlan, offlineItem) {
	item := offlineItem{}
	if d.Kind == catalog.KindCatalog {
		item.Status, item.Reason = statusRemote, "A point catalog has no planes; its rows are read from the catalog file and are not kept offline in this version."
		return nil, item
	}
	eff, _, _, k, status, msg := s.chooseRead(ctx, d, true)
	if status != 0 {
		// The host does not answer: say what an earlier preparation still gives, and that it is not new.
		if es := s.copiesFor(d); len(es) > 0 {
			nchan := s.copyChannels(es)
			st, reason := classifyOffline(nchan, es, "The host is not reachable now ("+msg+"), so nothing new can be prepared.")
			item.Status, item.Reason = st, reason
			return nil, item
		}
		item.Status, item.Reason = statusRemote, msg+". Prepare again when the host is reachable."
		return nil, item
	}
	if eff.HostID == 0 {
		item.Status, item.Reason = statusOffline, "The file is on this computer: it needs no offline copy, and nothing was copied."
		return nil, item
	}
	code, body := fetch(ctx, h, "/api/datasets/"+strconv.FormatInt(d.ID, 10)+"/view")
	var v struct{ Width, Height, Channels int }
	if code != 200 || json.Unmarshal(body, &v) != nil || v.Width <= 0 || v.Height <= 0 || v.Channels <= 0 {
		item.Status, item.Reason = statusRemote, "The cube's layout could not be read: "+errorText(body, code)+"."
		return nil, item
	}
	dp := &dsPlan{d: d, host: k.host, size: k.size, mtime: k.mtime, width: v.Width, height: v.Height, nchan: v.Channels,
		axial: d.Interpretation != nil && d.Interpretation.Kind == "axial_orientation"}
	dp.step = previewStepFor(v.Width, v.Height)
	notes := ""
	if dp.axial {
		// block means of angles are wrong: an orientation map is always kept whole (the plane route refuses a reduced one)
		dp.step = 1
		if in.Detail == "preview" {
			notes = "This is an orientation map: its planes are only kept at full resolution."
		}
	}
	if in.Channels != nil {
		for c := in.Channels.From; c <= in.Channels.To && c < v.Channels; c++ {
			switch {
			case dp.axial:
				dp.full = append(dp.full, c)
			case in.Detail == "full":
				dp.full = append(dp.full, c)
				dp.preview = append(dp.preview, c)
			default:
				dp.preview = append(dp.preview, c)
			}
		}
	}
	dp.estimate = 32<<10 + 64<<10 // the view and the header cards
	dp.estimate += int64(len(dp.full)) * planeBytes(v.Width, v.Height, 1)
	if !dp.axial {
		dp.estimate += int64(len(dp.preview)) * planeBytes(v.Width, v.Height, dp.step)
	}
	if s.Regions != nil && !dp.axial {
		if rs, err := s.Regions.List(d.ID); err == nil {
			for _, r := range rs {
				if r.Shape.Shape == "rect" || r.Shape.Shape == "circle" {
					qs, q := spectrumQuery(r.Shape)
					dp.regions = append(dp.regions, [2]string{qs, q})
					dp.estimate += int64(v.Channels)*20 + 4<<10
				}
			}
		}
	}
	// What this preparation would leave: what is kept already for this very file plus what it adds.
	virtual := s.freshEntries(d.ID, k.host, k.size, k.mtime)
	add := func(op string, ch, step int, q string) {
		virtual = append(virtual, offline.Entry{Key: offline.Key{Op: op, Channel: ch, Step: step, Q: q}})
	}
	add("view", -1, 0, "")
	add("cards", -1, 0, "")
	for _, c := range dp.full {
		add("plane", c, 0, "")
	}
	for _, c := range dp.preview {
		if !dp.axial {
			add("plane", c, dp.step, "")
		}
	}
	for _, r := range dp.regions {
		add("spectrum", -1, 0, r[1])
	}
	item.Status, item.Reason = classifyOffline(v.Channels, dedupe(virtual), notes)
	item.EstimateBytes = dp.estimate
	return dp, item
}

// dedupe drops virtual entries that repeat a copy already counted (existing copies of this file plus the ones to be made).
func dedupe(es []offline.Entry) []offline.Entry {
	type key struct {
		op       string
		ch, step int
		q        string
	}
	seen := map[key]bool{}
	var out []offline.Entry
	for _, e := range es {
		if k := (key{e.Op, e.Channel, e.Step, e.Q}); !seen[k] {
			seen[k] = true
			out = append(out, e)
		}
	}
	return out
}

// copiesFor are the copies made from any signature the catalog currently holds for the dataset, not seen to be stale.
func (s *Server) copiesFor(d catalog.Dataset) []offline.Entry {
	o := s.offlineFor(d)
	if o == nil {
		return nil
	}
	var out []offline.Entry
	for _, c := range o.cands {
		out = append(out, s.freshEntries(d.ID, c.host, c.size, c.mtime)...)
	}
	return out
}

// copyChannels is the number of channels of the cube the copies were made from, read from the kept view.
func (s *Server) copyChannels(es []offline.Entry) int {
	for _, e := range es {
		if e.Op != "view" {
			continue
		}
		if _, head, _, err := s.Offline.Get(e.Key); err == nil {
			var v struct{ Channels int }
			if json.Unmarshal(head, &v) == nil {
				return v.Channels
			}
		}
	}
	return 0
}

// ---- the preparation run ----

type offlineJob struct {
	mu         sync.Mutex
	State      string // running | done | cancelled | failed
	StartedAt  time.Time
	FinishedAt *time.Time
	Total      int
	Done       int
	Bytes      int64
	Current    string
	Items      []offlineItem
	Error      string
	cancel     context.CancelFunc
	finished   chan struct{}
}

type offlineRunner struct {
	pmu     sync.Mutex     // guards probing
	probing map[int64]bool // hosts being asked again in the background (probeHost)
	mu      sync.Mutex
	job     *offlineJob
}

func (s *Server) waitOffline() {
	s.offlineRun.mu.Lock()
	j := s.offlineRun.job
	s.offlineRun.mu.Unlock()
	if j != nil {
		<-j.finished
	}
}

// startPrepare plans again (the host may have changed its mind since the sheet was shown), refuses what does not fit, and runs.
func (s *Server) startPrepare(in offlineRequest) (int, string) {
	s.offlineRun.mu.Lock()
	defer s.offlineRun.mu.Unlock()
	if j := s.offlineRun.job; j != nil {
		j.mu.Lock()
		running := j.State == "running"
		j.mu.Unlock()
		if running {
			return 409, "another preparation is running; wait for it or cancel it"
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	p, code, msg := s.planOffline(ctx, in)
	if code != 0 {
		cancel()
		return code, msg
	}
	if !p.Fits {
		cancel()
		return 409, fmt.Sprintf("this needs about %d bytes; the budget is %d and the offline store has room for %d. Narrow the channel range, choose preview detail, or raise a limit",
			p.EstimateBytes, p.BudgetBytes, max(0, p.StoreCapBytes-p.StoreUsed))
	}
	j := &offlineJob{State: "running", StartedAt: time.Now().UTC(), Total: len(p.datasets), Items: p.Items, cancel: cancel, finished: make(chan struct{})}
	s.offlineRun.job = j
	go s.runPrepare(ctx, j, p)
	return 0, ""
}

func (s *Server) runPrepare(ctx context.Context, j *offlineJob, p offlinePlan) {
	defer close(j.finished)
	remaining := p.BudgetBytes
	h := s.offlineHandler()
	var fatal string
	for _, dp := range p.datasets {
		if ctx.Err() != nil || fatal != "" {
			break
		}
		j.mu.Lock()
		j.Current = dp.d.Name
		j.mu.Unlock()
		c := &offlineCapture{s: s, d: dp.d, at: time.Now().UTC(), budget: remaining}
		stop := s.prepareDataset(context.WithValue(ctx, captureKey{}, c), h, dp, c)
		remaining -= c.bytes
		var oc *offline.OverCap
		if errors.As(c.err, &oc) {
			fatal = c.err.Error()
		}
		es := s.freshEntries(dp.d.ID, dp.host, dp.size, dp.mtime)
		st, reason := classifyOffline(dp.nchan, es, stop)
		j.mu.Lock()
		it := &j.Items[dp.item]
		it.Status, it.Reason, it.KeptBytes = st, reason, c.bytes
		j.Done++
		j.Bytes += c.bytes
		j.mu.Unlock()
		s.Audit.RecordCtx(ctx, "offline copy prepared", "dataset", dp.d.ID, fmt.Sprintf("%d replies, %d bytes", c.copies, c.bytes))
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	now := time.Now().UTC()
	j.FinishedAt, j.Current = &now, ""
	switch {
	case fatal != "":
		j.State, j.Error = "failed", fatal
	case ctx.Err() != nil:
		j.State = "cancelled"
	default:
		j.State = "done"
	}
}

// prepareDataset asks the handlers for everything planned for one dataset and returns a sentence about why it stopped early, if it did.
func (s *Server) prepareDataset(ctx context.Context, h http.Handler, dp *dsPlan, c *offlineCapture) string {
	base := "/api/datasets/" + strconv.FormatInt(dp.d.ID, 10)
	// A newer file than the copies were made from replaces them: they would never be served, and would only take room.
	if err := s.Offline.Supersede(dp.d.ID, dp.host, dp.size, dp.mtime); err != nil {
		return "Older copies could not be removed: " + err.Error()
	}
	type call struct{ path, what string }
	calls := []call{{base + "/view", "the view"}, {base + "/header-cards", "the header"}}
	for _, q := range dp.regions {
		calls = append(calls, call{base + "/spectrum?" + q[0], "a region spectrum"})
	}
	for _, ch := range dp.preview {
		calls = append(calls, call{base + "/plane?channel=" + strconv.Itoa(ch) + "&step=" + strconv.Itoa(dp.step), "channel " + strconv.Itoa(ch)})
	}
	for _, ch := range dp.full {
		calls = append(calls, call{base + "/plane?channel=" + strconv.Itoa(ch), "channel " + strconv.Itoa(ch)})
	}
	for _, cl := range calls {
		if ctx.Err() != nil {
			return "Cancelled."
		}
		code, body := fetch(ctx, h, cl.path)
		if c.err != nil {
			var oc *offline.OverCap
			if errors.As(c.err, &oc) {
				return "Stopped: " + c.err.Error() + "."
			}
			if errors.Is(c.err, errBudget) {
				return "Stopped at the byte budget before " + cl.what + "."
			}
			return "Stopped: " + c.err.Error() + "."
		}
		if code == 200 {
			continue
		}
		if code == 502 || code == 504 || code == 503 {
			return "The host stopped answering at " + cl.what + " (" + errorText(body, code) + ")."
		}
		// a refusal of this one reply (a channel past the end, an unreadable region) leaves the rest to go on
	}
	return ""
}

var errBudget = errors.New("the byte budget was reached")

// ---- routes ----

func (s *Server) offlineOverview(w http.ResponseWriter, r *http.Request) {
	es, err := s.Offline.Entries(0)
	if err != nil {
		fail(w, 500, "internal error")
		return
	}
	type ds struct {
		ID       int64     `json:"id"`
		Name     string    `json:"name"`
		HostID   int64     `json:"hostId"`
		Copies   int       `json:"copies"`
		Bytes    int64     `json:"bytes"`
		TakenAt  time.Time `json:"takenAt"`
		Stale    bool      `json:"stale"`
		Coverage string    `json:"coverage"`
	}
	out := []ds{}
	byID := map[int64][]offline.Entry{}
	var order []int64
	for _, e := range es {
		if _, ok := byID[e.DatasetID]; !ok {
			order = append(order, e.DatasetID)
		}
		byID[e.DatasetID] = append(byID[e.DatasetID], e)
	}
	for _, id := range order {
		d, err := s.Store.Get(id)
		if err != nil {
			continue
		}
		row := ds{ID: id, Name: d.Name, HostID: d.HostID}
		for _, e := range byID[id] {
			row.Copies++
			row.Bytes += e.Bytes
			if row.TakenAt.IsZero() || e.TakenAt.After(row.TakenAt) {
				row.TakenAt = e.TakenAt
			}
			row.Stale = row.Stale || e.Stale()
		}
		row.Coverage = coverage(byID[id], func(e offline.Entry) bool { return !e.Stale() })
		out = append(out, row)
	}
	writeJSON(w, 200, map[string]any{"capBytes": s.Offline.Cap(), "usedBytes": s.Offline.Used(), "defaultBudgetBytes": defaultBudget, "datasets": out})
}

func (s *Server) offlinePlanRoute(w http.ResponseWriter, r *http.Request) {
	var in offlineRequest
	if !body(w, r, 64<<10, &in, true) {
		return
	}
	p, code, msg := s.planOffline(r.Context(), in)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	if p.Items == nil {
		p.Items = []offlineItem{}
	}
	writeJSON(w, 200, p)
}

func (s *Server) offlinePrepareRoute(w http.ResponseWriter, r *http.Request) {
	var in offlineRequest
	if !body(w, r, 64<<10, &in, true) {
		return
	}
	if code, msg := s.startPrepare(in); code != 0 {
		fail(w, code, msg)
		return
	}
	writeJSON(w, 202, s.prepareState())
}

func (s *Server) prepareState() map[string]any {
	s.offlineRun.mu.Lock()
	j := s.offlineRun.job
	s.offlineRun.mu.Unlock()
	if j == nil {
		return map[string]any{"state": "idle", "total": 0, "done": 0, "bytes": 0, "current": "", "items": []offlineItem{}, "error": ""}
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return map[string]any{"state": j.State, "startedAt": j.StartedAt, "finishedAt": j.FinishedAt, "total": j.Total, "done": j.Done, "bytes": j.Bytes,
		"current": j.Current, "items": append([]offlineItem{}, j.Items...), "error": j.Error}
}

func (s *Server) offlinePrepareStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, s.prepareState())
}

func (s *Server) offlinePrepareCancel(w http.ResponseWriter, r *http.Request) {
	s.offlineRun.mu.Lock()
	j := s.offlineRun.job
	s.offlineRun.mu.Unlock()
	if j != nil {
		j.cancel()
	}
	writeJSON(w, 200, s.prepareState())
}

func (s *Server) offlineSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		CapBytes int64 `json:"capBytes"`
	}
	if !body(w, r, 1<<10, &in, true) {
		return
	}
	if in.CapBytes < 0 || in.CapBytes > 1<<50 {
		fail(w, 422, "capBytes must be between 0 and 1 PiB")
		return
	}
	if err := s.Offline.SetCap(in.CapBytes); err != nil {
		fail(w, 500, "internal error")
		return
	}
	s.Audit.RecordCtx(r.Context(), "offline store limit set", "offline", 0, strconv.FormatInt(in.CapBytes, 10)+" bytes")
	writeJSON(w, 200, map[string]any{"capBytes": s.Offline.Cap(), "usedBytes": s.Offline.Used()})
}

func (s *Server) offlineForget(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DatasetID int64 `json:"datasetId"`
	}
	if !body(w, r, 1<<10, &in, true) {
		return
	}
	if in.DatasetID <= 0 {
		fail(w, 422, "datasetId is required")
		return
	}
	n, freed, err := s.Offline.Forget(in.DatasetID)
	if err != nil {
		fail(w, 500, "internal error")
		return
	}
	s.Audit.RecordCtx(r.Context(), "offline copy forgotten", "dataset", in.DatasetID, fmt.Sprintf("%d replies, %d bytes", n, freed))
	writeJSON(w, 200, map[string]any{"datasetId": in.DatasetID, "forgotten": n, "freedBytes": freed})
}
