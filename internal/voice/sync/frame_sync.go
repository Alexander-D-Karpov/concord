package sync

import (
	"sync"
	"time"
)

type StreamClock struct {
	mu          sync.RWMutex
	baseTime    time.Time
	audioOffset int64
	videoOffset int64
	sampleRate  int
	frameRate   int
}

func NewStreamClock(sampleRate, frameRate int) *StreamClock {
	return &StreamClock{
		baseTime:   time.Now(),
		sampleRate: sampleRate,
		frameRate:  frameRate,
	}
}

func (c *StreamClock) AudioPTS(timestamp uint32) int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return int64(timestamp)*1000000/int64(c.sampleRate) + c.audioOffset
}

func (c *StreamClock) VideoPTS(timestamp uint32) int64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return int64(timestamp)*1000000/int64(90000) + c.videoOffset
}

func (c *StreamClock) SyncStreams(audioPTS, videoPTS int64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	drift := audioPTS - videoPTS
	if drift > 40000 || drift < -40000 {
		c.videoOffset += drift / 2
		c.audioOffset -= drift / 2
	}
}

type SyncBuffer struct {
	mu          sync.Mutex
	audioQueue  []*TimedPacket
	videoQueue  []*TimedPacket
	targetDelay time.Duration
}

type TimedPacket struct {
	Data      []byte
	PTS       int64
	Arrival   time.Time
	MediaType string
}

func NewSyncBuffer(targetDelay time.Duration) *SyncBuffer {
	return &SyncBuffer{
		audioQueue:  make([]*TimedPacket, 0, 100),
		videoQueue:  make([]*TimedPacket, 0, 30),
		targetDelay: targetDelay,
	}
}

func (b *SyncBuffer) AddAudio(data []byte, pts int64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.audioQueue = append(b.audioQueue, &TimedPacket{
		Data:      data,
		PTS:       pts,
		Arrival:   time.Now(),
		MediaType: "audio",
	})

	if len(b.audioQueue) > 100 {
		b.audioQueue = b.audioQueue[1:]
	}
}

func (b *SyncBuffer) AddVideo(data []byte, pts int64) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.videoQueue = append(b.videoQueue, &TimedPacket{
		Data:      data,
		PTS:       pts,
		Arrival:   time.Now(),
		MediaType: "video",
	})

	if len(b.videoQueue) > 30 {
		b.videoQueue = b.videoQueue[1:]
	}
}

func (b *SyncBuffer) GetSyncedPackets() ([]*TimedPacket, []*TimedPacket) {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	var readyAudio, readyVideo []*TimedPacket

	cutoff := 0
	for i, p := range b.audioQueue {
		if now.Sub(p.Arrival) >= b.targetDelay {
			readyAudio = append(readyAudio, p)
			cutoff = i + 1
		}
	}
	b.audioQueue = b.audioQueue[cutoff:]

	cutoff = 0
	for i, p := range b.videoQueue {
		if now.Sub(p.Arrival) >= b.targetDelay {
			readyVideo = append(readyVideo, p)
			cutoff = i + 1
		}
	}
	b.videoQueue = b.videoQueue[cutoff:]

	return readyAudio, readyVideo
}
