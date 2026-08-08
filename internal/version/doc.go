// Package version holds the version constants and formatted version strings for
// the API and Voice subsystems.
//
// Major/minor/patch numbers are compile-time constants; API() and Voice() format
// them for logs, headers, and banners. APICodename maps the API major version to
// a Concorde-themed name, falling back to a generated name past the codename list.
package version
