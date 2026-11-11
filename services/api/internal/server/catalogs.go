package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/worker"
)

// Point catalogs (ADR 0044): tables of sources registered as datasets of kind "catalog". The worker reads them with astropy,
// bounded and never executed; this file is the API around that: registration under the same root authorisation as a FITS
// file, the declaration of columns, pages of rows, the parse report and positions on the sky.

// catalogExts are the file names registered as catalogs without asking the worker about the content. A FITS file is told apart
// by what the worker finds in it (see registerPath).
var catalogExts = []string{".csv", ".tsv", ".ecsv", ".dat", ".txt", ".tbl", ".cat", ".xcat", ".vot", ".votable"}

func hasCatalogExt(name string) bool {
	l := strings.ToLower(name)
	for _, e := range catalogExts {
		if strings.HasSuffix(l, e) {
			return true
		}
	}
	return false
}

// catalogDo asks the worker one catalog question and returns its JSON document (the payload of the reply). The code is the
// HTTP status to answer with when it is not 0.
func (s *Server) catalogDo(ctx context.Context, svc PlaneService, op, real string, extra map[string]any) (json.RawMessage, int, string) {
	if svc == nil {
		return nil, 503, "the scientific worker is not configured"
	}
	req := map[string]any{"op": op, "path": real}
	for k, v := range extra {
		req[k] = v
	}
	rep, err := svc.Do(ctx, req)
	var re *worker.RequestError
	switch {
	case err == nil:
		return json.RawMessage(rep.Payload), 0, ""
	case errors.As(err, &re):
		switch re.Code {
		case "bad_request":
			return nil, 400, re.Message
		case "forbidden":
			return nil, 403, re.Message
		}
		return nil, 422, re.Message
	case errors.Is(err, context.Canceled):
		return nil, 499, "request cancelled"
	case errors.Is(err, worker.ErrTimeout):
		return nil, 504, "the scientific worker timed out"
	}
	log.Printf("catalog worker: %v", err)
	return nil, 503, "the scientific worker is unavailable"
}

// registerCatalogPath reads an authorised file as a catalog and records the reference. The header kept in the catalog is the
// worker's description of the table (format, row count, columns); the rows stay in the file.
func (s *Server) registerCatalogPath(ctx context.Context, real string) (catalog.Dataset, int, string) {
	doc, code, msg := s.catalogDo(ctx, s.planes(), "catalog_describe", real, nil)
	if code != 0 {
		return catalog.Dataset{}, code, msg
	}
	var d struct {
		Format  string                  `json:"format"`
		Rows    int64                   `json:"rows"`
		Columns []catalog.CatalogColumn `json:"columns"`
	}
	if err := json.Unmarshal(doc, &d); err != nil || len(d.Columns) == 0 {
		return catalog.Dataset{}, 502, "unreadable answer from the worker"
	}
	hdr, _ := json.Marshal(catalog.CatalogHeader{Kind: catalog.KindCatalog, Format: d.Format, Rows: d.Rows, Columns: d.Columns})
	info, err := statFile(real)
	if err != nil {
		return catalog.Dataset{}, 404, "file not found"
	}
	ds, err := s.Store.Upsert(catalog.Dataset{Path: real, Name: filepath.Base(real), SizeBytes: info.Size(), ModifiedAt: info.ModTime(), Header: hdr})
	if err != nil {
		log.Printf("register catalog: %v", err)
		return catalog.Dataset{}, 500, "internal error"
	}
	s.Audit.Record("catalog registered", "dataset", ds.ID, ds.Name)
	return ds, 0, ""
}

func (s *Server) planes() PlaneService {
	if s.Planes == nil {
		return nil
	}
	return s.Planes
}

// registerCatalog is POST /api/catalogs: register a file as a point catalog, whatever its name (a FITS table has a .fits name).
// The path goes through the same source-root check as FITS registration.
func (s *Server) registerCatalog(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var in struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, 400, "invalid request body")
		return
	}
	real, err := s.roots().Resolve(in.Path)
	if err != nil {
		fail(w, 403, err.Error())
		return
	}
	d, code, msg := s.registerCatalogPath(r.Context(), real)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	writeJSON(w, 201, d)
}

// catalogSource resolves {id} to an authorised, unchanged catalog file, or answers why not.
func (s *Server) catalogSource(w http.ResponseWriter, r *http.Request) (d catalog.Dataset, real string, ok bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, 400, "bad id")
		return
	}
	d, code, msg := s.dataset(id)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	if d.Kind != catalog.KindCatalog {
		fail(w, 409, catalog.ErrNotACatalog.Error())
		return
	}
	real, _, status, msg := s.authorize(r.Context(), d)
	if status != 0 {
		fail(w, status, msg)
		return
	}
	return d, real, true
}

func intParam(r *http.Request, name string, def, low, high int) (int, bool) {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def, true
	}
	n, err := strconv.Atoi(v)
	return n, err == nil && n >= low && n <= high
}

