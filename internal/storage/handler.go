package storage

import (
	"net/http"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
)

// Handler is an HTTP handler that serves stored blobs from the local filesystem.
type Handler struct {
	service *Storage
	logger  *zap.Logger
}

// NewHandler returns a Handler that serves files from the given Storage.
func NewHandler(service *Storage, logger *zap.Logger) *Handler {
	return &Handler{
		service: service,
		logger:  logger,
	}
}

// ServeHTTP serves a GET request for a stored file under the "/files/" prefix. It
// rejects non-GET methods, empty paths, and any path containing ".." after
// cleaning to prevent directory traversal outside the storage base path.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/files/")
	path = strings.TrimPrefix(path, "/")

	if path == "" {
		http.Error(w, "File not found", http.StatusNotFound)
		return
	}

	path = filepath.Clean(path)
	if strings.Contains(path, "..") {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}

	fullPath := filepath.Join(h.service.basePath, path)

	h.logger.Debug("serving file",
		zap.String("requested_path", r.URL.Path),
		zap.String("clean_path", path),
		zap.String("full_path", fullPath),
	)

	http.ServeFile(w, r, fullPath)
}
