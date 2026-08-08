// Package gifprovider abstracts GIF search behind a Provider interface, with a
// Tenor implementation.
//
// The API key is passed in by the caller rather than read from the environment
// here; when it is empty, Enabled reports false and GIF search is silently
// disabled.
package gifprovider
