package server

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/unsound-mint/vesqira/services/api/internal/boards"
	"github.com/unsound-mint/vesqira/services/api/internal/subsets"
)

// A view of the sky panel saved as a board figure (ADR 0072). It is a figure of its own kind, "sky": no dataset of its own, and a
// description of what the picture shows instead of a scene. The description (sky-view/1) names the datasets whose footprints were
// drawn, so like a scene it is owner-only: it lives in figures.scene (which access.DatasetRefs reads, so the access labels of
// ADR 0069 govern publishing this figure like any other), is never copied into a snapshot or a report, and the reviewer-safe
// provenance holds the frame, the field, the survey and each footprint by file name and WCS basis, with no id.
//
// The survey image is context. The server never contacts the survey host: the browser drew the picture after the person turned the
// background on (ADR 0072), and the server only records which survey from a fixed list, with its name, provider and credit written
// by the server. Nothing here reads a pixel of the survey or turns it into a number.

// SkyViewSchema is the schema of the owner-only description in figures.scene.
const SkyViewSchema = "sky-view/1"

const maxSkyFootprints = 120

// skySurveys is the fixed list the sky panel offers (apps/web/src/sky/surveys.ts); a test keeps the two equal. A survey id outside it
// is refused, so a figure never claims a background the server cannot name.
var skySurveys = []skySurvey{
	{ID: "CDS/P/DSS2/color", Name: "DSS2 colour (optical)", Credit: "Digitized Sky Survey, STScI/NASA; coloured and healpixed by CDS"},
	{ID: "CDS/P/2MASS/color", Name: "2MASS colour J, H, K (near infrared)", Credit: "2MASS, University of Massachusetts and IPAC/Caltech"},
	{ID: "CDS/P/allWISE/color", Name: "AllWISE colour W4, W2, W1 (mid infrared)", Credit: "WISE, IPAC/NASA"},
	{ID: "CDS/P/PanSTARRS/DR1/color-z-zg-g", Name: "Pan-STARRS DR1 colour (optical)", Credit: "Pan-STARRS1 Science Consortium, via MAST/STScI"},
	{ID: "CDS/P/HI4PI/NHI", Name: "HI4PI H I column density", Credit: "HI4PI Collaboration (B. Winkel)"},
}

const skySurveyProvider = "CDS, Strasbourg"

