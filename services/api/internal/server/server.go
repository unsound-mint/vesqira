// Package server exposes the catalog over a small JSON API.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"path/filepath"
	"strconv"

	"github.com/unsound-mint/vesqira/services/api/internal/catalog"
	"github.com/unsound-mint/vesqira/services/api/internal/sourceroot"
)

type HeaderReader interface {
	Header(ctx context.Context, path string) (json.RawMessage, error)
}

type Server struct {
	Roots  sourceroot.Roots
	Store  *catalog.Store
	Worker HeaderReader
	Planes PlaneService // persistent plane worker; nil answers 503
	Cache  *PlaneCache  // optional; nil disables caching
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/roots", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]any{"roots": s.Roots}) })
	mux.HandleFunc("GET /api/datasets", s.list)
	mux.HandleFunc("GET /api/datasets/{id}", s.get)
	mux.HandleFunc("POST /api/datasets", s.register)
	mux.HandleFunc("GET /api/datasets/{id}/view", s.view)
	mux.HandleFunc("GET /api/datasets/{id}/plane", s.plane)
	mux.HandleFunc("GET /api/datasets/{id}/coord", s.coord)
	mux.HandleFunc("GET /api/datasets/{id}/spectrum", s.spectrum)
	mux.HandleFunc("GET /api/datasets/{id}/axial", s.axial)
	mux.HandleFunc("POST /api/datasets/{id}/interpretation", s.interpret)
	return localOnly(mux)
}

// localOnly rejects requests whose Host is not a loopback name, which blocks DNS-rebinding
// pages from reaching the local API, and rejects cross-site browser writes.
func localOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if i := lastColon(host); i >= 0 {
			host = host[:i]
		}
		if host != "127.0.0.1" && host != "localhost" && host != "[::1]" {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		// A page on another site must not make the API read files or run the worker, even by GET.
		if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
			http.Error(w, "cross-site request refused", http.StatusForbidden)
			return
		}
		if r.Method == http.MethodPost {
			if ct := r.Header.Get("Content-Type"); ct != "application/json" {
				http.Error(w, "content type must be application/json", http.StatusUnsupportedMediaType)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

func lastColon(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == ':' {
			if s[len(s)-1] == ']' {
				return -1
			}
			return i
		}
	}
	return -1
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func fail(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	ds, err := s.Store.List()
	if err != nil {
		log.Printf("list: %v", err)
		fail(w, 500, "internal error")
		return
	}
	writeJSON(w, 200, map[string]any{"datasets": ds})
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		fail(w, 400, "bad id")
		return
	}
	d, err := s.Store.Get(id)
	if errors.Is(err, catalog.ErrNotFound) {
		fail(w, 404, "dataset not found")
		return
	}
	if err != nil {
		log.Printf("get: %v", err)
		fail(w, 500, "internal error")
		return
	}
	writeJSON(w, 200, d)
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var in struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, 400, "invalid request body")
		return
	}
	real, err := s.Roots.Resolve(in.Path)
	if err != nil {
		fail(w, 403, err.Error())
		return
	}
	hdr, err := s.Worker.Header(r.Context(), real)
	if err != nil {
		fail(w, 422, err.Error())
		return
	}
	info, err := statFile(real)
	if err != nil {
		fail(w, 404, "file not found")
		return
	}
	d, err := s.Store.Upsert(catalog.Dataset{Path: real, Name: filepath.Base(real), SizeBytes: info.Size(), ModifiedAt: info.ModTime(), Header: hdr})
	if err != nil {
		log.Printf("register: %v", err)
		fail(w, 500, "internal error")
		return
	}
	writeJSON(w, 201, d)
}
