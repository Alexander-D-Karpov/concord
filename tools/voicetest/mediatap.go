package main

import "sync"

// mediaTap holds the most recent decoded media from the monitored publisher. The
// monitor bot's receive loop writes it; the TUI (or the headless render-dump) reads
// it. Only the latest frame is kept — this is a live monitor, not lossless playback.
type mediaTap struct {
	mu          sync.Mutex
	audioPCM    []byte
	video       *videoFrame
	audioFrames uint64
	videoFrames uint64
	decodeErrs  uint64
	lastMarker  int
}

// putAudio stores the latest decoded PCM frame and increments the audio frame count.
func (m *mediaTap) putAudio(pcm []byte) {
	m.mu.Lock()
	m.audioPCM = pcm
	m.audioFrames++
	m.mu.Unlock()
}

// putVideo stores the latest decoded video frame, increments the video frame count,
// and records the frame's integrity marker for end-to-end verification.
func (m *mediaTap) putVideo(f *videoFrame) {
	m.mu.Lock()
	m.video = f
	m.videoFrames++
	m.lastMarker = frameMarker(f)
	m.mu.Unlock()
}

// decodeErr increments the count of packets that failed to decrypt or decode.
func (m *mediaTap) decodeErr() {
	m.mu.Lock()
	m.decodeErrs++
	m.mu.Unlock()
}

// mediaSnapshot is an immutable view of the tap for one render pass.
type mediaSnapshot struct {
	audioPCM    []byte
	video       *videoFrame
	audioFrames uint64
	videoFrames uint64
	decodeErrs  uint64
	lastMarker  int
}

// snapshot returns a consistent copy of the tap's current state under lock, for one
// render or assertion pass without holding the mutex during rendering.
func (m *mediaTap) snapshot() mediaSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return mediaSnapshot{
		audioPCM:    m.audioPCM,
		video:       m.video,
		audioFrames: m.audioFrames,
		videoFrames: m.videoFrames,
		decodeErrs:  m.decodeErrs,
		lastMarker:  m.lastMarker,
	}
}
