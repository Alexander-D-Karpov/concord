// Package swagger serves the Swagger UI and the OpenAPI (openapiv2) spec over HTTP.
//
// The UI assets are compiled into the binary with go:embed. NewHandler loads and
// parses the spec at construction time, so a missing or invalid spec path fails at
// startup rather than per request. GenerateSpec is currently a stub: it only
// ensures the output directory exists and returns nil — the OpenAPI spec is
// produced out-of-band by the proto tooling, not by this package.
package swagger
