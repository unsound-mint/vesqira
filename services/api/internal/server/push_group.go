package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif" // pushed pictures are PNG, JPEG or GIF, as published ones
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log"
	"net/http"
	"reflect"
	"regexp"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/authz"
	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/imgmeta"
	"github.com/unsound-mint/vesqira/services/api/internal/publish"
	"github.com/unsound-mint/vesqira/services/api/internal/pushed"
	"github.com/unsound-mint/vesqira/services/api/internal/pushwire"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// Push to the group server (ADR 0066 section 2), the server's side. A linked local instance, through a workspace-bound token
// with the push scope only (authz.PushUpload, never a session), asks which hashes are missing, uploads pictures, then one frozen
// item per request; members read what was pushed through their browser session (authz.PushedRead) and archive it under the
// ownership rule (authz.PushedArchive). Every route acts in the token's or the session's workspace, and every store query names it.
//
// The server decides an item's hash from what arrived, decodes its document strictly into the type its kind names, and refuses
// it unless re-encoding gives the same bytes: the type is the whitelist, as for review snapshots, and nothing rides along.
func (s *Server) pushRoutes(mux *http.ServeMux) {
	s.route(mux, "POST /api/push/have", authz.PushUpload, s.pushHave)
	s.route(mux, "PUT /api/push/assets/{sha}", authz.PushUpload, s.pushAsset)
	s.route(mux, "POST /api/push/items", authz.PushUpload, s.pushItem)
	s.route(mux, "GET /api/push/heads", authz.PushUpload, s.pushHeads)
	s.route(mux, "GET /api/pushed", authz.PushedRead, s.listPushed)
	s.route(mux, "GET /api/pushed/{id}", authz.PushedRead, s.getPushed)
	s.route(mux, "GET /api/pushed/{id}/assets/{sha}", authz.PushedRead, s.pushedAsset)
	s.route(mux, "POST /api/pushed/{id}/archive", authz.PushedArchive, s.archivePushed)
}

// pushAssetPath is the one route whose body is not JSON: groupOnly lets it through for a bearer request only.
const pushAssetPath = "/api/push/assets/"

func pushActor(r *http.Request, instance string) pushed.Pusher {
	a, _ := authz.From(r.Context())
	return pushed.Pusher{Account: a.ID, TokenID: a.TokenID, Instance: instance}
}

func (s *Server) pushRefused(r *http.Request, kind, reason string) {
	a, _ := authz.From(r.Context())
	s.audit(r.Context(), "push.refused", "pushed", 0, fmt.Sprintf("%s via token %s: %s", kind, a.TokenID, reason))
}

func (s *Server) pushHave(w http.ResponseWriter, r *http.Request) {
	var in pushwire.Have
	if !body(w, r, 128<<10, &in, true) {
		return
	}
	if !pushwire.ValidInstance(in.Instance) || len(in.Hashes) > pushwire.MaxHave {
		fail(w, 422, fmt.Sprintf("send the instance id and at most %d hashes", pushwire.MaxHave))
		return
	}
	for _, h := range in.Hashes {
		if !pushwire.ValidSHA(h) {
			fail(w, 422, "every hash is a lowercase sha256")
			return
		}
	}
	missing, err := s.Pushed.Missing(wsOf(r), in.Hashes)
	if err != nil {
		log.Printf("push have: %v", err)
		fail(w, 500, "internal error")
		return
	}
	writeJSON(w, 200, map[string]any{"missing": missing})
}

var pictureMime = map[string]string{"png": "image/png", "jpeg": "image/jpeg", "gif": "image/gif"}

