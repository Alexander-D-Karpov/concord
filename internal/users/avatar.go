package users

import (
	"bytes"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	"golang.org/x/image/draw"
)

const (
	AvatarFullMaxSize  = 512
	AvatarThumbSize    = 64
	AvatarJPEGQuality  = 85
	AvatarThumbQuality = 80
	MaxAvatarHistory   = 10
	MaxAvatarBytes     = 10 * 1024 * 1024
)

type ProcessedAvatar struct {
	FullData  []byte
	ThumbData []byte
	Width     int
	Height    int
}

func ProcessAvatarImage(data []byte) (*ProcessedAvatar, error) {
	if len(data) > MaxAvatarBytes {
		return nil, fmt.Errorf("image too large: %d bytes (max %d)", len(data), MaxAvatarBytes)
	}

	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}

	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()

	fullImg := resizeImage(src, w, h, AvatarFullMaxSize)
	squared := cropToSquare(src)
	thumbImg := resizeImage(squared, squared.Bounds().Dx(), squared.Bounds().Dy(), AvatarThumbSize)

	var fullBuf bytes.Buffer
	if err := jpeg.Encode(&fullBuf, fullImg, &jpeg.Options{Quality: AvatarJPEGQuality}); err != nil {
		return nil, fmt.Errorf("encode full: %w", err)
	}

	var thumbBuf bytes.Buffer
	if err := jpeg.Encode(&thumbBuf, thumbImg, &jpeg.Options{Quality: AvatarThumbQuality}); err != nil {
		return nil, fmt.Errorf("encode thumb: %w", err)
	}

	fb := fullImg.Bounds()
	return &ProcessedAvatar{
		FullData:  fullBuf.Bytes(),
		ThumbData: thumbBuf.Bytes(),
		Width:     fb.Dx(),
		Height:    fb.Dy(),
	}, nil
}

func cropToSquare(src image.Image) image.Image {
	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w == h {
		return src
	}
	size := w
	if h < w {
		size = h
	}
	x0 := bounds.Min.X + (w-size)/2
	y0 := bounds.Min.Y + (h-size)/2

	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	draw.NearestNeighbor.Scale(dst, dst.Bounds(), src, image.Rect(x0, y0, x0+size, y0+size), draw.Over, nil)
	return dst
}

func resizeImage(src image.Image, srcW, srcH, maxSize int) image.Image {
	if srcW <= maxSize && srcH <= maxSize {
		return src
	}

	var newW, newH int
	if srcW > srcH {
		newW = maxSize
		newH = int(float64(srcH) * float64(maxSize) / float64(srcW))
	} else {
		newH = maxSize
		newW = int(float64(srcW) * float64(maxSize) / float64(srcH))
	}
	if newW < 1 {
		newW = 1
	}
	if newH < 1 {
		newH = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, newW, newH))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
	return dst
}

func SaveAvatarFiles(basePath, userID string, fullData, thumbData []byte) (fullPath, thumbPath string, err error) {
	dir := filepath.Join(basePath, "avatars", userID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", "", fmt.Errorf("create avatar dir: %w", err)
	}

	fileID := uuid.New().String()[:8]
	fullName := fileID + "_full.jpg"
	thumbName := fileID + "_thumb.jpg"

	if err := writeFile(filepath.Join(dir, fullName), fullData); err != nil {
		return "", "", err
	}
	if err := writeFile(filepath.Join(dir, thumbName), thumbData); err != nil {
		return "", "", err
	}

	return filepath.Join("avatars", userID, fullName),
		filepath.Join("avatars", userID, thumbName),
		nil
}

func DeleteAvatarFiles(basePath, fullURL, thumbURL string) {
	for _, rel := range []string{fullURL, thumbURL} {
		if rel == "" {
			continue
		}
		_ = os.Remove(filepath.Join(basePath, rel))
	}
}

func writeFile(path string, data []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("create file %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	_, err = io.Copy(f, bytes.NewReader(data))
	if err != nil {
		return err
	}
	return f.Close()
}
