package version

import "fmt"

// concordeFleet holds the registration codes of the Concorde aircraft, indexed
// by production order. APIMajor selects an entry to serve as the release
// codename.
var concordeFleet = []string{
	"F-WTSS", // 0 - prototype 001
	"G-BSST", // 1 - prototype 002
	"G-AXDN", // 2 - pre-production 01
	"F-WTSA", // 3 - pre-production 02
	"F-WTSB", // 4 - production 201
	"G-BBDG", // 5 - production 202
	"F-BTSC", // 6 - production 203
	"G-BOAC", // 7 - production 204
	"F-BVFA", // 8 - production 205
	"G-BOAA", // 9 - production 206
	"F-BVFB", // 10 - production 207
	"G-BOAB", // 11 - production 208
	"F-BVFC", // 12 - production 209
	"G-BOAD", // 13 - production 210
	"F-BVFD", // 14 - production 211
	"G-BOAE", // 15 - production 212
	"F-BTSD", // 16 - production 213
	"G-BOAG", // 17 - production 214
	"F-BVFF", // 18 - production 215
	"G-BOAF", // 19 - production 216
}

const (
	// APIMajor, APIMinor, and APIPatch are the semantic version components of the
	// HTTP/gRPC API; APIMajor also indexes concordeFleet for the codename.
	APIMajor = 0
	APIMinor = 3
	APIPatch = 0

	// VoiceMajor, VoiceMinor, and VoicePatch are the semantic version components of
	// the voice subsystem, versioned independently of the API.
	VoiceMajor = 0
	VoiceMinor = 2
	VoicePatch = 0
)

// APICodename returns the Concorde registration used as the release codename for
// the current APIMajor, falling back to a "post-concorde-N" name if APIMajor is
// beyond the known fleet.
func APICodename() string {
	if APIMajor < len(concordeFleet) {
		return concordeFleet[APIMajor]
	}
	return fmt.Sprintf("post-concorde-%d", APIMajor)
}

// API returns the full API version string, e.g. "F-WTSS-0.3.0", combining the
// codename with the major.minor.patch numbers.
func API() string {
	return fmt.Sprintf("%s-%d.%d.%d", APICodename(), APIMajor, APIMinor, APIPatch)
}

// APIShort returns the abbreviated API version with only major.minor, e.g.
// "F-WTSS-0.3".
func APIShort() string {
	return fmt.Sprintf("%s-%d.%d", APICodename(), APIMajor, APIMinor)
}

// Voice returns the voice subsystem version as "major.minor.patch"; unlike API
// it carries no codename.
func Voice() string {
	return fmt.Sprintf("%d.%d.%d", VoiceMajor, VoiceMinor, VoicePatch)
}
