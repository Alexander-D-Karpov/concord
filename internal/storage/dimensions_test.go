package storage

import (
	"bytes"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"
)

func makeImage(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 100, A: 255})
		}
	}
	return img
}

// TestGetImageDimensions checks that dimensions are decoded from the encoded image
// bytes for the formats the platform accepts, and that non-image data reports 0, 0.
func TestGetImageDimensions(t *testing.T) {
	s := &Storage{}

	pngBuf := &bytes.Buffer{}
	if err := png.Encode(pngBuf, makeImage(7, 11)); err != nil {
		t.Fatal(err)
	}
	jpgBuf := &bytes.Buffer{}
	if err := jpeg.Encode(jpgBuf, makeImage(4, 9), nil); err != nil {
		t.Fatal(err)
	}
	gifBuf := &bytes.Buffer{}
	if err := gif.Encode(gifBuf, makeImage(5, 6), nil); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name        string
		data        []byte
		contentType string
		wantW       int
		wantH       int
	}{
		{"png", pngBuf.Bytes(), "image/png", 7, 11},
		{"jpeg", jpgBuf.Bytes(), "image/jpeg", 4, 9},
		{"gif", gifBuf.Bytes(), "image/gif", 5, 6},
		{"not an image", []byte("this is not an image"), "text/plain", 0, 0},
		{"empty", nil, "image/png", 0, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, h := s.getImageDimensions(tc.data, tc.contentType)
			if w != tc.wantW || h != tc.wantH {
				t.Errorf("getImageDimensions(%s) = (%d, %d), want (%d, %d)", tc.name, w, h, tc.wantW, tc.wantH)
			}
		})
	}
}
