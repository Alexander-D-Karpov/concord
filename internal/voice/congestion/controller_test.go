package congestion

import (
	"testing"
	"time"
)

func TestAllowPLIThrottles(t *testing.T) {
	c := NewController(DefaultConfig())
	t0 := time.Unix(1_700_000_000, 0)
	if !c.AllowPLI(42, t0) {
		t.Fatal("first PLI should be allowed")
	}
	if c.AllowPLI(42, t0.Add(100*time.Millisecond)) {
		t.Fatal("PLI within min interval should be blocked")
	}
	if !c.AllowPLI(42, t0.Add(800*time.Millisecond)) {
		t.Fatal("PLI after min interval should be allowed")
	}
	if !c.AllowPLI(99, t0.Add(100*time.Millisecond)) {
		t.Fatal("different SSRC has its own bucket")
	}
}

func TestPrunePLI(t *testing.T) {
	c := NewController(DefaultConfig())
	t0 := time.Unix(1_700_000_000, 0)
	c.AllowPLI(7, t0)
	c.Prune(t0.Add(10 * time.Second))
	if !c.AllowPLI(7, t0.Add(10*time.Second)) {
		t.Fatal("stale PLI entry should have been pruned")
	}
}

func TestEvaluateBitrateFirstEvalSilent(t *testing.T) {
	c := NewController(DefaultConfig())
	t0 := time.Unix(1_700_000_000, 0)
	target, reason, changed := c.EvaluateBitrate(1, 2 /* video */, 0, t0)
	if changed {
		t.Fatal("first eval must not emit a hint")
	}
	if target != DefaultConfig().VideoCeil || reason != ReasonSteady {
		t.Fatalf("first eval should init to ceiling: target=%d reason=%d", target, reason)
	}
}

func TestEvaluateBitrateStepsDownOnLoss(t *testing.T) {
	c := NewController(DefaultConfig())
	t0 := time.Unix(1_700_000_000, 0)
	c.EvaluateBitrate(1, 2, 0, t0) // init to 2_500_000
	c.ObserveRR(1, 100, 0.10 /* 10% loss */, 0, t0)
	target, reason, changed := c.EvaluateBitrate(1, 2, 0, t0.Add(time.Second))
	if !changed || reason != ReasonLossDown {
		t.Fatalf("expected loss-down, got changed=%v reason=%d", changed, reason)
	}
	if target != 2_000_000 { // 2_500_000 * 0.8
		t.Fatalf("want 2_000_000, got %d", target)
	}
}

func TestEvaluateBitrateRecoversAfterSustainedLowLoss(t *testing.T) {
	c := NewController(DefaultConfig())
	t0 := time.Unix(1_700_000_000, 0)
	c.EvaluateBitrate(1, 2, 0, t0)
	c.ObserveRR(1, 100, 0.10, 0, t0)
	c.EvaluateBitrate(1, 2, 0, t0.Add(time.Second)) // -> 2_000_000
	// now clean loss, sustained
	c.ObserveRR(1, 100, 0.0, 0, t0.Add(2*time.Second))
	// first low-loss eval: starts the clock, no step
	_, _, changed := c.EvaluateBitrate(1, 2, 0, t0.Add(2*time.Second))
	if changed {
		t.Fatal("up-step must wait for LowLossHold")
	}
	// after 5s sustained low loss: step up
	target, reason, changed := c.EvaluateBitrate(1, 2, 0, t0.Add(8*time.Second))
	if !changed || reason != ReasonRecoverUp {
		t.Fatalf("expected recover-up, got changed=%v reason=%d", changed, reason)
	}
	if target != 2_160_000 { // 2_000_000 * 1.08
		t.Fatalf("want 2_160_000, got %d", target)
	}
}

func TestEvaluateBitrateRateLimited(t *testing.T) {
	c := NewController(DefaultConfig())
	t0 := time.Unix(1_700_000_000, 0)
	c.EvaluateBitrate(1, 2, 0, t0)
	c.ObserveRR(1, 100, 0.10, 0, t0)
	_, _, changed := c.EvaluateBitrate(1, 2, 0, t0.Add(500*time.Millisecond))
	if changed {
		t.Fatal("eval within EvalInterval must be a no-op")
	}
}

func TestWorstLossExcludesStaleReporters(t *testing.T) {
	c := NewController(DefaultConfig())
	t0 := time.Unix(1_700_000_000, 0)
	c.EvaluateBitrate(1, 2, 0, t0)
	c.ObserveRR(1, 100, 0.20, 0, t0)                    // stale by eval time
	c.ObserveRR(1, 200, 0.02, 0, t0.Add(7*time.Second)) // fresh, below LossDown
	_, _, changed := c.EvaluateBitrate(1, 2, 0, t0.Add(7*time.Second))
	if changed {
		t.Fatal("stale 20% reporter should be ignored; fresh 2% must not trigger down-step")
	}
}

func TestEvaluateBitrateClampsToAdvertisedCeiling(t *testing.T) {
	c := NewController(DefaultConfig())
	t0 := time.Unix(1_700_000_000, 0)
	// advertised ceiling 500_000 for a video stream
	target, _, _ := c.EvaluateBitrate(1, 2, 500_000, t0)
	if target != 500_000 {
		t.Fatalf("init should clamp to advertised ceiling, got %d", target)
	}
}
