package boards

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/workspace"
)

// Review rounds (ADR 0073). A round is one published snapshot of a board; a board's history is its snapshots in order
// (snapshots of a figure shared on its own have board_id 0 and are in no history). When a board is published again, the
// new snapshot embeds what changed since the round before it, computed once from the two frozen documents, so the reviewer
// listener never reads another snapshot to describe a change. Nothing here changes a snapshot that exists.

// Item states in a round's comparison with the one before.
const (
	StateNew       = "new"       // no figure of the earlier round matches it
	StateRedrawn   = "redrawn"   // the same figure drawn again: another revision, linked by the owner's "draw again"
	StateUnchanged = "unchanged" // the same revision (text may differ)
)

// Round is what a snapshot says about its place in its board's history.
type Round struct {
	Number int    `json:"number"`          // 1 for the first snapshot of the board
	Since  *Since `json:"since,omitempty"` // absent in round 1
}

// Since is the comparison with the previous round, frozen when the board was published.
type Since struct {
	Label string    `json:"label"` // the previous round's label
	At    time.Time `json:"at"`    // when the previous round was published
	// Items has one entry per item of the snapshot, in the same order.
	Items []ItemChange `json:"items"`
	// Removed are the previous round's items that this one no longer holds. RemovedCount survives when the previous round is no
	// longer available and the titles are withheld.
	Removed      []RemovedItem `json:"removed"`
	RemovedCount int           `json:"removedCount"`
	// Lines is the comparison as a flat list, in reading order.
	Lines []ChangeLine `json:"lines"`
	// Carried are comments on figures of the previous round, shown read-only on the same figure.
	Carried []CarriedComment `json:"carried"`
}

// ItemChange says how one item relates to the previous round.
type ItemChange struct {
	State   string   `json:"state"`
	Changed []string `json:"changed"` // title, caption, note, corrections, masks, access, state
	// Previous is the earlier version of a redrawn figure, or of an item whose text changed.
	Previous *PreviousItem `json:"previous,omitempty"`
}

