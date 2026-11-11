package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/derived"
	"github.com/unsound-mint/vesqira/services/api/internal/jobs"
	"github.com/unsound-mint/vesqira/services/api/internal/runs"
	"github.com/unsound-mint/vesqira/services/api/internal/scene"
)

// ---- figure provenance: built here from catalogued facts and a validated, whitelisted view description ----

type regionParam struct {
	Shape string `json:"shape"`
	X0    *int   `json:"x0,omitempty"`
	Y0    *int   `json:"y0,omitempty"`
	X1    *int   `json:"x1,omitempty"`
	Y1    *int   `json:"y1,omitempty"`
	CX    *int   `json:"cx,omitempty"`
	CY    *int   `json:"cy,omitempty"`
	R     *int   `json:"r,omitempty"`
}

// ViewParams is what the figure shows. Unknown keys are refused, so nothing else can ride along into a reviewer's view.
type ViewParams struct {
	Kind          string       `json:"kind"` // plane | spectrum | orientation
	Channel       *int         `json:"channel,omitempty"`
	SpectralValue *float64     `json:"spectralValue,omitempty"`
	SpectralUnit  string       `json:"spectralUnit,omitempty"`
	Region        *regionParam `json:"region,omitempty"`
	Colormap      string       `json:"colormap,omitempty"`
	Stretch       string       `json:"stretch,omitempty"`
	Range         string       `json:"range,omitempty"`
	Limits        []float64    `json:"limits,omitempty"`
	LimitsUnit    string       `json:"limitsUnit,omitempty"`
	ShowLines     *bool        `json:"showLines,omitempty"`
}

// unitText allows unit spellings such as m/s, Hz or Jy/beam and nothing that could carry markup or a path. It guards the
// spectral axis unit the browser reports; a figure's limit unit is guarded by limitsUnitOK.
var unitText = regexp.MustCompile(`^[A-Za-z0-9/*^. -]{0,16}$`)

// limitsUnitOK accepts the spelling of a figure's limit unit: what a header's BUNIT looks like, or anything a person can
// declare (catalog.ValidUnit). That it is the file's unit, or the declared one, is checked against the catalog later.
func limitsUnitOK(u string) bool {
	return u == "" || unitText.MatchString(u) || catalog.ValidUnit(u)
}

func oneOf(v string, ok ...string) bool {
	if v == "" {
		return true
	}
	for _, o := range ok {
		if v == o {
			return true
		}
	}
	return false
}

func (p ViewParams) valid() bool {
	if !oneOf(p.Kind, "plane", "spectrum", "orientation") || p.Kind == "" {
		return false
	}
	if !oneOf(p.Colormap, "viridis", "inferno", "gray", "twilight") || !oneOf(p.Stretch, "linear", "sqrt", "asinh") || !oneOf(p.Range, "percentile", "minmax", "custom") {
		return false
	}
	if !unitText.MatchString(p.SpectralUnit) || !limitsUnitOK(p.LimitsUnit) || len(p.Limits) > 2 {
		return false
	}
	if p.Region != nil && !oneOf(p.Region.Shape, "rect", "circle") {
		return false
	}
	return true
}

type safeAxis struct {
	Axis   int      `json:"axis"`
	Length int      `json:"length"`
	CType  *string  `json:"ctype"`
	CUnit  *string  `json:"cunit"`
	CRVal  *float64 `json:"crval"`
	CDelt  *float64 `json:"cdelt"`
}

type safeHeader struct {
	Axes   []safeAxis `json:"axes"`
	BUnit  *string    `json:"bunit"`
	RadeSy *string    `json:"radesys"`
	SpecSy *string    `json:"specsys"`
	Bitpix *int       `json:"bitpix"`
}

type safeInterpretation struct {
	Kind           string `json:"kind"`
	Unit           string `json:"unit"`
	DeclaredBy     string `json:"declaredBy"`
	HasReliability bool   `json:"hasReliabilityMap"`
}

// safeUnitDeclaration is the unit the author declared for a file with no BUNIT, said to be the author's.
type safeUnitDeclaration struct {
	Unit       string `json:"unit"`
	DeclaredBy string `json:"declaredBy"`
}

