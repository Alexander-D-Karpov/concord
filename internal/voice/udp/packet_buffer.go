package udp

import (
	"sync"
	"sync/atomic"
)

// packetBuffer is a pooled, reference-counted receive buffer. buf is fixed at
// maxPacketLen; n is the valid length of the last read. refs is the live
// reference count, and pool is where the buffer returns when refs hits zero.
type packetBuffer struct {
	buf  []byte
	n    int
	refs atomic.Int32
	pool *sync.Pool
}

// newPacketPool returns a sync.Pool that mints packetBuffers each pre-sized to
// maxPacketLen and wired back to the pool for recycling on Release.
func newPacketPool() *sync.Pool {
	p := &sync.Pool{}
	p.New = func() interface{} {
		return &packetBuffer{
			buf:  make([]byte, maxPacketLen),
			pool: p,
		}
	}
	return p
}

// PrepareForRead resets the buffer for a fresh datagram: it clears the length and
// sets the reference count to 1 (the reader's own reference), returning the full
// backing slice to read into.
func (p *packetBuffer) PrepareForRead() []byte {
	p.n = 0
	p.refs.Store(1)
	return p.buf[:cap(p.buf)]
}

// SetLen records how many bytes the last read produced; Bytes reports this slice.
func (p *packetBuffer) SetLen(n int) {
	p.n = n
}

// Bytes returns the valid portion (first n bytes) of the buffer. The slice aliases
// the pooled backing array, so it is only valid while a reference is held.
func (p *packetBuffer) Bytes() []byte {
	return p.buf[:p.n]
}

// Retain adds a reference, keeping the buffer out of the pool while another
// consumer (e.g. the router fanning it out) still needs it.
func (p *packetBuffer) Retain() {
	p.refs.Add(1)
}

// Release drops one reference and, when the count reaches zero, returns the buffer
// to its pool. A missed Release leaks the buffer; a double Release drops it below
// zero and corrupts pool reuse.
func (p *packetBuffer) Release() {
	if p.refs.Add(-1) == 0 {
		p.n = 0
		p.pool.Put(p)
	}
}
