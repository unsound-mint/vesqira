package runs

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// ROHSA decomposition runs (ADR 0046). The run record is the pipeline's own text: the Fortran namelist the fit ran with
// (parameters_n<N>.txt), rohsa_<label>_run_trace.toml and rohsa_<label>_products_trace.toml. Everything read from them is
// declared by them; nothing is verified, and a value they do not state stays unstated.

// RohsaInfo is what a ROHSA run knows beyond the common run model. It carries no path.
type RohsaInfo struct {
	Label       string `json:"label"` // rohsa_n4: the .dat's name without its extension
	DatName     string `json:"datName"`
	NGauss      int    `json:"nGauss"` // 0 when neither the trace nor the namelist declares it
	NGaussBasis string `json:"nGaussBasis"`
	Region      string `json:"region"`
	RegionBasis string `json:"regionBasis"` // declared (the trace names it) | inferred (the folder's name)
	// CropDatasetID is the registered crop the fit used; nil when its FITS file was not found, and then no spectral
	// calibration exists for this run.
	CropDatasetID *int64 `json:"cropDatasetId"`
	CropName      string `json:"cropName"`
	// HostID is the host the fit lives on (0: this machine). The .dat, its crop and the product maps are on that host, the
	// index of the .dat is built there (ADR 0046, remote fits), and only its pixels and small records travel.
	HostID int64    `json:"hostId,omitempty"`
	Notes  []string `json:"notes"`
	// UnitNotes says, per output role, how the unit shown for a product map relates to its name (see ProposeUnit).
	UnitNotes map[string]string `json:"unitNotes"`
}

// RohsaParsed is a ROHSA run read from its text records, before the crop and the products are looked up.
type RohsaParsed struct {
	Parsed
	Info RohsaInfo
	// ProductLabel is the product tree's name under <region>/rohsa/ (run_label in the products trace; else the .dat's stem).
	ProductLabel string
}

type rohsaTrace struct {
	NGauss int64          `toml:"n_gauss"`
	Region string         `toml:"region"`
	Status string         `toml:"status"`
	Config map[string]any `toml:"config"`
}

type rohsaProducts struct {
	NGauss     int64          `toml:"n_gauss"`
	Region     string         `toml:"region"`
	RunLabel   string         `toml:"run_label"`
	FitMetrics map[string]any `toml:"fit_metrics"`
}

var namelistLine = regexp.MustCompile(`^\s*,?\s*([A-Za-z_][A-Za-z0-9_]*)\s*=\s*(.*?)\s*,?\s*$`)

// pathKeys are namelist entries that name files on the machine the fit ran on, whatever their value looks like.
var pathKeys = map[string]bool{"filename": true, "fileout": true, "timeout": true, "filename_noise": true}

func namelist(data []byte) map[string]any {
	out := map[string]any{}
	for _, line := range strings.Split(string(data), "\n") {
		m := namelistLine.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil || pathKeys[strings.ToLower(m[1])] {
			continue
		}
		out[strings.ToLower(m[1])] = strings.Trim(m[2], `'"`)
	}
	return out
}