// Provenance is the reviewer-safe account of where a figure came from. It has no path and no dataset id.
type Provenance struct {
	Source struct {
		Name       string     `json:"name"`
		SizeBytes  int64      `json:"sizeBytes"`
		ModifiedAt time.Time  `json:"modifiedAt"`
		Identity   string     `json:"identity"`
		Header     safeHeader `json:"headerDeclaredByFile"`
	} `json:"source"`
	Interpretation *safeInterpretation `json:"interpretation,omitempty"`
	// UnitDeclaration is set when the brightness unit of the figure is one the author declared because the file has no BUNIT
	// (headerDeclaredByFile.bunit stays null: the header is only what the file said).
	UnitDeclaration *safeUnitDeclaration `json:"unitDeclaration,omitempty"`
	Nature          string               `json:"nature,omitempty"` // observed | simulated | constructed_control, as declared by the author
	Run             *runs.Summary        `json:"run,omitempty"`
	Derived         *derived.Summary     `json:"derived,omitempty"`
	View            ViewParams           `json:"view"`
	Statements      []string             `json:"statements"`
}

// checkAgainst validates the view against what the catalog knows about the file: a channel beyond every axis, a
// brightness unit the header does not declare, or a region far outside the image cannot be recorded as fact.
func (p ViewParams) checkAgainst(h safeHeader, declaredUnit string) bool {
	maxLen := 0
	for _, a := range h.Axes {
		if a.Length > maxLen {
			maxLen = a.Length
		}
	}
	if p.Channel != nil && (*p.Channel < 0 || *p.Channel >= maxLen) {
		return false
	}
	// the unit of the limits is the file's BUNIT, or, when the file has none, the one the author declared for it
	if p.LimitsUnit != "" && !(h.BUnit != nil && *h.BUnit == p.LimitsUnit) && !(h.BUnit == nil && declaredUnit != "" && declaredUnit == p.LimitsUnit) {
		return false
	}
	return true
}

func buildProvenance(d catalog.Dataset, p ViewParams, run *runs.Summary, der *derived.Summary) (json.RawMessage, error) {
	var pr Provenance
	pr.Source.Name, pr.Source.SizeBytes, pr.Source.ModifiedAt = d.Name, d.SizeBytes, d.ModifiedAt
	pr.Source.Identity = "a reference to the file by name, size and modification time; its bytes were not hashed"
	if err := json.Unmarshal(d.Header, &pr.Source.Header); err != nil {
		return nil, err
	}
	declaredUnit := ""
	if d.UnitDeclaration != nil && !d.UnitDeclaration.Stale && pr.Source.Header.BUnit == nil {
		declaredUnit = d.UnitDeclaration.Unit
	}
	if !p.checkAgainst(pr.Source.Header, declaredUnit) {
		return nil, errBadView
	}
	pr.View = p
	pr.Run = run
	pr.Nature = d.Nature
	pr.Derived = der
	pr.Statements = []string{
		"Header values are declared by the file and are not verified.",
		"The picture was drawn by the author's browser and the view settings are as reported by it; the server checked them against the catalogued header (channel range, brightness unit) but did not redraw the picture.",
		"Pixels are shown as float32; the author's viewer treats NaN, BLANK and infinite values as no value and never draws them as zero.",
	}
	if declaredUnit != "" {
		pr.UnitDeclaration = &safeUnitDeclaration{Unit: declaredUnit, DeclaredBy: "the author of this board, not the file"}
		pr.Statements = append(pr.Statements, "The brightness unit ("+declaredUnit+") was declared by the author of this board, not by the file: the file has no BUNIT.")
	}
	if d.Interpretation != nil {
		by := "the author of this board"
		if d.Interpretation.DeclaredBy != "user" {
			by = "the run import adapter (" + d.Interpretation.DeclaredBy + "), not the file"
			if d.Interpretation.DeclaredBy != "vgt-run-adapter" {
				by = "the analysis that made this map (" + d.Interpretation.DeclaredBy + "), not the file"
			}
		}
		pr.Interpretation = &safeInterpretation{Kind: d.Interpretation.Kind, Unit: d.Interpretation.Unit, DeclaredBy: by, HasReliability: d.Interpretation.ReliabilityID != nil}
		pr.Statements = append(pr.Statements, "The orientation unit and meaning were declared by "+by+", not by the file. Angles are axial (0° and 180° are the same orientation) and statistics use the doubled-angle method.")
	}
	switch d.Nature {
	case "constructed_control":
		pr.Statements = append(pr.Statements, "This is a constructed control (synthetic data built for a test), declared by the author. It is not an observation.")
	case "simulated":
		pr.Statements = append(pr.Statements, "This is simulated data, declared by the author. Results on a simulation are not validation on observations.")
	}
	if p.Region != nil {
		pr.Statements = append(pr.Statements, "Region values are means of the finite pixels inside the region; flagged channels keep their data; pixel scale is taken at the reference pixel.")
	}
	return json.Marshal(pr)
}