// PreviousItem is the earlier round's version: its text and its picture, which is served only while that round's link is valid.
type PreviousItem struct {
	Title      string `json:"title"`
	Caption    string `json:"caption"`
	Note       string `json:"note"`
	PreviewSHA string `json:"previewSha,omitempty"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
}

// RemovedItem is an item of the previous round that this one lacks.
type RemovedItem struct {
	Title string `json:"title"`
}

// ChangeLine is one thing that changed. Kind is added, removed, redrawn, title, caption, note, corrections, masks, access or
// state. Detail is a sentence for corrections, masks, access and state.
type ChangeLine struct {
	Kind   string `json:"kind"`
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
}

// CarriedComment is a comment of an earlier round, copied (as text) onto the matching figure of this one. Round says which round it
// was made in. The anchor is kept only when the picture is unchanged: a mark placed on an earlier drawing says nothing about a new one.
type CarriedComment struct {
	ID             int64      `json:"id"`
	ParentID       int64      `json:"parentId"`
	Round          int        `json:"round"`
	FigureRevision string     `json:"figureRevision"` // the figure's revision in this snapshot
	Author         string     `json:"author"`
	FromOwner      bool       `json:"fromOwner"`
	Body           string     `json:"body"`
	Anchor         *Anchor    `json:"anchor,omitempty"`
	PictureChanged bool       `json:"pictureChanged"`
	CreatedAt      time.Time  `json:"createdAt"`
	ResolvedAt     *time.Time `json:"resolvedAt,omitempty"`
}

// RoundChanges is a round's comparison as the owner's history lists it (no comments, no pictures).
type RoundChanges struct {
	Since        time.Time    `json:"since"`
	SinceLabel   string       `json:"sinceLabel"`
	Lines        []ChangeLine `json:"lines"`
	Carried      int          `json:"carried"`
	RemovedCount int          `json:"removedCount"`
}

// PreviousStatus tells the reviewer whether the earlier round's picture can still be opened from this link.
type PreviousStatus struct {
	Available bool `json:"available"`
}

// compareRounds matches the items of two rounds and describes the differences. lineage maps a figure revision to the revision
// at the root of its redrawing chain ("" when unknown); with nil, only equal revisions match. Item matching: the same revision
// first, then the same lineage, in order; a run panel matches the earlier panel of the same name.
func compareRounds(prev, cur []FrozenItem, lineage func(rev string) string) (changes []ItemChange, prevOf []int, removed []int) {
	prevOf = make([]int, len(cur))
	used := make([]bool, len(prev))
	for i := range prevOf {
		prevOf[i] = -1
	}
	same := func(a, b FrozenItem) bool {
		if a.Kind == KindJob || b.Kind == KindJob {
			return a.Kind == b.Kind && a.Title == b.Title
		}
		return a.Revision != "" && a.Revision == b.Revision
	}
	for i, c := range cur {
		for j, p := range prev {
			if !used[j] && same(p, c) {
				used[j], prevOf[i] = true, j
				break
			}
		}
	}
	changes = make([]ItemChange, len(cur))
	for i := range changes {
		changes[i].Changed = []string{}
	}
	for i, c := range cur {
		if prevOf[i] >= 0 {
			changes[i].State = StateUnchanged
			continue
		}
		changes[i].State = StateNew
		if lineage == nil || c.Kind == KindJob {
			continue
		}
		root := lineage(c.Revision)
		for j, p := range prev {
			if !used[j] && p.Kind != KindJob && root != "" && lineage(p.Revision) == root {
				used[j], prevOf[i] = true, j
				changes[i].State = StateRedrawn
				break
			}
		}
	}
	for j := range prev {
		if !used[j] {
			removed = append(removed, j)
		}
	}
	for i, c := range cur {
		j := prevOf[i]
		if j < 0 {
			continue
		}
		p := prev[j]
		ch := &changes[i]
		if c.Title != p.Title {
			ch.Changed = append(ch.Changed, "title")
		}
		if c.Caption != p.Caption {
			ch.Changed = append(ch.Changed, "caption")
		}
		if c.Note != p.Note {
			ch.Changed = append(ch.Changed, "note")
		}
		if c.Access != p.Access {
			ch.Changed = append(ch.Changed, "access")
		}
		if c.Kind == KindJob && c.State != p.State {
			ch.Changed = append(ch.Changed, "state")
		}
		if ch.State == StateRedrawn {
			// only what the two frozen provenance documents record: the earlier round's catalog is not consulted
			corr, masks := provenanceChanges(p.Provenance, c.Provenance)
			if corr != "" {
				ch.Changed = append(ch.Changed, "corrections")
			}
			if masks != "" {
				ch.Changed = append(ch.Changed, "masks")
			}
		}
		if ch.State == StateRedrawn || len(ch.Changed) > 0 {
			ch.Previous = &PreviousItem{Title: p.Title, Caption: p.Caption, Note: p.Note, PreviewSHA: p.PreviewSHA, Width: p.Width, Height: p.Height}
			if ch.State == StateUnchanged {
				ch.Previous.PreviewSHA, ch.Previous.Width, ch.Previous.Height = "", 0, 0 // the same picture: nothing to fetch
			}
		}
	}
	return changes, prevOf, removed
}

// changeLines turns a comparison into the flat list the reader and the owner's history show.
func changeLines(prev, cur []FrozenItem, changes []ItemChange, prevOf, removed []int) []ChangeLine {
	out := []ChangeLine{}
	for i, c := range cur {
		ch := changes[i]
		switch ch.State {
		case StateNew:
			out = append(out, ChangeLine{Kind: "added", Title: c.Title})
		case StateRedrawn:
			out = append(out, ChangeLine{Kind: "redrawn", Title: c.Title})
		}
		for _, what := range ch.Changed {
			l := ChangeLine{Kind: what, Title: c.Title}
			p := prev[prevOf[i]]
			switch what {
			case "corrections":
				l.Detail, _ = provenanceChanges(p.Provenance, c.Provenance)
			case "masks":
				_, l.Detail = provenanceChanges(p.Provenance, c.Provenance)
			case "access":
				l.Detail = "access label " + labelName(p.Access) + " before, " + labelName(c.Access) + " now"
			case "state":
				l.Detail = "run " + p.State + " before, " + c.State + " now"
			}
			out = append(out, l)
		}
	}
	for _, j := range removed {
		out = append(out, ChangeLine{Kind: "removed", Title: prev[j].Title})
	}
	return out
}

func labelName(l string) string {
	if l == "" {
		return "none"
	}
	return l
}

// provenanceChanges compares what two frozen provenance documents record about corrections and masks, as sentences ("" when
// equal). Nothing is read from the catalog: a figure's provenance is pinned with it.
func provenanceChanges(a, b json.RawMessage) (corrections, masks string) {
	type doc struct {
		Corrections []map[string]any `json:"corrections"`
		Tiers       []struct {
			What string `json:"what"`
			Tier string `json:"tier"`
			Note string `json:"note"`
		} `json:"tiers"`
	}
	read := func(raw json.RawMessage) (corr, mk map[string]string) {
		corr, mk = map[string]string{}, map[string]string{}
		var d doc
		if json.Unmarshal(raw, &d) != nil {
			return
		}
		for _, c := range d.Corrections {
			what, _ := c["what"].(string)
			enc, _ := json.Marshal(c) // map keys marshal sorted: equal records encode equally
			corr[what] = string(enc)
		}
		for _, t := range d.Tiers {
			if strings.Contains(strings.ToLower(t.What), "mask") {
				mk[t.What] = t.Tier + "|" + t.Note
			}
		}
		return
	}
	ac, am := read(a)
	bc, bm := read(b)
	return diffSets("Corrections", ac, bc), diffSets("Masks", am, bm)
}

func diffSets(name string, a, b map[string]string) string {
	var parts []string
	keys := map[string]bool{}
	for k := range a {
		keys[k] = true
	}
	for k := range b {
		keys[k] = true
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, k := range sorted {
		av, inA := a[k]
		bv, inB := b[k]
		switch {
		case !inA:
			parts = append(parts, k+" added")
		case !inB:
			parts = append(parts, k+" removed")
		case av != bv:
			parts = append(parts, k+" changed")
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return name + ": " + clip(strings.Join(parts, ", "), 400)
}

// redact withholds what only the previous round's own link entitles a reader to: the earlier text and picture, the removed titles,
// the sentences that quote the earlier provenance, and the comments. What stays says only what this round holds and whether each item
// is new, redrawn or unchanged.
func (sn *Since) redact() {
	sn.Label = ""
	sn.Carried = []CarriedComment{}
	sn.Removed = []RemovedItem{}
	lines := make([]ChangeLine, 0, len(sn.Lines))
	for _, l := range sn.Lines {
		if l.Kind == "removed" {
			continue
		}
		l.Detail = ""
		lines = append(lines, l)
	}
	sn.Lines = lines
	for i := range sn.Items {
		sn.Items[i].Previous = nil
	}
}

// ---------- building a round ----------

type prevSnapshot struct {
	id      int64
	frozen  Frozen
	revoked bool
	expired bool
}

// previousSnapshot is the snapshot just before the one with id (or, with id 0, the newest) of the same board, none for a board's first
// and for a figure shared on its own. Snapshots are never deleted, so "just before" is stable.
func (s *Store) previousSnapshot(ws workspace.ID, boardID, before int64) (p prevSnapshot, n int, ok bool, err error) {
	if boardID == FigureShareBoard {
		return p, 0, false, nil
	}
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM snapshots WHERE board_id=? AND workspace_id=? AND (?=0 OR id<?)`, boardID, ws, before, before).Scan(&n); err != nil || n == 0 {
		return p, 0, false, err
	}
	var raw string
	var rv, exp int64
	err = s.db.QueryRow(`SELECT id,frozen,revoked,expires_at FROM snapshots WHERE board_id=? AND workspace_id=? AND (?=0 OR id<?) ORDER BY id DESC LIMIT 1`, boardID, ws, before, before).Scan(&p.id, &raw, &rv, &exp)
	if errors.Is(err, sql.ErrNoRows) {
		return p, n, false, nil
	}
	if err != nil {
		return p, n, false, err
	}
	if json.Unmarshal([]byte(raw), &p.frozen) != nil {
		return p, n, false, fmt.Errorf("snapshot %d holds a document that cannot be read", p.id)
	}
	p.revoked, p.expired = rv != 0, time.Now().Unix() >= exp
	return p, n, true, nil
}

