package udp

import (
	"sync"
	"sync/atomic"
)

type packetBuffer struct {
	buf  []byte
	n    int
	refs atomic.Int32
	pool *sync.Pool
}

func newPacketPool() sync.Pool {
	p := sync.Pool{}
	p.New = func() interface{} {
		return &packetBuffer{
			buf:  make([]byte, maxPacketLen),
			pool: &p,
		}
	}
	return p
}

func (p *packetBuffer) PrepareForRead() []byte {
	p.n = 0
	p.refs.Store(1)
	return p.buf[:cap(p.buf)]
}

func (p *packetBuffer) SetLen(n int) {
	p.n = n
}

func (p *packetBuffer) Bytes() []byte {
	return p.buf[:p.n]
}

func (p *packetBuffer) Retain() {
	p.refs.Add(1)
}

func (p *packetBuffer) Release() {
	if p.refs.Add(-1) == 0 {
		p.n = 0
		p.pool.Put(p)
	}
}
