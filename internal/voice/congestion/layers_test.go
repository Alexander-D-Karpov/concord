package congestion

import (
	"testing"
	"time"
)

var layerBase = time.Unix(1_700_000_000, 0)

// The forwarded layer is the highest produced layer at or below the ceiling.
func TestTargetLayerHighestUnderCeiling(t *testing.T) {
	c := NewController(DefaultConfig())
	for _, l := range []uint8{0, 1, 2, 3} {
		c.ObserveLayer(100, l, layerBase)
	}
	if got, ok := c.TargetLayer(100, 2, layerBase); !ok || got != 2 {
		t.Fatalf("ceiling 2: want layer 2, got %d ok=%v", got, ok)
	}
	if got, ok := c.TargetLayer(100, 3, layerBase); !ok || got != 3 {
		t.Fatalf("ceiling 3: want layer 3, got %d ok=%v", got, ok)
	}
}

// With no layer at or below the ceiling, forward the lowest produced layer
// rather than starving the receiver; once layers expire, ok is false so the
// router forwards unconditionally.
func TestTargetLayerLowestFallbackAndExpiry(t *testing.T) {
	cfg := DefaultConfig()
	c := NewController(cfg)
	c.ObserveLayer(100, 2, layerBase)
	c.ObserveLayer(100, 3, layerBase)

	if got, ok := c.TargetLayer(100, 1, layerBase); !ok || got != 2 {
		t.Fatalf("nothing <= ceiling: want lowest 2, got %d ok=%v", got, ok)
	}

	later := layerBase.Add(cfg.LayerExpiry + time.Second)
	if _, ok := c.TargetLayer(100, 3, later); ok {
		t.Fatal("expired layers must yield ok=false")
	}
}

// A single-layer sender (today's clients) never has a packet dropped: only
// layer 0 is produced, so the target is always 0.
func TestTargetLayerSingleLayerInert(t *testing.T) {
	c := NewController(DefaultConfig())
	c.ObserveLayer(100, 0, layerBase)
	if got, ok := c.TargetLayer(100, 3, layerBase); !ok || got != 0 {
		t.Fatalf("single layer: want 0, got %d ok=%v", got, ok)
	}
}

// EffectiveTier caps at the client's preference when the link is clean and
// drops after StepTiers sees high loss from that receiver. The hot-path read is
// pure; stepping happens off it.
func TestEffectiveTierCapsAndDowngrades(t *testing.T) {
	c := NewController(DefaultConfig())
	// Clean link, no state yet: capped by the client preference.
	if got := c.EffectiveTier(100, 7, 2); got != 2 {
		t.Fatalf("clean link: want pref 2, got %d", got)
	}
	c.ObserveRR(100, 7, 0.20, 0, layerBase)
	c.StepTiers(layerBase)
	if got := c.EffectiveTier(100, 7, 3); got != 1 {
		t.Fatalf("high loss: want ceiling 1, got %d", got)
	}
}

// After loss clears, the ceiling recovers one tier per TierRecoverHold.
func TestEffectiveTierRecoversOneTierPerHold(t *testing.T) {
	cfg := DefaultConfig()
	c := NewController(cfg)
	c.ObserveRR(100, 7, 0.20, 0, layerBase)
	c.StepTiers(layerBase)
	if got := c.EffectiveTier(100, 7, 3); got != 1 {
		t.Fatalf("want 1 under loss, got %d", got)
	}
	c.ObserveRR(100, 7, 0.0, 0, layerBase.Add(time.Second))

	t1 := layerBase.Add(cfg.TierRecoverHold + time.Second)
	c.StepTiers(t1)
	if got := c.EffectiveTier(100, 7, 3); got != 2 {
		t.Fatalf("first recovery step: want 2, got %d", got)
	}
	t2 := t1.Add(cfg.TierRecoverHold + time.Second)
	c.StepTiers(t2)
	if got := c.EffectiveTier(100, 7, 3); got != 3 {
		t.Fatalf("second recovery step: want 3, got %d", got)
	}
}

// StepTiers must not resurrect a departed receiver's ceiling forever: once RR
// stops, the entry ages out of the tier table.
func TestStepTiersEntryPrunes(t *testing.T) {
	cfg := DefaultConfig()
	c := NewController(cfg)
	c.ObserveRR(100, 7, 0.20, 0, layerBase)
	c.StepTiers(layerBase)
	c.Prune(layerBase.Add(31 * time.Second)) // no StepTiers since layerBase
	if _, ok := c.tier[tierKey{stream: 100, receiver: 7}]; ok {
		t.Fatal("stale tier entry should have been pruned")
	}
}
