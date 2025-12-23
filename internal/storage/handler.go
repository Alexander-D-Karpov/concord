package storage

import (
	"net/http"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
)

type Handler struct {
	service *Storage
	logger  *zap.Logger
}

func NewHandler(service *Storage, logger *zap.Logger) *Handler {
	return &Handler{
		service: service,
		logger:  logger,
	}
}

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
