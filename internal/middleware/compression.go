package middleware

import (
	"compress/gzip"
	"io"
	"net/http"
	"strings"
)

// gzipResponseWriter wraps an http.ResponseWriter so that body writes are sent
// through a gzip.Writer while header and status handling stay on the original
// ResponseWriter.
type gzipResponseWriter struct {
	io.Writer
	http.ResponseWriter
}

// Write sends b to the embedded gzip.Writer rather than the underlying
// ResponseWriter, so the response body is compressed.
func (w gzipResponseWriter) Write(b []byte) (int, error) {
	return w.Writer.Write(b)
}

// CompressionMiddleware gzip-compresses responses for clients that advertise
// gzip in Accept-Encoding, setting Content-Encoding and flushing the gzip
// writer when the handler returns. Requests without gzip support pass through
// unmodified.
func CompressionMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			next.ServeHTTP(w, r)
			return
		}

		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		defer func() {
			if err := gz.Close(); err != nil {
				return
			}
		}()

		gzw := gzipResponseWriter{Writer: gz, ResponseWriter: w}
		next.ServeHTTP(gzw, r)
	})
}