// catalogRows is GET /api/datasets/{id}/catalog/rows?offset&limit: a page of the table, cells as the file gave them (missing is
// null, never 0), with each row's line in the file.
func (s *Server) catalogRows(w http.ResponseWriter, r *http.Request) {
	_, real, ok := s.catalogSource(w, r)
	if !ok {
		return
	}
	offset, ok1 := intParam(r, "offset", 0, 0, 1<<30)
	limit, ok2 := intParam(r, "limit", 100, 1, 500)
	if !ok1 || !ok2 {
		fail(w, 400, "offset must be a non-negative integer and limit between 1 and 500")
		return
	}
	doc, code, msg := s.catalogDo(r.Context(), s.planes(), "catalog_rows", real, map[string]any{"offset": offset, "limit": limit})
	if code != 0 {
		fail(w, code, msg)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(doc)
}

// declarationArg is the declaration sent to the worker, or an answer for why there is none to use: a declaration made about
// other bytes (the file changed since) is not applied, the person confirms it again.
func declarationArg(d catalog.Dataset) (map[string]any, int, string) {
	c := d.CatalogDeclaration
	if c == nil {
		return nil, 409, "no columns are declared yet: say which columns are the longitude and latitude, how they are written and which celestial frame they use"
	}
	if c.Stale {
		return nil, 409, "the file changed since you declared its columns; check the declaration and confirm it again"
	}
	b, _ := json.Marshal(c)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for _, k := range []string{"declaredBy", "declaredAt", "stale"} {
		delete(m, k)
	}
	return m, 0, ""
}

// catalogReport is GET /api/datasets/{id}/catalog/report: how the file was read (format, columns, units when the file declares
// them, a preview, lines that were not rows), what the column names suggest (a proposal, never stored), and, when a
// declaration is in force, how it reads every row: placed, missing, unreadable with their line.
func (s *Server) catalogReport(w http.ResponseWriter, r *http.Request) {
	d, real, ok := s.catalogSource(w, r)
	if !ok {
		return
	}
	doc, code, msg := s.catalogDo(r.Context(), s.planes(), "catalog_describe", real, nil)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(doc, &out); err != nil {
		fail(w, 502, "unreadable answer from the worker")
		return
	}
	out["declaration"], _ = json.Marshal(d.CatalogDeclaration)
	out["parse"] = json.RawMessage("null")
	out["parseError"] = json.RawMessage("null")
	if decl, code, msg := declarationArg(d); code == 0 {
		rep, code, msg := s.catalogDo(r.Context(), s.planes(), "catalog_report", real, map[string]any{"declaration": decl})
		if code == 0 {
			out["parse"] = rep
		} else {
			out["parseError"], _ = json.Marshal(msg) // e.g. a sexagesimal column too long to place: shown next to the form
		}
	} else if d.CatalogDeclaration != nil {
		out["parseError"], _ = json.Marshal(msg)
	}
	writeJSON(w, 200, out)
}

// putCatalogDeclaration is PUT /api/datasets/{id}/catalog/declaration: validate and store the columns' meaning. Nothing is
// inferred: a missing frame, equinox, format or angle unit is a 400 that names it.
func (s *Server) putCatalogDeclaration(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var in catalog.CatalogDeclaration
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		fail(w, 400, "invalid request body")
		return
	}
	d, _, ok := s.catalogSource(w, r)
	if !ok {
		return
	}
	bad, err := catalog.ValidateCatalogDeclaration(d, in)
	if err != nil {
		fail(w, 409, err.Error())
		return
	}
	if len(bad) > 0 {
		writeJSON(w, 400, map[string]any{"error": "the declaration is not complete or not consistent", "reasons": bad})
		return
	}
	if err := s.Store.SetCatalogDeclaration(d, in, "user"); err != nil {
		log.Printf("catalog declaration: %v", err)
		fail(w, 500, "internal error")
		return
	}
	s.Audit.Record("catalog columns declared", "dataset", d.ID, d.Name+": "+in.Frame+" "+in.Format)
	nd, code, msg := s.dataset(d.ID)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	writeJSON(w, 200, nd)
}

// deleteCatalogDeclaration is DELETE /api/datasets/{id}/catalog/declaration.
func (s *Server) deleteCatalogDeclaration(w http.ResponseWriter, r *http.Request) {
	d, _, ok := s.catalogSource(w, r)
	if !ok {
		return
	}
	if err := s.Store.ClearCatalogDeclaration(d.ID); err != nil {
		log.Printf("catalog declaration: %v", err)
		fail(w, 500, "internal error")
		return
	}
	s.Audit.Record("catalog declaration withdrawn", "dataset", d.ID, d.Name)
	nd, code, msg := s.dataset(d.ID)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	writeJSON(w, 200, nd)
}

// catalogPositions is GET /api/datasets/{id}/catalog/positions?offset&limit[&frame&equinox]: sky positions (and angles and
// uncertainties) of rows in the declared frame, or transformed to another frame with astropy. Refused, naming what is missing,
// until a fresh declaration exists. An unreadable row has null in every field; its count and lines are in the report.
func (s *Server) catalogPositions(w http.ResponseWriter, r *http.Request) {
	d, real, ok := s.catalogSource(w, r)
	if !ok {
		return
	}
	decl, code, msg := declarationArg(d)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	offset, ok1 := intParam(r, "offset", 0, 0, 1<<30)
	limit, ok2 := intParam(r, "limit", 1000, 1, 100000)
	if !ok1 || !ok2 {
		fail(w, 400, "offset must be a non-negative integer and limit between 1 and 100000")
		return
	}
	req := map[string]any{"declaration": decl, "offset": offset, "limit": limit}
	op := "catalog_positions"
	if f := r.URL.Query().Get("frame"); f != "" {
		op = "catalog_transform"
		to := map[string]any{"frame": f}
		if eq := r.URL.Query().Get("equinox"); eq != "" {
			to["equinox"] = eq
		}
		req["to"] = to
	}
	doc, code, msg := s.catalogDo(r.Context(), s.planes(), op, real, req)
	if code != 0 {
		fail(w, code, msg)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(doc)
}
