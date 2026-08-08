package infra

import (
	"sync"
	"time"
)

const (
	// epoch is the custom Snowflake epoch (2022-01-01T00:00:00Z in ms). The
	// timestamp field counts milliseconds from this instant, extending the
	// usable ID lifetime versus the Unix epoch.
	epoch = int64(1640995200000)
	// workerIDBits is the width of the worker-ID field, allowing up to 1024
	// distinct generators (2^10).
	workerIDBits = uint(10)
	// sequenceBits is the width of the per-millisecond sequence field, allowing
	// up to 4096 IDs per worker per millisecond (2^12).
	sequenceBits = uint(12)
	// workerIDShift is the left shift applied to the worker ID so it sits just
	// above the sequence field in the 64-bit layout.
	workerIDShift = sequenceBits
	// timestampShift is the left shift applied to the timestamp so it occupies
	// the high bits above the worker-ID and sequence fields.
	timestampShift = sequenceBits + workerIDBits
	// sequenceMask masks the low sequenceBits bits; ANDing with it wraps the
	// sequence back to 0 on overflow.
	sequenceMask = int64(-1) ^ (int64(-1) << sequenceBits)
)

// SnowflakeGenerator produces unique, roughly time-ordered 64-bit IDs. All
// state is guarded by mu, so a single generator is safe for concurrent use.
type SnowflakeGenerator struct {
	mu        sync.Mutex
	workerID  int64
	sequence  int64
	timestamp int64 // last millisecond timestamp an ID was minted for
}

// NewSnowflakeGenerator returns a generator stamping IDs with workerID, which
// the caller must keep unique across concurrently running instances (0..1023)
// to guarantee global uniqueness.
func NewSnowflakeGenerator(workerID int64) *SnowflakeGenerator {
	return &SnowflakeGenerator{
		workerID: workerID,
	}
}

// Generate returns the next ID: timestamp | workerID | sequence. It is
// lock-serialized. When the 4096-per-ms sequence overflows within the same
// millisecond it spin-waits (busy-loops on the clock) until the next
// millisecond rather than blocking or returning an error.
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

// ExtractTimestamp recovers the creation time encoded in id by shifting out the
// worker and sequence fields and re-adding the custom epoch. Because IDs are
// time-ordered, this also gives their relative age.
func (s *SnowflakeGenerator) ExtractTimestamp(id int64) time.Time {
	timestamp := (id >> timestampShift) + epoch
	return time.UnixMilli(timestamp)
}