// ParseROHSA reads the three optional text records of a ROHSA run. A record that is absent is simply nil.
func ParseROHSA(trace, products, params []byte, stem, folder string) (RohsaParsed, error) {
	for _, b := range [][]byte{trace, products, params} {
		if len(b) > MaxMetadataBytes {
			return RohsaParsed{}, errors.New("a ROHSA record is too large")
		}
	}
	var tr rohsaTrace
	var pr rohsaProducts
	if len(trace) > 0 {
		if _, err := toml.Decode(string(trace), &tr); err != nil {
			return RohsaParsed{}, errors.New("the run trace is not valid TOML")
		}
	}
	if len(products) > 0 {
		if _, err := toml.Decode(string(products), &pr); err != nil {
			return RohsaParsed{}, errors.New("the products trace is not valid TOML")
		}
	}
	var topTrace map[string]any
	if len(trace) > 0 {
		_, _ = toml.Decode(string(trace), &topTrace)
		delete(topTrace, "config")
	}
	nl := namelist(params)

	p := RohsaParsed{ProductLabel: stem}
	p.Kind = "rohsa"
	p.Name = stem
	p.Diagnostics = map[string]string{}
	if tr.Status != "" && len(tr.Status) <= 60 {
		p.Diagnostics["status"] = tr.Status
	}
	if pr.RunLabel != "" && !strings.ContainsAny(pr.RunLabel, `/\`) {
		p.ProductLabel = pr.RunLabel
	}
	p.Params = mergeParams(mergeParams(params2(tr.Config, "config"), params2(nl, "parameters")), params2(topTrace, "run_trace"))
	if len(p.Params) > maxParams {
		p.Params = p.Params[:maxParams]
	}
	metrics := map[string]any{}
	for k, v := range pr.FitMetrics {
		if _, ok := number(v); ok { // the trace's metrics are numbers; anything else is not one
			metrics[k] = v
		}
	}
	p.Metrics = parseMetrics(metrics)

	info := RohsaInfo{Label: stem, DatName: stem + ".dat", Notes: []string{}, UnitNotes: map[string]string{}}
	switch {
	case tr.NGauss > 0:
		info.NGauss, info.NGaussBasis = int(tr.NGauss), "declared by the run trace"
	case pr.NGauss > 0:
		info.NGauss, info.NGaussBasis = int(pr.NGauss), "declared by the products trace"
	default:
		if v, ok := nl["n_gauss"].(string); ok {
			var n int
			if _, err := fmt.Sscanf(v, "%d", &n); err == nil && n > 0 {
				info.NGauss, info.NGaussBasis = n, "declared by the parameter file"
			}
		}
	}
	if info.NGauss == 0 || info.NGauss > 64 {
		info.NGauss = 0
		info.Notes = append(info.Notes, "The number of Gaussians per pixel is not declared by the run's records, so the components in the .dat cannot be read.")
	}
	switch {
	case tr.Region != "":
		info.Region, info.RegionBasis = tr.Region, "declared"
	case pr.Region != "":
		info.Region, info.RegionBasis = pr.Region, "declared"
	default:
		info.Region, info.RegionBasis = folder, "inferred from the folder's name"
	}
	if len(trace) == 0 && len(products) == 0 && len(params) == 0 {
		info.Notes = append(info.Notes, "No trace or parameter file sits beside the .dat: the run has no recorded settings.")
	}
	p.Info = info
	return p, nil
}

func params2(m map[string]any, source string) []Param {
	if m == nil {
		return nil
	}
	return params(m, source)
}

// ProposeUnit proposes a unit from a product's file-name suffix. It is a proposal: the file's own BUNIT always wins.
func ProposeUnit(role string) (unit, basis string, ok bool) {
	for _, s := range []struct{ suffix, unit string }{{"_kkms", "K km/s"}, {"_kms", "km/s"}, {"_cm2", "cm^-2"}, {"_k", "K"}} {
		if strings.HasSuffix(role, s.suffix) {
			return s.unit, fmt.Sprintf("%s proposed from the file name's %q suffix", s.unit, s.suffix), true
		}
	}
	return "", "", false
}

func normUnit(u string) string {
	return strings.ToLower(strings.NewReplacer(" ", "", "^", "", "*", "", "·", "").Replace(u))
}

// SameUnit compares two unit spellings loosely enough that "cm^-2" and "cm-2" agree and "K" and "km/s" do not.
func SameUnit(a, b string) bool { return normUnit(a) == normUnit(b) }

// ---- store ----

func (s *Store) openROHSA() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS rohsa_runs(run_id INTEGER PRIMARY KEY, dat_path TEXT NOT NULL, info TEXT NOT NULL)`)
	return err
}

// SetROHSA records what a ROHSA run knows beyond the common run model; datPath is kept server-side only.
func (s *Store) SetROHSA(runID int64, datPath string, info RohsaInfo) error {
	b, err := json.Marshal(info)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO rohsa_runs(run_id,dat_path,info) VALUES(?,?,?) ON CONFLICT(run_id) DO UPDATE SET dat_path=excluded.dat_path,info=excluded.info`, runID, datPath, string(b))
	return err
}

// RohsaRun is a ROHSA run's extra record with the .dat's location, which only the server side sees.
type RohsaRun struct {
	RunID   int64
	RunName string
	DatPath string
	Info    RohsaInfo
}

// ROHSA returns the record of a run, or ErrNotFound when it is not a ROHSA run.
func (s *Store) ROHSA(runID int64) (RohsaRun, error) {
	var r RohsaRun
	var info string
	err := s.db.QueryRow(`SELECT r.id,r.name,x.dat_path,x.info FROM rohsa_runs x JOIN runs r ON r.id=x.run_id WHERE x.run_id=?`, runID).Scan(&r.RunID, &r.RunName, &r.DatPath, &info)
	if errors.Is(err, sql.ErrNoRows) {
		return RohsaRun{}, ErrNotFound
	}
	if err != nil {
		return RohsaRun{}, err
	}
	return r, decodeInfo(info, &r.Info)
}

// AllROHSA lists every ROHSA run, newest first.
func (s *Store) AllROHSA() ([]RohsaRun, error) {
	rows, err := s.db.Query(`SELECT r.id,r.name,x.dat_path,x.info FROM rohsa_runs x JOIN runs r ON r.id=x.run_id ORDER BY r.id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RohsaRun
	for rows.Next() {
		var r RohsaRun
		var info string
		if err := rows.Scan(&r.RunID, &r.RunName, &r.DatPath, &info); err != nil {
			return nil, err
		}
		if err := decodeInfo(info, &r.Info); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func decodeInfo(s string, into *RohsaInfo) error {
	if err := json.Unmarshal([]byte(s), into); err != nil {
		return err
	}
	if into.Notes == nil {
		into.Notes = []string{}
	}
	if into.UnitNotes == nil {
		into.UnitNotes = map[string]string{}
	}
	return nil
}
