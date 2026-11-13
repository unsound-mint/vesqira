package server

import (
	"compress/gzip"
	"net/http"
	"strconv"
	"strings"
)

// gzipJSON compresses the answer when the client accepts it. It is for the few JSON answers that are large and repetitive (the
// dataset list); a handler calls it first and defers the returned close. It is not a middleware: the plane, terminal and stream
// routes must not be wrapped (they hijack, flush or set their own length).
func gzipJSON(w http.ResponseWriter, r *http.Request) (http.ResponseWriter, func()) {
	if !acceptsGzip(r.Header.Get("Accept-Encoding")) {
		return w, func() {}
	}
	w.Header().Set("Content-Encoding", "gzip")
	w.Header().Add("Vary", "Accept-Encoding")
	zw, _ := gzip.NewWriterLevel(w, gzip.BestSpeed)
	return &gzipWriter{ResponseWriter: w, zw: zw}, func() { _ = zw.Close() }
}

type gzipWriter struct {
	http.ResponseWriter
	zw *gzip.Writer
}

func (g *gzipWriter) Write(b []byte) (int, error) { return g.zw.Write(b) }

// acceptsGzip reads an Accept-Encoding value: gzip listed, and not with a quality of 0.
func acceptsGzip(h string) bool {
	for _, part := range strings.Split(h, ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if !strings.EqualFold(strings.TrimSpace(name), "gzip") {
			continue
		}
		if _, q, ok := strings.Cut(strings.ReplaceAll(params, " ", ""), "q="); ok {
			v, err := strconv.ParseFloat(q, 64)
			return err != nil || v > 0
		}
		return true
	}
	return false
}