var errBadView = errors.New("view does not match the dataset")

// checkScene holds a figure's scene to the dataset and to the view description sent with it, so the two cannot tell
// different stories about one picture. In this version every layer is drawn from the figure's own dataset, and a spectrum
// figure (a plot, not a pane) has no scene.
func checkScene(sc scene.Scene, d catalog.Dataset, vp ViewParams) error {
	refuse := func(why string) error { return fmt.Errorf("%w: %s", scene.ErrInvalid, why) }
	if vp.Kind == "spectrum" {
		return refuse("a spectrum figure has no scene")
	}
	if sc.Base.DatasetID != d.ID {
		return refuse("the scene's base is not the figure's dataset")
	}
	for _, o := range sc.Overlays {
		if o.DatasetID != d.ID {
			return refuse("overlays from another dataset are not drawn in this version")
		}
		if o.Kind == "orientation" && d.Interpretation == nil {
			return refuse("orientation lines need a map declared axial")
		}
		// opacities from a reliability map are only reproducible when the figure names that map, and it is the declared one
		if o.Kind == "orientation" && o.Opacity == "reliability" && (o.ReliabilityID == nil || d.Interpretation.ReliabilityID == nil || *o.ReliabilityID != *d.Interpretation.ReliabilityID) {
			return refuse("orientation opacity from reliability must name the declared reliability map")
		}
	}
	disp := sc.Base.Display
	if (disp.Mapping == "axial") != (d.Interpretation != nil) || (disp.Mapping == "axial") != (vp.Kind == "orientation") {
		return refuse("the scene's mapping does not match the dataset's declaration or the figure kind")
	}
	if vp.Channel == nil || *vp.Channel != sc.Base.Channel {
		return refuse("the scene's channel is not the figure's")
	}
	if (vp.SpectralValue == nil) != (sc.Base.Spectral == nil) || (vp.SpectralValue != nil && (*vp.SpectralValue != sc.Base.Spectral.Value || vp.SpectralUnit != sc.Base.Spectral.Unit)) {
		return refuse("the scene's spectral value is not the figure's")
	}
	if disp.Mapping == "scalar" && (disp.Colormap != vp.Colormap || disp.Stretch != vp.Stretch || disp.Range != vp.Range) {
		return refuse("the scene's display settings are not the figure's")
	}
	// custom limits are the scene's own; limits another range gave are recomputed from the data, and the view records them
	if disp.Mapping == "scalar" && disp.Range == "custom" && (disp.LimitsUnit != vp.LimitsUnit || !sameFloats(disp.Limits, vp.Limits)) {
		return refuse("the scene's custom limits are not the figure's")
	}
	var active *scene.Region
	for _, o := range sc.Overlays {
		if o.Kind == "regions" && o.Active != nil {
			active = o.Active
		}
	}
	a, _ := json.Marshal(active)
	b, _ := json.Marshal(vp.Region)
	if string(a) != string(b) {
		return refuse("the scene's working region is not the figure's")
	}
	var h safeHeader
	if err := json.Unmarshal(d.Header, &h); err != nil {
		return refuse("the dataset header cannot be read")
	}
	maxLen := 0
	for _, ax := range h.Axes {
		maxLen = max(maxLen, ax.Length)
	}
	if c := sc.View.Centre; c != nil && (c.X > float64(maxLen) || c.Y > float64(maxLen)) {
		return refuse("the view centre is outside the image")
	}
	return nil
}

