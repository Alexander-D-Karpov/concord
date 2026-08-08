// Package congestion holds the voice SFU's congestion-control state:
// per-sender RR aggregation, bitrate targets, and PLI rate-limit buckets.
// It is a pure, lock-guarded state machine — it never performs I/O. Every
// time-dependent method takes an explicit now for deterministic testing.
package congestion

import (
	"sync"
	"time"
)

// Reason codes returned by EvaluateBitrate to explain a target change; carried
// on the wire in the BitrateHint so the sender can log why its budget moved.
const (
	// ReasonSteady: no change this evaluation (also used when a change was suppressed).
	ReasonSteady uint8 = 0
	// ReasonLossDown: target was cut because measured loss exceeded LossDown.
	ReasonLossDown uint8 = 1
	// ReasonRecoverUp: target was raised after a sustained low-loss hold.
	ReasonRecoverUp uint8 = 2
)

// codecOpus mirrors protocol.CodecOpus; kept local to avoid an import dependency.
const codecOpus uint8 = 1

// Config holds the congestion controller's tunables: the AIMD loss thresholds
// and multipliers for bitrate, per-codec floor/ceiling clamps, PLI/reporter
// timing, and the simulcast tier-selection parameters. Loss values are fractions
// (0..1); bitrates are bits per second. Use DefaultConfig for sane defaults.
type Config struct {
	LossDown     float64       // loss above this cuts the target (multiply by DownFactor)
	DownFactor   float64       // multiplicative decrease applied on high loss (<1)
	LossUp       float64       // loss below this is "low" and, once held, allows increase
	UpFactor     float64       // multiplicative increase applied on recovery (>1)
	LowLossHold  time.Duration // how long loss must stay low before increasing
	EvalInterval time.Duration // minimum spacing between bitrate re-evaluations

	OpusFloor  uint32
	OpusCeil   uint32
	VideoFloor uint32
	VideoCeil  uint32

	PLIMinInterval time.Duration
	ReporterExpiry time.Duration

	// Simulcast layer selection.
	LayerExpiry     time.Duration // how long an unseen layer stays "produced"
	TierLossHigh    float64       // loss above this forces the receiver to layer 1
	TierLossMed     float64       // loss above this caps the receiver at layer 2
	TierRecoverHold time.Duration // min spacing between one-tier recovery steps
}

// DefaultConfig returns the production-tuned controller settings (5% loss cuts
// to 80%, sustained <1% loss over 5s grows by 8%; Opus 8–64 kbps, video
// 100 kbps–2.5 Mbps).
func DefaultConfig() Config {
	return Config{
		LossDown:       0.05,
		DownFactor:     0.80,
		LossUp:         0.01,
		UpFactor:       1.08,
		LowLossHold:    5 * time.Second,
		EvalInterval:   time.Second,
		OpusFloor:      8_000,
		OpusCeil:       64_000,
		VideoFloor:     100_000,
		VideoCeil:      2_500_000,
		PLIMinInterval: 750 * time.Millisecond,
		ReporterExpiry: 5 * time.Second,

		LayerExpiry:     2 * time.Second,
		TierLossHigh:    0.10,
		TierLossMed:     0.05,
		TierRecoverHold: 3 * time.Second,
	}
}

// rrSample is one receiver's most recent report about a stream: its fractional
// loss (0..1), jitter, and when it arrived (for expiry via ReporterExpiry).
type rrSample struct {
	fractionLost float64
	jitter       uint32
	lastSeen     time.Time
}

// streamRR aggregates the latest receiver report per receiver for one stream.
type streamRR struct {
	reporters map[uint32]rrSample // receiver session ID -> sample
}

// bitrateState is the per-stream AIMD target in bits/sec plus its pacing state.
// inited is false until the first evaluation seeds current at the ceiling;
// lowLossSince is the zero time unless a low-loss recovery window is open.
type bitrateState struct {
	current      uint32
	lastEvalAt   time.Time
	lowLossSince time.Time
	inited       bool
}

// maxLayer mirrors protocol.LayerLarge; kept local to avoid an import dependency.
const maxLayer uint8 = 3