// pushAsset stores one picture under the SHA-256 of its bytes, after checking on arrival what the client already did: the hash,
// a complete PNG, JPEG or GIF of at most 4096 by 4096, and no metadata left. A file is untrusted and small files can decode to
// large pictures, so the checks run cheapest first and the one decode comes last: a picture the workspace holds is answered at
// once; the decoded size is bounded from the header (width x height x bytes per pixel of its colour model); metadata is found
// from the structure alone (imgmeta.IsClean); then at most two pictures decode at a time server-wide (503 beyond). Refused
// uploads count against the token (429 past pushRefusalLimit in pushRefusalWindow). A new picture counts towards the daily
// limit and the workspace's byte quota.
func (s *Server) pushAsset(w http.ResponseWriter, r *http.Request) {
	a, _ := authz.From(r.Context())
	refuse := func(code int, msg, reason string) {
		s.noteRefusal(a.TokenID)
		if reason != "" {
			s.pushRefused(r, "asset", reason)
		}
		fail(w, code, msg)
	}
	if s.refusedTooOften(a.TokenID) {
		w.Header().Set("Retry-After", "600")
		fail(w, 429, "too many refused uploads from this computer; wait ten minutes")
		return
	}
	sha := r.PathValue("sha")
	if !pushwire.ValidSHA(sha) {
		refuse(422, "the picture's name is the lowercase sha256 of its bytes", "")
		return
	}
	if r.Header.Get("Content-Type") != "application/octet-stream" {
		refuse(415, "send the picture as application/octet-stream", "")
		return
	}
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, pushwire.MaxAssetBytes+1))
	if err != nil || len(b) == 0 || len(b) > pushwire.MaxAssetBytes {
		refuse(413, "a picture is at most 8 MiB", "larger than 8 MiB")
		return
	}
	sum := sha256.Sum256(b)
	if hex.EncodeToString(sum[:]) != sha {
		refuse(422, "the picture's bytes do not match its sha256", "")
		return
	}
	ws := wsOf(r)
	if s.Pushed.HasAsset(ws, sha) { // the same bytes, already checked when they were stored
		writeJSON(w, 200, map[string]any{"sha": sha, "stored": false})
		return
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || pictureMime[format] == "" {
		refuse(422, "a picture is a PNG, JPEG or GIF", "")
		return
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > pushwire.MaxAssetPixels || cfg.Height > pushwire.MaxAssetPixels {
		refuse(413, fmt.Sprintf("a picture is at most %d by %d pixels", pushwire.MaxAssetPixels, pushwire.MaxAssetPixels), "larger than 4096 by 4096")
		return
	}
	if decodedBytes(cfg) > maxDecodedBytes {
		refuse(413, fmt.Sprintf("a picture decodes to at most %d MiB (4096 by 4096 at 8 bits per channel with alpha)", maxDecodedBytes>>20), "decodes too large")
		return
	}
	if clean, err := imgmeta.IsClean(b); err != nil || !clean {
		refuse(422, "the picture still carries metadata (EXIF, text, comments or trailing data); send it as cleaned for publishing", "picture carries metadata")
		return
	}
	slots := s.decodeSlots()
	select {
	case slots <- struct{}{}:
	default:
		w.Header().Set("Retry-After", "5")
		fail(w, 503, "the server is checking other pictures; try again in a moment")
		return
	}
	pictureDecodes.Add(1)
	_, _, err = image.Decode(bytes.NewReader(b))
	<-slots
	if err != nil {
		refuse(422, "the picture is not complete", "")
		return
	}
	created, err := s.Pushed.PutAsset(ws, a.ID, sha, pictureMime[format], b, s.pushQuota(ws))
	var limit pushed.LimitError
	switch {
	case errors.As(err, &limit):
		s.pushLimit(w, r, "asset", limit)
		return
	case err != nil:
		log.Printf("push asset: %v", err)
		fail(w, 500, "internal error")
		return
	}
	code := 200
	if created {
		code = 201
	}
	writeJSON(w, code, map[string]any{"sha": sha, "stored": created})
}

// pushLimit answers a refusal by a limit: 507 for the workspace's quota, 429 with Retry-After for the account's daily limit.
func (s *Server) pushLimit(w http.ResponseWriter, r *http.Request, kind string, limit pushed.LimitError) {
	s.pushRefused(r, kind, limit.Msg)
	if limit.Quota {
		fail(w, 507, limit.Msg)
		return
	}
	w.Header().Set("Retry-After", "3600")
	fail(w, 429, limit.Msg)
}

// pushQuota is the workspace's byte quota and what it keeps outside the pushed store (managed copies and derived files), for the
// pushed store to check against inside the transaction that stores.
func (s *Server) pushQuota(ws workspace.ID) pushed.Quota {
	wsp, err := s.MultiUser.Accounts.Workspace(ws)
	if err != nil {
		return pushed.Quota{Limit: 1} // a workspace that cannot be read takes nothing
	}
	return pushed.Quota{Limit: wsp.QuotaBytes, Outside: s.managedUse(ws)}
}

// Bounds on checking pictures (ADR 0066 limits; the security review of slice 4).
const (
	maxDecodedBytes   = 4096 * 4096 * 4 // 64 MiB: the largest picture at 8 bits per channel with alpha
	pictureDecodeSlot = 2               // pictures decoded at once, server-wide
	pushRefusalLimit  = 20              // refused uploads a token may make in pushRefusalWindow
	pushRefusalWindow = 10 * time.Minute
)

// pictureDecodes counts picture decodes (tests check that a refusal never decoded).
var pictureDecodes atomic.Int64

// decodedBytes is what decoding a picture allocates, from its header: width x height x the bytes per pixel of its colour model
// (an unknown model counts as the widest, 8).
func decodedBytes(cfg image.Config) int64 {
	bpp := int64(8)
	switch cfg.ColorModel {
	case color.GrayModel, color.AlphaModel:
		bpp = 1
	case color.Gray16Model, color.Alpha16Model:
		bpp = 2
	case color.YCbCrModel:
		bpp = 3
	case color.RGBAModel, color.NRGBAModel, color.CMYKModel:
		bpp = 4
	case color.RGBA64Model, color.NRGBA64Model:
		bpp = 8
	default:
		if _, ok := cfg.ColorModel.(color.Palette); ok {
			bpp = 1
		}
	}
	return int64(cfg.Width) * int64(cfg.Height) * bpp
}

// pushGuard holds the decode slots and the refused-upload counts of the push routes.
type pushGuard struct {
	slotsOnce sync.Once
	slots     chan struct{}
	mu        sync.Mutex
	refusals  map[string][]time.Time // token id -> times of refused uploads within the window
}

func (s *Server) decodeSlots() chan struct{} {
	s.pushGuard.slotsOnce.Do(func() { s.pushGuard.slots = make(chan struct{}, pictureDecodeSlot) })
	return s.pushGuard.slots
}

func (s *Server) recentRefusals(token string, now time.Time) []time.Time {
	var keep []time.Time
	for _, t := range s.pushGuard.refusals[token] {
		if now.Sub(t) < pushRefusalWindow {
			keep = append(keep, t)
		}
	}
	return keep
}

func (s *Server) noteRefusal(token string) {
	g := &s.pushGuard
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.refusals == nil {
		g.refusals = map[string][]time.Time{}
	}
	if len(g.refusals) > 10000 { // bounded: drop the tokens with nothing recent
		for k := range g.refusals {
			if len(s.recentRefusals(k, time.Now())) == 0 {
				delete(g.refusals, k)
			}
		}
	}
	g.refusals[token] = append(s.recentRefusals(token, time.Now()), time.Now())
}

func (s *Server) refusedTooOften(token string) bool {
	g := &s.pushGuard
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(s.recentRefusals(token, time.Now())) >= pushRefusalLimit
}

// pushItem stores one frozen item in one transaction.
func (s *Server) pushItem(w http.ResponseWriter, r *http.Request) {
	var in pushwire.Push
	if !body(w, r, pushwire.MaxDocBytes+(1<<20), &in, true) {
		return
	}
	if err := pushwire.Check(in); err != nil {
		s.pushRefused(r, in.Item.Kind, err.Error())
		fail(w, 422, err.Error())
		return
	}
	if err := checkPushedDoc(in.Item); err != nil {
		s.pushRefused(r, in.Item.Kind, err.Error())
		fail(w, 422, err.Error())
		return
	}
	sent := in.Item.Hash
	h, err := pushwire.ContentHash(in.Item)
	if err != nil {
		fail(w, 422, "the item cannot be hashed")
		return
	}
	if sent != "" && sent != h {
		s.pushRefused(r, in.Item.Kind, "hash does not match the content")
		fail(w, 422, "the item's hash does not match its content")
		return
	}
	in.Item.Hash = h
	res, err := s.Pushed.Put(wsOf(r), pushActor(r, in.Instance), in.Item, in.Identity, s.pushQuota(wsOf(r)))
	var miss pushed.MissingError
	var limit pushed.LimitError
	switch {
	case errors.As(err, &miss):
		s.pushRefused(r, in.Item.Kind, "missing pictures")
		failCode(w, 409, "missing", "upload the pictures this item shows first", map[string]any{"missing": miss.Hashes})
		return
	case errors.As(err, &limit):
		s.pushLimit(w, r, in.Item.Kind, limit)
		return
	case err != nil:
		log.Printf("push item: %v", err)
		fail(w, 500, "internal error")
		return
	}
	a, _ := authz.From(r.Context())
	if res.Stored {
		detail := fmt.Sprintf("%s %q %s via token %s, instance %s", in.Item.Kind, in.Item.Title, res.Hash, a.TokenID, in.Instance[:8])
		if res.Superseded != "" {
			detail += ", superseding " + res.Superseded
		}
		s.audit(r.Context(), "push.item", "pushed", res.ID, detail)
	} else if res.Superseded != "" {
		s.audit(r.Context(), "push.item", "pushed", res.ID, fmt.Sprintf("%s %q %s made current again via token %s, instance %s, superseding %s",
			in.Item.Kind, in.Item.Title, res.Hash, a.TokenID, in.Instance[:8], res.Superseded))
	}
	for _, m := range res.Matches {
		s.audit(r.Context(), "push.match", "pushed", res.ID, fmt.Sprintf("reference %d %q is %s", m.N, m.Name, m.Match))
	}
	code := 200
	if res.Stored {
		code = 201
	}
	writeJSON(w, code, res)
}

func (s *Server) pushHeads(w http.ResponseWriter, r *http.Request) {
	inst := r.URL.Query().Get("instance")
	if !pushwire.ValidInstance(inst) {
		fail(w, 422, "name the instance")
		return
	}
	a, _ := authz.From(r.Context())
	heads, err := s.Pushed.Heads(wsOf(r), a.ID, inst)
	if err != nil {
		log.Printf("push heads: %v", err)
		fail(w, 500, "internal error")
		return
	}
	name := ""
	if wsp, err := s.MultiUser.Accounts.Workspace(wsOf(r)); err == nil {
		name = wsp.Name
	}
	writeJSON(w, 200, map[string]any{"workspace": name, "heads": heads})
}

// pushedSummary is a list entry with the pusher's name.
type pushedSummary struct {
	pushed.Summary
	PushedByName string `json:"pushedByName"`
}

func (s *Server) accountName(cache map[string]string, id string) string {
	if n, ok := cache[id]; ok {
		return n
	}
	n := "former member"
	if acct, err := s.MultiUser.Accounts.Get(id); err == nil {
		n = acct.Name
	}
	cache[id] = n
	return n
}

func (s *Server) listPushed(w http.ResponseWriter, r *http.Request) {
	xs, err := s.Pushed.List(wsOf(r), r.URL.Query().Get("archived") == "1")
	if err != nil {
		log.Printf("pushed list: %v", err)
		fail(w, 500, "internal error")
		return
	}
	names := map[string]string{}
	out := make([]pushedSummary, 0, len(xs))
	for _, x := range xs {
		out = append(out, pushedSummary{x, s.accountName(names, x.PushedBy)})
	}
	writeJSON(w, 200, map[string]any{"items": out})
}

func (s *Server) getPushed(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	ws := wsOf(r)
	// matching is re-run on every read: a server dataset may have gained a fingerprint (a full hash) since the push
	if ch, err := s.Pushed.Rematch(ws, id); err == nil {
		for _, m := range ch {
			s.audit(r.Context(), "push.match", "pushed", id, fmt.Sprintf("reference %d %q is %s", m.N, m.Name, m.Match))
		}
	}
	d, err := s.Pushed.Get(ws, id)
	if errors.Is(err, pushed.ErrNotFound) {
		fail(w, 404, "not found")
		return
	}
	if err != nil {
		log.Printf("pushed get: %v", err)
		fail(w, 500, "internal error")
		return
	}
	names := map[string]string{}
	writeJSON(w, 200, map[string]any{"item": d, "pushedByName": s.accountName(names, d.PushedBy)})
}

func (s *Server) pushedAsset(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	mime, b, err := s.Pushed.Asset(wsOf(r), id, r.PathValue("sha"))
	if err != nil {
		fail(w, 404, "not found")
		return
	}
	w.Header().Set("Content-Type", mime)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, max-age=86400, immutable")
	_, _ = w.Write(b)
}

func (s *Server) archivePushed(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	a, _ := authz.From(r.Context())
	if err := s.Pushed.Archive(wsOf(r), id, a.ID); errors.Is(err, pushed.ErrNotFound) {
		fail(w, 404, "not found")
		return
	} else if err != nil {
		fail(w, 500, "internal error")
		return
	}
	s.audit(r.Context(), "push.archive", "pushed", id, "")
	writeJSON(w, 200, map[string]any{"id": id, "archived": true})
}

// ---- the documents of the four kinds ----

// pushedFigure is the frozen figure a figure item carries (ADR 0011): the picture's cleaned hash and size, title, caption, the
// figure's revision and its reviewer-safe provenance. Nothing else: no dataset id, scene or identity record.
type pushedFigure struct {
	Title      string          `json:"title"`
	Caption    string          `json:"caption"`
	Revision   string          `json:"revision"`
	Asset      string          `json:"asset"`
	Width      int             `json:"width"`
	Height     int             `json:"height"`
	Provenance json.RawMessage `json:"provenance"`
}

// pushedBoard is a board snapshot: its title and the ADR 0011 FrozenItem list (a job panel frozen to name and state), each
// figure's previewSha the hash of the cleaned picture that was uploaded.
type pushedBoard struct {
	Title string              `json:"title"`
	Items []boards.FrozenItem `json:"items"`
}

var reAssetRef = regexp.MustCompile(`asset:([0-9a-f]{64})`)

// strictInto decodes doc into v refusing unknown fields, and refuses a document whose re-encoding differs from what arrived.
func strictInto(doc json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(doc))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("the document does not have the shape of its kind: %v", err)
	}
	if dec.More() {
		return fmt.Errorf("the document holds more than one value")
	}
	again, err := json.Marshal(reflect.ValueOf(v).Elem().Interface())
	if err != nil {
		return err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, doc); err != nil || !bytes.Equal(compact.Bytes(), again) {
		return fmt.Errorf("the document carries something its kind does not hold, or is not in canonical form")
	}
	return nil
}

