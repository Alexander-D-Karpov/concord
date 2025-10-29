package qos

import (
	"container/heap"
	"sync"
	"time"
)

type Packet struct {
	Sequence  uint16
	Timestamp uint32
	Data      []byte
	Arrival   time.Time
}

type packetHeap []Packet

func (h packetHeap) Len() int           { return len(h) }
func (h packetHeap) Less(i, j int) bool { return h[i].Sequence < h[j].Sequence }
func (h packetHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }

func (h *packetHeap) Push(x interface{}) {
	*h = append(*h, x.(Packet))
}

func (h *packetHeap) Pop() interface{} {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[0 : n-1]
	return x
}

type JitterBuffer struct {
	buffer       *packetHeap
	maxSize      int
	targetDelay  time.Duration
	nextSequence uint16
	mu           sync.Mutex
}

func NewJitterBuffer(maxSize int, targetDelay time.Duration) *JitterBuffer {
	h := &packetHeap{}
	heap.Init(h)

	return &JitterBuffer{
		buffer:      h,
		maxSize:     maxSize,
		targetDelay: targetDelay,
	}
}

func (jb *JitterBuffer) Add(packet Packet) {
	jb.mu.Lock()
	defer jb.mu.Unlock()

	if jb.buffer.Len() >= jb.maxSize {
		return
	}

	heap.Push(jb.buffer, packet)
}

func (jb *JitterBuffer) Get() *Packet {
	jb.mu.Lock()
	defer jb.mu.Unlock()

	if jb.buffer.Len() == 0 {
		return nil
	}

	oldest := (*jb.buffer)[0]
	if time.Since(oldest.Arrival) < jb.targetDelay {
		return nil
	}

	packet := heap.Pop(jb.buffer).(Packet)
	jb.nextSequence = packet.Sequence + 1

	return &packet
}

func (jb *JitterBuffer) Size() int {
	jb.mu.Lock()
	defer jb.mu.Unlock()
	return jb.buffer.Len()
}