// rootRevision follows the owner's "draw again" links from a figure back to the first drawing. The link lives in figures.identity and is
// owner-side (ADR 0042): it is read here when a round is built and never frozen, so no snapshot carries it.
func (s *Store) rootRevision(ws workspace.ID, rev string) string {
	if rev == "" {
		return ""
	}
	for depth := 0; depth < 32; depth++ {
		f, err := s.FigureByRevision(ws, rev)
		if err != nil {
			return rev
		}
		var id struct {
			RedrawnFrom *struct {
				Revision string `json:"revision"`
			} `json:"redrawnFrom"`
		}
		if json.Unmarshal(f.Identity, &id) != nil || id.RedrawnFrom == nil || id.RedrawnFrom.Revision == "" || id.RedrawnFrom.Revision == rev {
			return rev
		}
		rev = id.RedrawnFrom.Revision
	}
	return rev
}

// addRound sets fr.Round for a board's snapshot about to be made (or previewed). items are the live items fr.Items were frozen from.
func (s *Store) addRound(ws workspace.ID, boardID int64, fr *Frozen) error {
	if boardID == FigureShareBoard {
		return nil
	}
	p, n, ok, err := s.previousSnapshot(ws, boardID, 0)
	if err != nil {
		return err
	}
	fr.Round = &Round{Number: n + 1}
	if !ok {
		return nil
	}
	cache := map[string]string{}
	lineage := func(rev string) string {
		if v, ok := cache[rev]; ok {
			return v
		}
		cache[rev] = s.rootRevision(ws, rev)
		return cache[rev]
	}
	changes, prevOf, removed := compareRounds(p.frozen.Items, fr.Items, lineage)
	since := &Since{Label: p.frozen.Label, At: p.frozen.CreatedAt, Items: changes, Removed: []RemovedItem{}, RemovedCount: len(removed),
		Lines: changeLines(p.frozen.Items, fr.Items, changes, prevOf, removed), Carried: []CarriedComment{}}
	for _, j := range removed {
		since.Removed = append(since.Removed, RemovedItem{Title: p.frozen.Items[j].Title})
	}
	if !p.revoked { // a withdrawn round's comments are not copied into a new one
		cs, err := s.Comments(p.id)
		if err != nil {
			return err
		}
		since.Carried = carryComments(p.frozen, n, cs, fr.Items, changes, prevOf)
	}
	if p.revoked {
		since.redact()
	}
	fr.Round.Since = since
	return nil
}

