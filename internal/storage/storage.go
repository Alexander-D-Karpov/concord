package storage

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"
)

type Storage struct {
	basePath string
	baseURL  string
	logger   *zap.Logger
}

type FileInfo struct {
	ID          string
	Filename    string
	ContentType string
	Size        int64
	Path        string
	URL         string
	Hash        string
	Width       int
	Height      int
}

const (
	MaxFileSize  = 50 * 1024 * 1024
	MaxImageSize = 10 * 1024 * 1024
)

var allowedTypes = map[string]bool{
	"image/jpeg":      true,
	"image/png":       true,
	"image/gif":       true,
	"image/webp":      true,
	"video/mp4":       true,
	"video/webm":      true,
	"application/pdf": true,
	"text/plain":      true,
}

func New(basePath, baseURL string, logger *zap.Logger) (*Storage, error) {
	if err := os.MkdirAll(basePath, 0755); err != nil {
		return nil, fmt.Errorf("create storage directory: %w", err)
	}

	return &Storage{
		basePath: basePath,
		baseURL:  baseURL,
		logger:   logger,
	}, nil
}

func (s *Storage) Store(ctx context.Context, data []byte, filename, contentType string) (*FileInfo, error) {
	reader := bytes.NewReader(data)
	return s.StoreFromReader(ctx, reader, filename, contentType)
}

func (s *Storage) StoreFromReader(ctx context.Context, reader io.Reader, filename, contentType string) (*FileInfo, error) {
	if contentType == "" {
		contentType = mime.TypeByExtension(filepath.Ext(filename))
	}

	if !allowedTypes[contentType] {
		return nil, fmt.Errorf("unsupported file type: %s", contentType)
	}

	maxSize := MaxFileSize
	if strings.HasPrefix(contentType, "image/") {
		maxSize = MaxImageSize
	}

	limitedReader := io.LimitReader(reader, int64(maxSize+1))
	data, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, fmt.Errorf("read file: %w", err)
	}

	if len(data) > maxSize {
		return nil, fmt.Errorf("file too large (max %d bytes)", maxSize)
	}

	hash := sha256.Sum256(data)
	hashStr := hex.EncodeToString(hash[:])

	id := uuid.New().String()
	ext := filepath.Ext(filename)
	storedFilename := id + ext

	datePath := time.Now().Format("2006/01/02")
	fullDir := filepath.Join(s.basePath, datePath)
	if err := os.MkdirAll(fullDir, 0755); err != nil {
		return nil, fmt.Errorf("create date directory: %w", err)
	}

	fullPath := filepath.Join(fullDir, storedFilename)
	if err := os.WriteFile(fullPath, data, 0644); err != nil {
		return nil, fmt.Errorf("write file: %w", err)
	}

	relativePath := filepath.Join(datePath, storedFilename)
	url := fmt.Sprintf("%s/%s", strings.TrimSuffix(s.baseURL, "/"), relativePath)

	info := &FileInfo{
		ID:          id,
		Filename:    filename,
		ContentType: contentType,
		Size:        int64(len(data)),
		Path:        relativePath,
		URL:         url,
		Hash:        hashStr,
	}

	if strings.HasPrefix(contentType, "image/") {
		width, height := s.getImageDimensions(data, contentType)
		info.Width = width
		info.Height = height
	}

	s.logger.Info("stored file",
		zap.String("id", id),
		zap.String("filename", filename),
		zap.Int64("size", info.Size),
		zap.String("type", contentType),
	)

	return info, nil
}

func (s *Storage) Get(ctx context.Context, path string) (io.ReadCloser, string, error) {
	fullPath := filepath.Join(s.basePath, path)

	info, err := os.Stat(fullPath)
	if err != nil {
		return nil, "", fmt.Errorf("file not found: %w", err)
	}

	if info.IsDir() {
		return nil, "", fmt.Errorf("path is a directory")
	}

	file, err := os.Open(fullPath)
	if err != nil {
		return nil, "", fmt.Errorf("open file: %w", err)
	}

	contentType := mime.TypeByExtension(filepath.Ext(path))
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	return file, contentType, nil
}

func (s *Storage) Delete(ctx context.Context, path string) error {
	fullPath := filepath.Join(s.basePath, path)
	if err := os.Remove(fullPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete file: %w", err)
	}
	return nil
}

func (s *Storage) getImageDimensions(data []byte, contentType string) (int, int) {
	return 0, 0
}