// tierKey identifies a per-(stream, receiver) simulcast ceiling entry.
type tierKey struct {
	stream   uint32
	receiver uint32
}

// tierState tracks one receiver's congestion ceiling (max layer it may pull) for
// a stream, with timestamps that pace recovery and drive pruning.
type tierState struct {
	ceiling  uint8
	lastMove time.Time // last actual ceiling change; paces recovery
	lastSeen time.Time // last StepTiers visit; drives pruning
}

// Controller is the SFU's per-node congestion state machine. It is pure and does
// no I/O; every mutating method takes an explicit now for deterministic tests.
// All maps are guarded by mu — hot-path reads take RLock, and every method may
// be called concurrently. Maps are keyed by stream SSRC (and receiver id where
// noted) and are pruned by Prune.
type Controller struct {
	cfg Config
	mu  sync.RWMutex

	rr      map[uint32]*streamRR
	bitrate map[uint32]*bitrateState
	pli     map[uint32]time.Time
	layers  map[uint32]map[uint8]time.Time // stream SSRC -> layer -> last seen
	tier    map[tierKey]*tierState         // (stream, receiver) -> congestion ceiling
}

// NewController returns a Controller with cfg and all state maps allocated empty.
func NewController(cfg Config) *Controller {
	return &Controller{
		cfg:     cfg,
		rr:      make(map[uint32]*streamRR),
		bitrate: make(map[uint32]*bitrateState),
		pli:     make(map[uint32]time.Time),
		layers:  make(map[uint32]map[uint8]time.Time),
		tier:    make(map[tierKey]*tierState),
	}
}

// AllowPLI reports whether a PLI to targetSSRC is permitted now, and records
// the emission time when it returns true.
func (c *Controller) AllowPLI(targetSSRC uint32, now time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if last, ok := c.pli[targetSSRC]; ok && now.Sub(last) < c.cfg.PLIMinInterval {
		return false
	}
	c.pli[targetSSRC] = now
	return true
}

// ObserveRR records a receiver report about streamSSRC from a receiver session.
func (c *Controller) ObserveRR(streamSSRC, reporterID uint32, fractionLost float64, jitter uint32, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := c.rr[streamSSRC]
	if s == nil {
		s = &streamRR{reporters: make(map[uint32]rrSample)}
		c.rr[streamSSRC] = s
	}
	s.reporters[reporterID] = rrSample{fractionLost: fractionLost, jitter: jitter, lastSeen: now}
}

// worstLossLocked returns the highest fractional loss reported by any non-expired
// reporter for streamSSRC (0 if none), so the bitrate reacts to the worst-off
// receiver. Caller must hold c.mu.
func (c *Controller) worstLossLocked(streamSSRC uint32, now time.Time) float64 {
	s := c.rr[streamSSRC]
	if s == nil {
		return 0
	}
	worst := 0.0
	for _, r := range s.reporters {
		if now.Sub(r.lastSeen) > c.cfg.ReporterExpiry {
			continue
		}
		if r.fractionLost > worst {
			worst = r.fractionLost
		}
	}
	return worst
}

// ObserveLayer records that streamSSRC is currently producing the given
// simulcast layer. The router calls this for every video packet so it can later
// forward the best layer each receiver can take. Hot-path cheap: it skips the
// write while the layer's timestamp is still fresh.
func (c *Controller) ObserveLayer(streamSSRC uint32, layer uint8, now time.Time) {
	c.mu.RLock()
	if lo := c.layers[streamSSRC]; lo != nil {
		if last, ok := lo[layer]; ok && now.Sub(last) < c.cfg.LayerExpiry/2 {
			c.mu.RUnlock()
			return
		}
	}
	c.mu.RUnlock()

	c.mu.Lock()
	lo := c.layers[streamSSRC]
	if lo == nil {
		lo = make(map[uint8]time.Time)
		c.layers[streamSSRC] = lo
	}
	lo[layer] = now
	c.mu.Unlock()
}

