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
	// AvatarFullMaxSize is the max width/height (px) of the processed full-size avatar; larger images are downscaled.
	AvatarFullMaxSize = 512
	// AvatarThumbSize is the width/height (px) of the square thumbnail.
	AvatarThumbSize = 64
	// AvatarJPEGQuality is the JPEG quality (1-100) used when encoding the full-size avatar.
	AvatarJPEGQuality = 85
	// AvatarThumbQuality is the JPEG quality (1-100) used when encoding the thumbnail.
	AvatarThumbQuality = 80
	// MaxAvatarHistory is the number of past avatars retained per user; older ones are pruned on upload.
	MaxAvatarHistory = 10
	// MaxAvatarBytes is the maximum accepted size of a raw uploaded avatar image.
	MaxAvatarBytes = 10 * 1024 * 1024
)

// ProcessedAvatar holds the JPEG-encoded full and thumbnail data plus the full
// image's final pixel dimensions, produced by ProcessAvatarImage.
type ProcessedAvatar struct {
	FullData  []byte
	ThumbData []byte
	Width     int
	Height    int
}

// magicBytes maps supported image MIME types to their file-signature prefixes,
// used by ValidateImageMagic to sniff format from content rather than filename.
var magicBytes = map[string][]byte{
	"image/jpeg": {0xFF, 0xD8, 0xFF},
	"image/png":  {0x89, 0x50, 0x4E, 0x47},
	"image/gif":  {0x47, 0x49, 0x46},
	"image/webp": {0x52, 0x49, 0x46, 0x46},
}

// ValidateImageMagic sniffs data's leading bytes against known image signatures
// and returns the detected MIME type, or an error if no format matches.
func ValidateImageMagic(data []byte) (string, error) {
	for mime, magic := range magicBytes {
		if len(data) >= len(magic) {
			match := true
			for i, b := range magic {
				if data[i] != b {
					match = false
					break
				}
			}
			if match {
				return mime, nil
			}
		}
	}
	return "", fmt.Errorf("unrecognized image format")
}

// ProcessAvatarImage validates, decodes, downscales, and re-encodes an uploaded
// image into full-size and square-thumbnail JPEGs. Re-encoding to JPEG strips all
// EXIF/metadata. It errors if the input exceeds MaxAvatarBytes or is an
// unrecognized/undecodable format.
func ProcessAvatarImage(data []byte) (*ProcessedAvatar, error) {
	if len(data) > MaxAvatarBytes {
		return nil, fmt.Errorf("image too large: %d bytes (max %d)", len(data), MaxAvatarBytes)
	}

	if _, err := ValidateImageMagic(data); err != nil {
		return nil, fmt.Errorf("invalid image: %w", err)
	}

	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}

	// Re-encoding to JPEG strips all EXIF/metadata automatically
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

// cropToSquare returns a center-cropped square copy of src (the shorter side
// determines the size); it returns src unchanged if already square.
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

// resizeImage scales src down with Catmull-Rom so neither side exceeds maxSize,
// preserving aspect ratio (each side clamped to a minimum of 1px); it returns src
// unchanged if it already fits.
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

// SaveAvatarFiles writes the full and thumbnail JPEGs under
// basePath/avatars/<userID>/ with a random shared file-ID prefix, and returns the
// two paths relative to basePath (suitable for building URLs).
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

// DeleteAvatarFiles best-effort removes the full and thumbnail files (given as
// paths relative to basePath); empty paths are skipped and removal errors are ignored.
func DeleteAvatarFiles(basePath, fullURL, thumbURL string) {
	for _, rel := range []string{fullURL, thumbURL} {
		if rel == "" {
			continue
		}
		_ = os.Remove(filepath.Join(basePath, rel))
	}
}

// writeFile creates the file at path and writes data to it, returning any
// create/write/close error.
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
