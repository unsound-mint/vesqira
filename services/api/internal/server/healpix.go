package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/derived"
)

// HEALPix polarization maps (ADR 0067). A Planck HFI SkyMap is registered as a dataset of kind "healpix" (the worker refuses one that
// does not state POLCCONV and COORDSYS); it has no pixel grid, so the one thing done with it is sampling it onto another dataset's
// celestial grid as Stokes Q and U, with the COSMO to IAU flip and the turn from Galactic north to the grid's north applied before an
// angle is formed. The result is an orientation map declared by the operation (dust polarization, or the field orientation at +90
// degrees), with its P and sigma_P maps as companions, and a derived record saying how it was made.

// healpixSampleReq is POST /api/analysis/healpix-sample.
type healpixSampleReq struct {
	DatasetID int64 `json:"datasetId"` // the HEALPix map
	TargetID  int64 `json:"targetId"`  // the dataset whose celestial grid receives it
	// Product is "dust_polarization" (psi = 1/2 atan2(U, Q)) or "b_field" (psi + 90 degrees).
	Product string `json:"product"`
	// Method is "nearest" (default), "bilinear" or "gaussian" (smoothing on the sphere with FwhmArcmin).
	Method     string   `json:"method"`
	FwhmArcmin *float64 `json:"fwhmArcmin"`
	// SnrMin blanks the angle where P/sigma_P is below it (P and sigma_P are kept everywhere); nil keeps every angle.
	SnrMin *float64 `json:"snrMin"`
}

var healpixProducts = map[string]bool{"dust_polarization": true, "b_field": true}
var healpixMethods = map[string]bool{"": true, "nearest": true, "bilinear": true, "gaussian": true}

// worker turns the request into the worker's parameters, refusing what is out of range (the worker checks again).
func (in healpixSampleReq) worker() (map[string]any, string) {
	if !healpixProducts[in.Product] {
		return nil, "say which product to derive: the dust polarization angle (dust_polarization) or the field orientation (b_field)"
	}
	if !healpixMethods[in.Method] {
		return nil, "the sampling method must be nearest, bilinear or gaussian"
	}
	out := map[string]any{"product": in.Product, "method": in.Method}
	if in.Method == "" {
		out["method"] = "nearest"
	}
	if in.Method == "gaussian" {
		if in.FwhmArcmin == nil || math.IsNaN(*in.FwhmArcmin) || *in.FwhmArcmin <= 0 || *in.FwhmArcmin > 600 {
			return nil, "Gaussian smoothing needs a FWHM in arcminutes, above 0 and at most 600"
		}
		out["fwhm_arcmin"] = *in.FwhmArcmin
	} else if in.FwhmArcmin != nil {
		return nil, "a smoothing FWHM applies to the gaussian method only"
	}
	if in.SnrMin != nil {
		if math.IsNaN(*in.SnrMin) || *in.SnrMin < 0 || *in.SnrMin > 1e6 {
			return nil, "the P/sigma_P threshold must be between 0 and 1000000"
		}
		out["snr_min"] = *in.SnrMin
	}
	return out, ""
}