func sameFloats(a, b []float64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---- handlers ----

func (s *Server) boardErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, boards.ErrNotFound):
		fail(w, 404, "not found")
	case errors.Is(err, boards.ErrInvalid):
		fail(w, 422, err.Error())
	case errors.Is(err, boards.ErrConflict):
		fail(w, 409, err.Error())
	default:
		log.Printf("boards: %v", err)
		fail(w, 500, "internal error")
	}
}

func pathInt(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	v, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || v < 0 {
		fail(w, 400, "bad "+name)
		return 0, false
	}
	return v, true
}

func body(w http.ResponseWriter, r *http.Request, limit int64, v any, strict bool) bool {
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	dec := json.NewDecoder(r.Body)
	if strict {
		dec.DisallowUnknownFields()
	}
	if err := dec.Decode(v); err != nil {
		fail(w, 400, "invalid request body")
		return false
	}
	return true
}

func (s *Server) createFigure(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DatasetID  int64           `json:"datasetId"`
		Title      string          `json:"title"`
		Caption    string          `json:"caption"`
		View       json.RawMessage `json:"view"`
		Scene      json.RawMessage `json:"scene"`      // optional: the scene the picture was drawn from (ADR 0042)
		PreviewPNG string          `json:"previewPng"` // base64
	}
	if !body(w, r, 12<<20, &in, false) {
		return
	}
	var vp ViewParams
	dec := json.NewDecoder(bytes.NewReader(in.View))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&vp); err != nil || !vp.valid() {
		fail(w, 400, "the view description is not valid")
		return
	}
	png, err := base64.StdEncoding.DecodeString(in.PreviewPNG)
	if err != nil {
		fail(w, 400, "previewPng must be base64")
		return
	}
	d, code, msg := s.imageDataset(in.DatasetID)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	if _, _, status, m := s.authorize(r.Context(), d); status != 0 { // the figure records a source that is still what was registered
		fail(w, status, m)
		return
	}
	var run *runs.Summary
	if s.Runs != nil {
		run, _ = s.Runs.ForDataset(d.ID)
	}
	var sceneJSON json.RawMessage
	if len(in.Scene) > 0 && string(in.Scene) != "null" {
		sc, err := scene.Parse(in.Scene)
		if err == nil {
			err = checkScene(sc, d, vp)
		}
		if err != nil {
			fail(w, 400, err.Error())
			return
		}
		if sceneJSON, err = json.Marshal(sc); err != nil { // what is stored is the server's reading, never the client's bytes
			fail(w, 500, "internal error")
			return
		}
	}
	prov, err := buildProvenance(d, vp, run, s.derivedSummary(d.ID))
	if errors.Is(err, errBadView) {
		fail(w, 400, "the view description does not match the dataset")
		return
	}
	if err != nil {
		fail(w, 500, "internal error")
		return
	}
	f, err := s.Boards.CreateFigure(boards.NewFigure{DatasetID: d.ID, Title: in.Title, Caption: in.Caption, Provenance: prov, Scene: sceneJSON}, png)
	if err != nil {
		s.boardErr(w, err)
		return
	}
	writeJSON(w, 201, f)
}

func (s *Server) listFigures(w http.ResponseWriter, r *http.Request) {
	fs, err := s.Boards.Figures()
	if err != nil {
		s.boardErr(w, err)
		return
	}
	mode := archivedMode(r)
	shown := fs[:0]
	for _, f := range fs {
		if keepArchived(mode, f.Archived) {
			shown = append(shown, f)
		}
	}
	writeJSON(w, 200, struct {
		Figures []boards.Figure `json:"figures"`
	}{shown})
}

