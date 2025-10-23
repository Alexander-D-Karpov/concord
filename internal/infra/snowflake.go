package infra

import (
	"sync"
	"time"
)

const (
	epoch          = int64(1640995200000)
	workerIDBits   = uint(10)
	sequenceBits   = uint(12)
	workerIDShift  = sequenceBits
	timestampShift = sequenceBits + workerIDBits
	sequenceMask   = int64(-1) ^ (int64(-1) << sequenceBits)
)

type SnowflakeGenerator struct {
	mu        sync.Mutex
	workerID  int64
	sequence  int64
	timestamp int64
}

func NewSnowflakeGenerator(workerID int64) *SnowflakeGenerator {
	return &SnowflakeGenerator{
		workerID: workerID,
	}
}

func (s *SnowflakeGenerator) Generate() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UnixMilli()

	if now == s.timestamp {
		s.sequence = (s.sequence + 1) & sequenceMask
		if s.sequence == 0 {
			for now <= s.timestamp {
				now = time.Now().UnixMilli()
			}
		}
	} else {
		s.sequence = 0
	}

	s.timestamp = now

	id := ((now - epoch) << timestampShift) |
		(s.workerID << workerIDShift) |
		s.sequence

	return id
}

func (s *SnowflakeGenerator) ExtractTimestamp(id int64) time.Time {
	timestamp := (id >> timestampShift) + epoch
	return time.UnixMilli(timestamp)
}
