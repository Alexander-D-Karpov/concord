package version

import "fmt"

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
	APIMajor = 0
	APIMinor = 1
	APIPatch = 0

	VoiceMajor = 0
	VoiceMinor = 1
	VoicePatch = 0
)

func APICodename() string {
	if APIMajor < len(concordeFleet) {
		return concordeFleet[APIMajor]
	}
	return fmt.Sprintf("post-concorde-%d", APIMajor)
}

func API() string {
	return fmt.Sprintf("%s-%d.%d.%d", APICodename(), APIMajor, APIMinor, APIPatch)
}

func APIShort() string {
	return fmt.Sprintf("%s-%d.%d", APICodename(), APIMajor, APIMinor)
}

func Voice() string {
	return fmt.Sprintf("%d.%d.%d", VoiceMajor, VoiceMinor, VoicePatch)
}