// carryComments picks the comments to show on the matching figures of the next round: those made on the previous round, and those it
// carried from earlier ones.
func carryComments(prev Frozen, prevRound int, own []Comment, cur []FrozenItem, changes []ItemChange, prevOf []int) []CarriedComment {
	out := []CarriedComment{}
	taken := map[string]bool{} // a revision on the board twice carries its comments once
	for i, c := range cur {
		j := prevOf[i]
		if j < 0 || c.Revision == "" || taken[prev.Items[j].Revision] {
			continue
		}
		taken[prev.Items[j].Revision] = true
		rev, changed := prev.Items[j].Revision, changes[i].State != StateUnchanged
		var older []CarriedComment
		if prev.Round != nil && prev.Round.Since != nil {
			older = prev.Round.Since.Carried
		}
		add := func(cc CarriedComment) {
			cc.FigureRevision, cc.PictureChanged = c.Revision, cc.PictureChanged || changed
			if cc.PictureChanged {
				cc.Anchor = nil
			}
			if len(out) < MaxCommentsPS {
				out = append(out, cc)
			}
		}
		for _, o := range older {
			if o.FigureRevision == rev {
				add(o)
			}
		}
		for _, o := range own {
			if o.FigureRevision == rev {
				add(CarriedComment{ID: o.ID, ParentID: o.ParentID, Round: prevRound, Author: o.Author, FromOwner: o.FromOwner, Body: o.Body, Anchor: o.Anchor, CreatedAt: o.CreatedAt, ResolvedAt: o.ResolvedAt})
			}
		}
	}
	return out
}