// healpixSample samples a HEALPix map onto a dataset's celestial grid and registers the angle map with its P and sigma_P companions.
func (s *Server) healpixSample(w http.ResponseWriter, r *http.Request) {
	var in healpixSampleReq
	if !body(w, r, 8<<10, &in, true) {
		return
	}
	params, bad := in.worker()
	if bad != "" {
		fail(w, 400, bad)
		return
	}
	d, code, msg := s.dataset(wsOf(r), in.DatasetID)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	hdr, ok := catalog.ParseHealpixHeader(d.Header)
	if !ok {
		fail(w, 409, "this dataset is not a HEALPix map; only a HEALPix polarization map is sampled this way")
		return
	}
	if hdr.Frame != "galactic" {
		fail(w, 422, "this HEALPix map is in the "+hdr.Frame+" system (COORDSYS = "+hdr.Coordsys+"); only Galactic maps are sampled in this version, because the equatorial frame (FK5 or ICRS) or the equinox it means is not stated")
		return
	}
	if d.HostID != 0 {
		fail(w, 409, "this HEALPix map lives on a host, and sampling runs on this computer only: download it with Transfers, register the copy, and sample that")
		return
	}
	t, code, msg := s.imageDataset(wsOf(r), in.TargetID)
	if code != 0 {
		fail(w, code, "the target: "+msg)
		return
	}
	if t.HostID != 0 {
		fail(w, 409, "the target dataset lives on a host; both files must be on this computer")
		return
	}
	real, _, status, msg := s.authorize(r.Context(), d)
	if status != 0 {
		fail(w, status, msg)
		return
	}
	treal, _, status, msg := s.authorize(r.Context(), t)
	if status != 0 {
		fail(w, status, "the target: "+msg)
		return
	}
	var b [4]byte
	_, _ = rand.Read(b[:])
	stem := filepath.Join(s.derivedDirOf(wsOf(r)), fmt.Sprintf("healpix-%s-%d-%d-%s", in.Product, d.ID, t.ID, hex.EncodeToString(b[:])))
	outPath, pPath, sPath := stem+".fits", stem+"-p.fits", stem+"-sigma-p.fits"
	cleanup := func() {
		for _, p := range []string{outPath, pPath, sPath} {
			_ = os.Remove(p)
		}
	}
	pj, _ := json.Marshal(params)
	args := []string{"-P", s.Analysis.Script, "healpix_sample", "--input", real, "--reference", treal, "--params", string(pj),
		"--out", outPath, "--p-out", pPath, "--sigma-out", sPath}
	res, code, msg := s.runAnalysisProgram(r, args)
	if code != 0 {
		cleanup()
		fail(w, code, msg)
		return
	}
	nd, code, msg := s.registerPath(waiting(r.Context()), outPath)
	if code != 0 {
		cleanup()
		fail(w, code, msg)
		return
	}
	sources := []int64{d.ID, t.ID}
	pm := map[string]any{"options": params, "healpix": map[string]any{"nside": hdr.Nside, "ordering": hdr.Ordering, "coordsys": hdr.Coordsys, "polcconv": hdr.Polcconv}}
	if res.Corrections != nil {
		pm["corrections"] = res.Corrections // the target's WCS correction in force (ADR 0061): the grid the map was sampled onto
	}
	rec, _ := json.Marshal(pm)
	if err := s.Derived.Save(derived.Record{DatasetID: nd.ID, Op: "healpix_sample", Params: rec, Sources: sources, Summary: res.Summary, Software: res.Software}); err != nil {
		log.Printf("derived: %v", err)
	}
	// the companions: P and sigma_P on the same grid, each with a record pointing at the angle map
	companions := map[string]*catalog.Dataset{}
	for _, c := range []struct{ key, path, what string }{
		{"p", pPath, "polarized intensity P = sqrt(Q^2 + U^2), not debiased"},
		{"sigmaP", sPath, "the uncertainty of P from the HEALPix covariances after the kernel (sum of squared weights); correlated noise between HEALPix pixels is not modelled"},
	} {
		cd, code, msg := s.registerPath(waiting(r.Context()), c.path)
		if code != 0 {
			log.Printf("healpix: companion %s: %d %s", c.key, code, msg)
			continue
		}
		note, _ := json.Marshal(map[string]any{"policy": "The companion of " + nd.Name + ": " + c.what + ". See the angle map's own record for how the map was sampled.", "companionOf": nd.ID})
		if err := s.Derived.Save(derived.Record{DatasetID: cd.ID, Op: "healpix_sample", Params: rec, Sources: sources, Summary: note, Software: res.Software}); err != nil {
			log.Printf("derived: %v", err)
		}
		companions[c.key] = &cd
	}
	// The angle map is declared by the operation that made it: degrees, east of north in the grid's own celestial frame ("sky", no
	// skyFrame), and the quantity asked for. It is a proposal labelled as the analysis', like every orientation result (analysisAuthor).
	frame, quantity := "sky", in.Product
	if err := s.Store.SetInterpretation(nd, catalog.Interpretation{Kind: "axial_orientation", Unit: "deg", Frame: &frame, Quantity: &quantity, DeclaredBy: analysisAuthor}); err != nil {
		log.Printf("healpix: interpretation of the result: %v", err)
	} else if got, err := s.Store.Get(wsOf(r), nd.ID); err == nil {
		nd = got
	}
	s.Audit.RecordCtx(r.Context(), "HEALPix sample", "dataset", nd.ID, d.Name+" onto "+t.Name)
	writeJSON(w, 201, map[string]any{"dataset": nd, "p": companions["p"], "sigmaP": companions["sigmaP"], "summary": res.Summary})
}
