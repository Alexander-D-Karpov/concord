package auth

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestIngestAvatar(t *testing.T) {
	pic := testJPEG(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(pic)
	}))
	defer srv.Close()

	dir := t.TempDir()
	svc := &Service{}
	svc.SetAvatarIngestion(dir, "/files")

	full, thumb, err := svc.ingestAvatar(context.Background(), "user-123", srv.URL+"/pic.jpg")
	if err != nil {
		t.Fatalf("ingestAvatar: %v", err)
	}
	if !strings.HasPrefix(full, "/files/avatars/user-123/") || !strings.HasSuffix(full, "_full.jpg") {
		t.Errorf("full url = %q", full)
	}
	if !strings.HasPrefix(thumb, "/files/avatars/user-123/") || !strings.HasSuffix(thumb, "_thumb.jpg") {
		t.Errorf("thumb url = %q", thumb)
	}
	// files were actually written under the storage dir
	for _, u := range []string{full, thumb} {
		rel := strings.TrimPrefix(u, "/files/")
		if _, err := os.Stat(filepath.Join(dir, rel)); err != nil {
			t.Errorf("expected stored file for %q: %v", u, err)
		}
	}
}

func TestIngestAvatarRejectsNonImage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("definitely not an image"))
	}))
	defer srv.Close()

	svc := &Service{}
	svc.SetAvatarIngestion(t.TempDir(), "/files")

	if _, _, err := svc.ingestAvatar(context.Background(), "u", srv.URL); err == nil {
		t.Error("expected an error for a non-image body")
	}
}

func TestIngestAvatarHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()

	svc := &Service{}
	svc.SetAvatarIngestion(t.TempDir(), "/files")

	if _, _, err := svc.ingestAvatar(context.Background(), "u", srv.URL); err == nil {
		t.Error("expected an error for a non-200 response")
	}
}
