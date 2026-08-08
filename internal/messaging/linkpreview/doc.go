// Package unfurl fetches a URL and extracts OpenGraph/meta tags to build a link
// preview. (The directory is named linkpreview; the package is unfurl.)
//
// Service.Unfurl caches results by raw URL and caps the response body with an
// io.LimitReader. It is SSRF-hardened: the HTTP client uses a custom DialContext
// that enforces a destination-port allow-list and rejects any resolved IP that is
// loopback, link-local, multicast, private, unspecified, the 169.254.169.254 cloud
// metadata address, or IPv6 unique-local, and it limits redirects.
package unfurl