// TargetLayer picks the simulcast layer to forward for streamSSRC at a
// receiver's effective ceiling effTier: the highest observed layer <= effTier,
// or — if none qualify — the lowest observed layer, so the receiver is never
// starved. ok is false when the stream has produced no (unexpired) layers, in
// which case the caller forwards unconditionally.
//
// NOTE: this assumes each layer carries its own sequence space (per-layer
// SSRCs). On a single-SSRC/shared-sequence stream, dropping layers here opens
// sequence gaps the receiver reports as loss — a feedback spiral. It is inert
// for today's single-layer senders (only layer 0 observed => target 0 => all
// forwarded) and must stay off until the client ships per-layer simulcast.
func (c *Controller) TargetLayer(streamSSRC uint32, effTier uint8, now time.Time) (uint8, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	lo := c.layers[streamSSRC]
	if len(lo) == 0 {
		return 0, false
	}
	best, lowest := -1, -1
	for layer, last := range lo {
		if now.Sub(last) > c.cfg.LayerExpiry {
			continue
		}
		l := int(layer)
		if lowest < 0 || l < lowest {
			lowest = l
		}
		if layer <= effTier && l > best {
			best = l
		}
	}
	switch {
	case best >= 0:
		return uint8(best), true
	case lowest >= 0:
		return uint8(lowest), true
	default:
		return 0, false
	}
}

// EffectiveTier is the ceiling a receiver may pull for streamSSRC: the smaller
// of the client's own preference (prefCeiling) and the congestion ceiling last
// computed by StepTiers. It is a pure read (RLock only) so it stays cheap on the
// media hot path, where the router calls it once per destination per packet. An
// unknown receiver starts optimistic (maxLayer) until StepTiers evaluates it.
func (c *Controller) EffectiveTier(streamSSRC, receiverID uint32, prefCeiling uint8) uint8 {
	c.mu.RLock()
	ceiling := maxLayer
	if ts := c.tier[tierKey{stream: streamSSRC, receiver: receiverID}]; ts != nil {
		ceiling = ts.ceiling
	}
	c.mu.RUnlock()
	if ceiling < prefCeiling {
		return ceiling
	}
	return prefCeiling
}

// StepTiers advances every (stream, receiver) simulcast ceiling from the latest
// RR loss. Call it periodically off the hot path (the voice server ticks it once
// a second); EffectiveTier then serves the result with a read lock. The ceiling
// drops immediately on loss and recovers one tier per TierRecoverHold so a lossy
// receiver is sent fewer layers without oscillating.
func (c *Controller) StepTiers(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for streamSSRC, s := range c.rr {
		for receiverID, r := range s.reporters {
			loss := 0.0
			if now.Sub(r.lastSeen) <= c.cfg.ReporterExpiry {
				loss = r.fractionLost
			}
			c.stepTierLocked(streamSSRC, receiverID, loss, now)
		}
	}
}

// stepTierLocked advances one (stream, receiver) ceiling from loss: loss above
// TierLossHigh forces layer 1 and above TierLossMed caps at layer 2 (both
// immediate), while low loss recovers one layer at most once per TierRecoverHold.
// It lazily creates the entry (starting optimistic at maxLayer) and refreshes
// lastSeen for pruning. Caller must hold c.mu.
func (c *Controller) stepTierLocked(streamSSRC, receiverID uint32, loss float64, now time.Time) {
	key := tierKey{stream: streamSSRC, receiver: receiverID}
	ts := c.tier[key]
	if ts == nil {
		ts = &tierState{ceiling: maxLayer, lastMove: now}
		c.tier[key] = ts
	}
	ts.lastSeen = now
	switch {
	case loss > c.cfg.TierLossHigh:
		if ts.ceiling > 1 {
			ts.ceiling = 1
			ts.lastMove = now
		}
	case loss > c.cfg.TierLossMed:
		if ts.ceiling > 2 {
			ts.ceiling = 2
			ts.lastMove = now
		}
	default:
		if ts.ceiling < maxLayer && now.Sub(ts.lastMove) >= c.cfg.TierRecoverHold {
			ts.ceiling++
			ts.lastMove = now
		}
	}
}