// previousOf finds the round before the snapshot with this id, from the snapshot's own board and workspace (the reviewer listener
// has only the token, which resolved to this id).
func (s *Store) previousOf(token string, id int64) (prevSnapshot, bool, error) {
	var boardID int64
	var ws workspace.ID
	// a capability lookup: the token that resolved to this snapshot names its workspace
	if err := s.db.QueryRow(`SELECT board_id, workspace_id FROM snapshots WHERE id=? AND token_hash=?`, id, hashToken(token)).Scan(&boardID, &ws); err != nil {
		return prevSnapshot{}, false, err
	}
	p, _, ok, err := s.previousSnapshot(ws, boardID, id)
	return p, ok, err
}

// reviewRound answers whether the earlier round is still open to this reader. When it was revoked (or cannot be found), the earlier
// round's content is withheld from fr, which is the reader's copy. An expired earlier round keeps its frozen text, which this
// snapshot owns, and loses only its picture.
func (s *Store) reviewRound(token string, id int64, fr *Frozen) *PreviousStatus {
	if fr.Round == nil || fr.Round.Since == nil {
		return nil
	}
	p, ok, err := s.previousOf(token, id)
	if err != nil || !ok || p.revoked {
		fr.Round.Since.redact()
		return &PreviousStatus{}
	}
	return &PreviousStatus{Available: !p.expired}
}

// previousPicture reports whether sha is the earlier picture of a redrawn figure of this snapshot and the earlier round's link is
// still valid (not revoked, not expired).
func (s *Store) previousPicture(token string, id int64, fr Frozen, sha string) bool {
	if fr.Round == nil || fr.Round.Since == nil || sha == "" {
		return false
	}
	listed := false
	for _, it := range fr.Round.Since.Items {
		listed = listed || (it.Previous != nil && it.Previous.PreviewSHA == sha)
	}
	if !listed {
		return false
	}
	p, ok, err := s.previousOf(token, id)
	return err == nil && ok && !p.revoked && !p.expired
}

// roundChanges is a snapshot's comparison for the owner's history. A snapshot made before rounds has none stored: it is compared with
// the one before it by revision only (the redrawing link was not recorded then).
func roundChanges(fr Frozen, prev *Frozen) *RoundChanges {
	if fr.Round != nil && fr.Round.Since != nil {
		sn := fr.Round.Since
		return &RoundChanges{Since: sn.At, SinceLabel: sn.Label, Lines: sn.Lines, Carried: len(sn.Carried), RemovedCount: sn.RemovedCount}
	}
	if fr.Round != nil || prev == nil {
		return nil
	}
	changes, prevOf, removed := compareRounds(prev.Items, fr.Items, nil)
	return &RoundChanges{Since: prev.CreatedAt, SinceLabel: prev.Label, Lines: changeLines(prev.Items, fr.Items, changes, prevOf, removed), RemovedCount: len(removed)}
}