func (s *Server) figurePreview(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	f, err := s.Boards.Figure(id)
	if err != nil {
		s.boardErr(w, err)
		return
	}
	b, err := s.Boards.Preview(f.PreviewSHA)
	if err != nil {
		s.boardErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(b)
}

// figureDownload serves the published picture of a figure as a PNG that carries its title and provenance inside the
// file, so it stays attributed when it leaves the board.
func (s *Server) figureDownload(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	f, err := s.Boards.Figure(id)
	if err != nil {
		s.boardErr(w, err)
		return
	}
	b, err := s.Boards.Preview(f.PreviewSHA)
	if err != nil {
		s.boardErr(w, err)
		return
	}
	out, err := boards.EmbedText(b, [][2]string{{"Title", f.Title}, {"Description", f.Caption}, {"Provenance", string(f.Provenance)}, {"Software", "Vesqira; the picture is as drawn in the author's browser, not a re-rendering of the data"}})
	if err != nil {
		out = b
	}
	name := strings.Map(func(r rune) rune {
		if r < 128 && (r == '-' || r == '_' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return r
		}
		return '-'
	}, f.Title)
	if name == "" || strings.Trim(name, "-") == "" {
		name = "figure"
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`.png"`)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(out)
}

func (s *Server) figureText(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	var in struct{ Title, Caption string }
	if !body(w, r, 16<<10, &in, true) {
		return
	}
	if err := s.Boards.SetText(id, in.Title, in.Caption); err != nil {
		s.boardErr(w, err)
		return
	}
	f, _ := s.Boards.Figure(id)
	writeJSON(w, 200, f)
}

func (s *Server) listBoards(w http.ResponseWriter, r *http.Request) {
	bs, err := s.Boards.Boards()
	if err != nil {
		s.boardErr(w, err)
		return
	}
	mode := archivedMode(r)
	shown := bs[:0]
	for _, b := range bs {
		if keepArchived(mode, b.Archived) {
			shown = append(shown, s.boardView(r.Context(), b, false))
		}
	}
	writeJSON(w, 200, struct {
		Boards []boards.Board `json:"boards"`
	}{shown})
}

func (s *Server) createBoard(w http.ResponseWriter, r *http.Request) {
	var in struct{ Title string }
	if !body(w, r, 4<<10, &in, true) {
		return
	}
	b, err := s.Boards.CreateBoard(in.Title)
	if err != nil {
		s.boardErr(w, err)
		return
	}
	writeJSON(w, 201, b)
}

func (s *Server) getBoard(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	b, err := s.boardByID(r.Context(), id, true)
	if err != nil {
		s.boardErr(w, err)
		return
	}
	writeJSON(w, 200, b)
}

func (s *Server) boardAdd(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		FigureID *int64 `json:"figureId"`
		JobID    *int64 `json:"jobId"`
	}
	if !body(w, r, 4<<10, &in, true) {
		return
	}
	switch {
	case (in.FigureID == nil) == (in.JobID == nil):
		fail(w, 422, "give either figureId or jobId")
		return
	case in.JobID != nil:
		if _, err := s.Boards.Board(id); err != nil { // 404 for a missing board before judging the job
			s.boardErr(w, err)
			return
		}
		if s.Jobs == nil {
			fail(w, 422, "the job does not exist")
			return
		}
		if _, err := s.Jobs.Get(*in.JobID); err != nil {
			if errors.Is(err, jobs.ErrNotFound) {
				fail(w, 422, "the job does not exist")
			} else {
				s.jobErr(w, err)
			}
			return
		}
		if err := s.Boards.AddJobItem(id, *in.JobID); err != nil {
			s.boardErr(w, err)
			return
		}
	default:
		if err := s.Boards.AddItem(id, *in.FigureID); err != nil {
			s.boardErr(w, err)
			return
		}
	}
	b, _ := s.boardByID(r.Context(), id, false)
	writeJSON(w, 200, b)
}

func (s *Server) boardItem(op string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathInt(w, r, "id")
		if !ok {
			return
		}
		pos, ok := pathInt(w, r, "pos")
		if !ok {
			return
		}
		var err error
		switch op {
		case "remove":
			err = s.Boards.RemoveItem(id, int(pos))
		case "move":
			var in struct{ Delta int }
			if !body(w, r, 4<<10, &in, true) {
				return
			}
			if in.Delta != -1 && in.Delta != 1 {
				fail(w, 400, "delta must be -1 or 1")
				return
			}
			err = s.Boards.MoveItem(id, int(pos), in.Delta)
		case "note":
			var in struct{ Note string }
			if !body(w, r, 16<<10, &in, true) {
				return
			}
			err = s.Boards.SetNote(id, int(pos), in.Note)
		}
		if err != nil {
			s.boardErr(w, err)
			return
		}
		b, _ := s.boardByID(r.Context(), id, false)
		writeJSON(w, 200, b)
	}
}