// floorCeil resolves the bitrate clamp bounds for a codec: Opus vs. video floor
// from cfg, and ceilingBps as the ceiling unless it is 0, in which case the
// codec's configured ceiling is used.
func (c *Controller) floorCeil(codec uint8, ceilingBps uint32) (uint32, uint32) {
	if codec == codecOpus {
		ceil := ceilingBps
		if ceil == 0 {
			ceil = c.cfg.OpusCeil
		}
		return c.cfg.OpusFloor, ceil
	}
	ceil := ceilingBps
	if ceil == 0 {
		ceil = c.cfg.VideoCeil
	}
	return c.cfg.VideoFloor, ceil
}

// clampU32 rounds v (toward zero) into [lo, hi], tolerating an inverted range by
// treating hi<lo as hi=lo so the result is always at least lo.
func clampU32(v float64, lo, hi uint32) uint32 {
	if hi < lo {
		hi = lo
	}
	if v < float64(lo) {
		return lo
	}
	if v > float64(hi) {
		return hi
	}
	return uint32(v)
}

// EvaluateBitrate applies the step rules for streamSSRC, rate-limited to
// EvalInterval. It returns the current target, a reason code, and whether the
// target changed (only then should a BitrateHint be sent).
func (c *Controller) EvaluateBitrate(streamSSRC uint32, codec uint8, ceilingBps uint32, now time.Time) (uint32, uint8, bool) {
	c.mu.RLock()
	if st := c.bitrate[streamSSRC]; st != nil && st.inited && now.Sub(st.lastEvalAt) < c.cfg.EvalInterval {
		cur := st.current
		c.mu.RUnlock()
		return cur, ReasonSteady, false
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()

	st := c.bitrate[streamSSRC]
	if st == nil {
		st = &bitrateState{}
		c.bitrate[streamSSRC] = st
	}
	if st.inited && now.Sub(st.lastEvalAt) < c.cfg.EvalInterval {
		return st.current, ReasonSteady, false
	}
	floor, ceil := c.floorCeil(codec, ceilingBps)

	if !st.inited {
		st.inited = true
		st.lastEvalAt = now
		st.current = ceil
		st.lowLossSince = time.Time{}
		return st.current, ReasonSteady, false
	}
	st.lastEvalAt = now

	loss := c.worstLossLocked(streamSSRC, now)
	prev := st.current
	reason := ReasonSteady

	switch {
	case loss > c.cfg.LossDown:
		st.current = clampU32(float64(st.current)*c.cfg.DownFactor, floor, ceil)
		st.lowLossSince = time.Time{}
		reason = ReasonLossDown
	case loss < c.cfg.LossUp:
		if st.lowLossSince.IsZero() {
			st.lowLossSince = now
		} else if now.Sub(st.lowLossSince) >= c.cfg.LowLossHold {
			st.current = clampU32(float64(st.current)*c.cfg.UpFactor, floor, ceil)
			st.lowLossSince = now
			reason = ReasonRecoverUp
		}
	default:
		st.lowLossSince = time.Time{}
	}

	if st.current < floor {
		st.current = floor
	}
	if st.current > ceil {
		st.current = ceil
	}

	changed := st.current != prev
	if !changed {
		reason = ReasonSteady
	}
	return st.current, reason, changed
}

// Prune drops stale PLI buckets, expired RR reporters, and idle bitrate state.
func (c *Controller) Prune(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for ssrc, last := range c.pli {
		if now.Sub(last) > c.cfg.PLIMinInterval*4 {
			delete(c.pli, ssrc)
		}
	}
	for ssrc, s := range c.rr {
		for id, r := range s.reporters {
			if now.Sub(r.lastSeen) > c.cfg.ReporterExpiry {
				delete(s.reporters, id)
			}
		}
		if len(s.reporters) == 0 {
			delete(c.rr, ssrc)
		}
	}
	for ssrc, st := range c.bitrate {
		if now.Sub(st.lastEvalAt) > 30*time.Second {
			delete(c.bitrate, ssrc)
		}
	}
	for ssrc, lo := range c.layers {
		for layer, last := range lo {
			if now.Sub(last) > c.cfg.LayerExpiry {
				delete(lo, layer)
			}
		}
		if len(lo) == 0 {
			delete(c.layers, ssrc)
		}
	}
	for key, ts := range c.tier {
		if now.Sub(ts.lastSeen) > 30*time.Second {
			delete(c.tier, key)
		}
	}
}