type skySurvey struct {
	ID       string `json:"hipsId"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Credit   string `json:"credit"`
	// Role is always "context": the survey is not data from the author's files and is never sampled into a number.
	Role string `json:"role"`
}

func surveyOf(id string) (skySurvey, bool) {
	for _, sv := range skySurveys {
		if sv.ID == id {
			sv.Provider, sv.Role = skySurveyProvider, "context"
			return sv, true
		}
	}
	return skySurvey{}, false
}

// skyFootprintRecord is one drawn footprint. DatasetID is the dataset itself, or a subset's source: either way it is the dataset
// whose file the outline came from, so its access label applies.
type skyFootprintRecord struct {
	DatasetID     int64   `json:"datasetId"`
	SubsetID      int64   `json:"subsetId,omitempty"`
	SubsetVersion int     `json:"subsetVersion,omitempty"`
	Name          string  `json:"name"`
	FromFrame     string  `json:"fromFrame"`
	FromStatus    string  `json:"fromStatus"`
	FromBasis     string  `json:"fromBasis"`
	ExtentDeg     float64 `json:"extentDeg"`
}

type skyCentre struct {
	Lon float64 `json:"lon"`
	Lat float64 `json:"lat"`
}

type skyViewSpec struct {
	Schema     string               `json:"schema"`
	Frame      string               `json:"frame"`
	Centre     skyCentre            `json:"centre"`
	FovDeg     float64              `json:"fovDeg"`
	Survey     *skySurvey           `json:"survey"`
	Footprints []skyFootprintRecord `json:"footprints"`
}

// safeSkyView is the reviewer-safe account: the frame, the field, the survey, and each footprint by the file's name and how its
// WCS is known. No dataset id, no subset id.
type safeSkyView struct {
	Frame      string        `json:"frame"`
	Centre     skyCentre     `json:"centre"`
	FovDeg     float64       `json:"fovDeg"`
	Background *skySurvey    `json:"background"`
	Footprints []safeSkyFoot `json:"footprints"`
}

type safeSkyFoot struct {
	Name string `json:"name"`
	// Window is true when the outline is that of a window onto the file (a virtual subset), not of the whole image.
	Window bool   `json:"window"`
	Basis  string `json:"basis"`
}

type skyRef struct {
	Kind string `json:"kind"` // dataset | subset
	ID   int64  `json:"id"`
}

type skyFigureRequest struct {
	Title      string    `json:"title"`
	Caption    string    `json:"caption"`
	PreviewPNG string    `json:"previewPng"`
	Frame      string    `json:"frame"`
	Centre     skyCentre `json:"centre"`
	FovDeg     float64   `json:"fovDeg"`
	// Survey is the HiPS id of the background in the picture, absent when the picture is the plain grid.
	Survey     *string  `json:"survey"`
	Footprints []skyRef `json:"footprints"`
}

// captureWriter lets a route's handler answer into memory, so the figure route can read what the sky route says for each dataset.
type captureWriter struct {
	h    http.Header
	code int
	buf  strings.Builder
}

func (c *captureWriter) Header() http.Header         { return c.h }
func (c *captureWriter) WriteHeader(code int)        { c.code = code }
func (c *captureWriter) Write(b []byte) (int, error) { return c.buf.Write(b) }

// skyFootprintFor reads one footprint exactly as the panel does (the same route, so the same corrections, authorisation, window and
// cache) and returns the worker's reply, or the status and sentence the route gave.
func (s *Server) skyFootprintFor(r *http.Request, ref skyRef, frame string) (skyReply, int, string) {
	rr := r.Clone(r.Context())
	rr.URL = &url.URL{Path: "/", RawQuery: "frame=" + frame}
	if ref.Kind == "subset" {
		rr.SetPathValue("sid", fmt.Sprint(ref.ID))
	} else {
		rr.SetPathValue("id", fmt.Sprint(ref.ID))
	}
	cw := &captureWriter{h: http.Header{}, code: 200}
	s.skyFootprint(cw, rr)
	if cw.code != 200 {
		var e struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal([]byte(cw.buf.String()), &e)
		if e.Error == "" {
			e.Error = "could not be read"
		}
		return skyReply{}, cw.code, e.Error
	}
	var rep skyReply
	if json.Unmarshal([]byte(cw.buf.String()), &rep) != nil {
		return rep, 502, "the worker answered unexpectedly"
	}
	return rep, 200, ""
}

type skyReply struct {
	Footprint *struct {
		ExtentDeg float64 `json:"extent_deg"`
	} `json:"footprint"`
	Reason     *string `json:"reason"`
	FromFrame  string  `json:"from_frame"`
	FromStatus string  `json:"from_status"`
	FromBasis  string  `json:"from_basis"`
}

// createSkyFigure is POST /api/figures/sky.
func (s *Server) createSkyFigure(w http.ResponseWriter, r *http.Request) {
	var in skyFigureRequest
	if !body(w, r, 12<<20, &in, true) {
		return
	}
	if in.Frame != "icrs" && in.Frame != "galactic" {
		fail(w, 400, "frame must be icrs or galactic")
		return
	}
	fin := func(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
	if !fin(in.Centre.Lon) || !fin(in.Centre.Lat) || in.Centre.Lon < 0 || in.Centre.Lon >= 360 || in.Centre.Lat < -90 || in.Centre.Lat > 90 {
		fail(w, 400, "the centre must be a longitude in [0, 360) and a latitude in [-90, 90] degrees")
		return
	}
	if !fin(in.FovDeg) || in.FovDeg <= 0 || in.FovDeg > 180 {
		fail(w, 400, "the field of view must be between 0 and 180 degrees")
		return
	}
	var survey *skySurvey
	if in.Survey != nil {
		sv, ok := surveyOf(*in.Survey)
		if !ok {
			fail(w, 400, "that survey is not one the sky panel offers")
			return
		}
		survey = &sv
	}
	if len(in.Footprints) == 0 || len(in.Footprints) > maxSkyFootprints {
		fail(w, 400, "a sky view needs between 1 and 120 footprints")
		return
	}
	png, err := base64.StdEncoding.DecodeString(in.PreviewPNG)
	if err != nil {
		fail(w, 400, "previewPng must be base64")
		return
	}
	spec := skyViewSpec{Schema: SkyViewSchema, Frame: in.Frame, Centre: in.Centre, FovDeg: in.FovDeg, Survey: survey, Footprints: []skyFootprintRecord{}}
	var window []bool // per footprint: an outline of a window onto the file
	seen := map[skyRef]bool{}
	identity := figureIdentity{Schema: "figure-identity/1", Datasets: []figureDatasetIdentity{}}
	for _, ref := range in.Footprints {
		if (ref.Kind != "dataset" && ref.Kind != "subset") || ref.ID < 1 || seen[ref] {
			fail(w, 400, "each footprint names a dataset or a subset once")
			return
		}
		seen[ref] = true
		rec := skyFootprintRecord{}
		var name string
		var src int64
		if ref.Kind == "subset" {
			sub, err := s.Subsets.Get(wsOf(r), ref.ID)
			if errors.Is(err, subsets.ErrNotFound) {
				fail(w, 400, fmt.Sprintf("subset %d does not exist", ref.ID))
				return
			}
			if err != nil {
				fail(w, 500, "internal error")
				return
			}
			rec.SubsetID, rec.SubsetVersion, src = sub.ID, sub.Current.Version, sub.SourceID
		} else {
			src = ref.ID
		}
		d, err := s.Store.Get(wsOf(r), src)
		if err != nil {
			fail(w, 400, fmt.Sprintf("dataset %d is not in the catalog", src))
			return
		}
		name = d.Name
		rep, code, msg := s.skyFootprintFor(r, ref, in.Frame)
		if code != 200 {
			fail(w, code, name+": "+msg)
			return
		}
		if rep.Footprint == nil {
			reason := "it has no footprint on the sky"
			if rep.Reason != nil {
				reason = *rep.Reason
			}
			fail(w, 422, name+": "+reason)
			return
		}
		rec.DatasetID, rec.Name, rec.ExtentDeg = d.ID, name, rep.Footprint.ExtentDeg
		rec.FromFrame, rec.FromStatus, rec.FromBasis = rep.FromFrame, rep.FromStatus, rep.FromBasis
		spec.Footprints = append(spec.Footprints, rec)
		window = append(window, ref.Kind == "subset")
		// the file's identity when drawn, for the newer-data check; its role is not "source", since no one file is the figure's
		var one figureIdentity
		if json.Unmarshal(s.buildFigureIdentity(r.Context(), s.ensureFingerprinted(r.Context(), d), nil, nil, nil), &one) == nil {
			for _, e := range one.Datasets {
				e.Role = "footprint"
				identity.Datasets = append(identity.Datasets, e)
			}
		}
	}
	sceneJSON, err := json.Marshal(spec)
	if err != nil {
		fail(w, 500, "internal error")
		return
	}
	idJSON, _ := json.Marshal(identity)
	prov, err := skyProvenance(spec, window, time.Now())
	if err != nil {
		fail(w, 500, "internal error")
		return
	}
	f, err := s.Boards.CreateFigure(wsOf(r), actorID(r.Context()), boards.NewFigure{Kind: boards.KindSky, Title: in.Title, Caption: in.Caption, Provenance: prov, Scene: sceneJSON, Identity: idJSON}, png)
	if err != nil {
		s.boardErr(w, err)
		return
	}
	if v, err := s.accessView(wsOf(r)); err == nil {
		f.Access = figureAccess(v, &f)
	}
	writeJSON(w, 201, f)
}

func skyFrameName(frame string) string {
	if frame == "galactic" {
		return "Galactic (l, b)"
	}
	return "ICRS (RA, Dec)"
}

// skyProvenance is the reviewer-safe provenance of a sky view. It has the shape every figure's provenance has, says plainly that
// there is no file, and states in words what the survey is and is not.
func skyProvenance(spec skyViewSpec, window []bool, at time.Time) (json.RawMessage, error) {
	var pr Provenance
	pr.Source.Name = fmt.Sprintf("sky view of %d datasets", len(spec.Footprints))
	pr.Source.ModifiedAt = at.UTC()
	pr.Source.Identity = "no file: a view of the sky panel, with each outline placed by that dataset's celestial WCS as it was when this was saved"
	pr.Source.Header = safeHeader{Axes: []safeAxis{}}
	pr.Tiers = []safeTier{}
	pr.View = ViewParams{Kind: "sky"}
	sky := &safeSkyView{Frame: spec.Frame, Centre: spec.Centre, FovDeg: spec.FovDeg, Background: spec.Survey, Footprints: []safeSkyFoot{}}
	for i, f := range spec.Footprints {
		sky.Footprints = append(sky.Footprints, safeSkyFoot{Name: f.Name, Window: window[i], Basis: f.FromBasis})
	}
	pr.Sky = sky
	pr.Statements = []string{
		fmt.Sprintf("Sky view in %s centred on %s %.5f, %s %.5f deg, %.4g deg across. Each outline is the footprint of one dataset's image, placed by that dataset's celestial WCS and carried into this frame by astropy; how each frame is known is listed per dataset.", skyFrameName(spec.Frame), lonName(spec.Frame), spec.Centre.Lon, latName(spec.Frame), spec.Centre.Lat, spec.FovDeg),
	}
	if sv := spec.Survey; sv != nil {
		pr.Statements = append(pr.Statements, fmt.Sprintf("Survey background (context only, not data from the author's files, never sampled into a number, not part of any measurement): %s, HiPS %s, served by %s. Credit: %s.", sv.Name, sv.ID, sv.Provider, sv.Credit))
	} else {
		pr.Statements = append(pr.Statements, "No survey image: the outlines are drawn on a plain coordinate grid.")
	}
	return json.Marshal(pr)
}

func lonName(frame string) string {
	if frame == "galactic" {
		return "l"
	}
	return "RA"
}

func latName(frame string) string {
	if frame == "galactic" {
		return "b"
	}
	return "Dec"
}
