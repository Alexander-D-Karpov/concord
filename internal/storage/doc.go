// Package storage provides local-filesystem blob storage for uploads plus an HTTP
// handler that serves them.
//
// Store/StoreFromReader write bytes under a base path and return a URL built from
// the configured base URL, decoding pixel dimensions from the image header for
// PNG/JPEG/GIF/WebP uploads. Handler serves the stored files over HTTP (the API
// adds long-lived cache headers for avatars).
package storage