func sameSet(a []string, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for _, x := range a {
		if !b[x] {
			return false
		}
	}
	return true
}

func isJSONObject(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) >= 2 && t[0] == '{' && json.Valid(t)
}

// checkPushedDoc decodes the document by its kind and checks that the pictures it shows are exactly the item's assets.
func checkPushedDoc(it pushwire.Item) error {
	shown := map[string]bool{}
	switch it.Kind {
	case pushwire.KindFigure:
		var f pushedFigure
		if err := strictInto(it.Doc, &f); err != nil {
			return err
		}
		if !pushwire.ValidSHA(f.Asset) || f.Width < 1 || f.Height < 1 || !isJSONObject(f.Provenance) || f.Title == "" {
			return fmt.Errorf("a figure has a title, a picture with its size, and a provenance object")
		}
		shown[f.Asset] = true
	case pushwire.KindRun:
		var sum runs.Summary
		if err := strictInto(it.Doc, &sum); err != nil {
			return err
		}
		if sum.Name == "" {
			return fmt.Errorf("a run line has a name")
		}
	case pushwire.KindBoard:
		var b pushedBoard
		if err := strictInto(it.Doc, &b); err != nil {
			return err
		}
		if len(b.Items) < 1 || len(b.Items) > pushwire.MaxBoardItems {
			return fmt.Errorf("a board has 1 to %d panels", pushwire.MaxBoardItems)
		}
		for i, x := range b.Items {
			switch x.Kind {
			case boards.KindJob:
			case "", "figure":
				if !pushwire.ValidSHA(x.PreviewSHA) || !isJSONObject(x.Provenance) {
					return fmt.Errorf("panel %d: a figure panel shows an uploaded picture and its provenance", i)
				}
				shown[x.PreviewSHA] = true
			default:
				return fmt.Errorf("panel %d: unknown kind", i)
			}
		}
	case pushwire.KindReport:
		var rev publish.Revision
		if err := strictInto(it.Doc, &rev); err != nil {
			return err
		}
		if rev.Title == "" || rev.Number < 1 {
			return fmt.Errorf("a report revision has a title and a number")
		}
		for i, b := range rev.Blocks {
			switch b.Kind {
			case publish.BlockText:
				for _, m := range reAssetRef.FindAllStringSubmatch(b.Text, -1) {
					shown[m[1]] = true
				}
			case publish.BlockFigure:
				if b.Figure == nil || !pushwire.ValidSHA(b.Figure.Asset) || !isJSONObject(b.Figure.Provenance) {
					return fmt.Errorf("block %d: a figure block shows an uploaded picture and its provenance", i)
				}
				shown[b.Figure.Asset] = true
			case publish.BlockRun:
				if !isJSONObject(b.Run) {
					return fmt.Errorf("block %d: a run block carries the run line", i)
				}
			default:
				return fmt.Errorf("block %d: unknown kind", i)
			}
		}
	}
	if !sameSet(it.Assets, shown) {
		return fmt.Errorf("the item's pictures are not exactly the ones its document shows")
	}
	return nil
}

// assetList returns the sorted hashes of a set of pictures.
func assetList(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// bound reports whether a token's scopes are a linked instance's, and checks the account may hold them in ws: a push or relay
// token needs Researcher or higher there (a Viewer gets neither, ADR 0066 section 4). The role is checked again on every use.
func (s *Server) boundTokenRefusal(a authz.Actor, scopes []string) (bound bool, msg string) {
	for _, sc := range scopes {
		if authz.Scope(sc).Bound() {
			bound = true
		}
	}
	if !bound {
		return false, ""
	}
	if a.Workspace <= 0 {
		return true, "a computer is linked to one workspace: choose a workspace first"
	}
	if !a.Role.AtLeast(authz.RoleResearcher) {
		return true, "linking a computer needs the Researcher role or higher in this workspace"
	}
	return true, ""
}
