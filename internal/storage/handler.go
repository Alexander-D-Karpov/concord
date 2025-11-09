package storage

import (
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/Alexander-D-Karpov/concord/internal/auth/interceptor"
	"go.uber.org/zap"
)

type Handler struct {
	storage *Storage
	logger  *zap.Logger
}

func NewHandler(storage *Storage, logger *zap.Logger) *Handler {
	return &Handler{
		storage: storage,
		logger:  logger,
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.handleUpload(w, r)
	case http.MethodGet:
		h.handleDownload(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *Handler) handleUpload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	userID := interceptor.GetUserID(ctx)
	if userID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	if err := r.ParseMultipartForm(MaxFileSize); err != nil {
		http.Error(w, "failed to parse form", http.StatusBadRequest)
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "file is required", http.StatusBadRequest)
		return
	}
	defer func(file multipart.File) {
		err := file.Close()
		if err != nil {
			h.logger.Error("failed to close file", zap.Error(err))
		}
	}(file)

	data, err := io.ReadAll(file)
	if err != nil {
		http.Error(w, "failed to read file", http.StatusBadRequest)
		return
	}

	contentType := header.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	info, err := h.storage.Store(ctx, data, header.Filename, contentType)
	if err != nil {
		h.logger.Error("failed to store file", zap.Error(err))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, err = fmt.Fprintf(w, `{"id":"%s","url":"%s","filename":"%s","size":%d,"content_type":"%s"}`,
		info.ID, info.URL, info.Filename, info.Size, info.ContentType)
	if err != nil {
		return
	}
}

func (h *Handler) handleDownload(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/files/")
	if path == "" {
		http.Error(w, "file path required", http.StatusBadRequest)
		return
	}

	path = filepath.Clean(path)

	reader, contentType, err := h.storage.Get(r.Context(), path)
	if err != nil {
		h.logger.Error("failed to get file", zap.Error(err))
		http.Error(w, "file not found", http.StatusNotFound)
		return
	}
	defer func(reader io.ReadCloser) {
		err := reader.Close()
		if err != nil {
			h.logger.Error("failed to close file reader", zap.Error(err))
		}
	}(reader)

	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=31536000")

	if _, err := io.Copy(w, reader); err != nil {
		h.logger.Error("failed to send file", zap.Error(err))
	}
}