// publishResponse is the body of POST /api/boards/{id}/publish.
type publishResponse struct {
	Snapshot   boards.Snapshot `json:"snapshot"`
	Token      string          `json:"token"`
	ReviewPath string          `json:"reviewPath"`
	Note       string          `json:"note"`
}

// publish freezes the board and returns the review token once. Only its hash is kept.
func (s *Server) publish(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		Label      string  `json:"label"`
		Question   string  `json:"question"`
		Hours      float64 `json:"hours"`
		Supersedes int64   `json:"supersedes"`
		Reason     string  `json:"reason"`
	}
	if !body(w, r, 8<<10, &in, true) {
		return
	}
	if in.Hours == 0 {
		in.Hours = 24 * 14
	}
	cur, err := s.Boards.Board(id)
	if err != nil {
		s.boardErr(w, err)
		return
	}
	sn, tok, err := s.Boards.PublishWith(id, boards.PublishOptions{Label: in.Label, Question: in.Question, TTL: time.Duration(in.Hours * float64(time.Hour)), Supersedes: in.Supersedes, Reason: in.Reason, Jobs: s.jobSnapshots(cur)})
	if err != nil {
		s.boardErr(w, err)
		return
	}
	s.Audit.Record("review published", "snapshot", sn.ID, "board "+strconv.FormatInt(id, 10)+", label: "+in.Label)
	writeJSON(w, 201, publishResponse{Snapshot: sn, Token: tok, ReviewPath: "/#/review/" + tok,
		Note: "Shown once. Anyone with this link can read the frozen board and comment; revoke it to end access."})
}

func (s *Server) snapshots(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	sn, err := s.Boards.Snapshots(id)
	if err != nil {
		s.boardErr(w, err)
		return
	}
	writeJSON(w, 200, struct {
		Snapshots []boards.Snapshot `json:"snapshots"`
	}{sn})
}

func (s *Server) revoke(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	if err := s.Boards.Revoke(id); err != nil {
		s.boardErr(w, err)
		return
	}
	s.Audit.Record("review revoked", "snapshot", id, "")
	writeJSON(w, 200, map[string]bool{"revoked": true})
}

func (s *Server) snapshotComments(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	cs, err := s.Boards.OwnerComments(id)
	if err != nil {
		s.boardErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"comments": cs})
}

// ownerReply records the author's reply inside a thread of their own snapshot.
func (s *Server) ownerReply(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		ParentID int64  `json:"parentId"`
		Body     string `json:"body"`
	}
	if !body(w, r, 16<<10, &in, true) {
		return
	}
	c, err := s.Boards.OwnerReply(id, boards.CommentInput{ParentID: in.ParentID, Body: in.Body})
	if err != nil {
		s.boardErr(w, err)
		return
	}
	s.Audit.Record("reply to a reviewer", "snapshot", id, "thread "+strconv.FormatInt(in.ParentID, 10))
	writeJSON(w, 201, c)
}

// resolveComment closes or reopens a thread. Reviewers cannot: whether a point is settled is the author's call.
func (s *Server) resolveComment(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		Resolved bool `json:"resolved"`
	}
	if !body(w, r, 1<<10, &in, true) {
		return
	}
	if err := s.Boards.SetResolved(id, in.Resolved); err != nil {
		s.boardErr(w, err)
		return
	}
	act := "thread reopened"
	if in.Resolved {
		act = "thread resolved"
	}
	s.Audit.Record(act, "comment", id, "")
	writeJSON(w, 200, map[string]bool{"resolved": in.Resolved})
}

// preflight shows what publishing would expose, without publishing.
func (s *Server) preflight(w http.ResponseWriter, r *http.Request) {
	id, ok := pathInt(w, r, "id")
	if !ok {
		return
	}
	cur, err := s.Boards.Board(id)
	if err != nil {
		s.boardErr(w, err)
		return
	}
	p, err := s.Boards.Preflight(id, r.URL.Query().Get("question"), s.jobSnapshots(cur))
	if err != nil {
		s.boardErr(w, err)
		return
	}
	writeJSON(w, 200, p)
}
