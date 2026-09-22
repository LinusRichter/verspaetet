package ratelimit

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestUnlimitedNil(t *testing.T) {
	var l *Limiter // nil = unlimited
	for i := 0; i < 1000; i++ {
		if err := l.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBurstThenThrottle(t *testing.T) {
	// New(60): full bucket = 60 tokens burst, then refill 1/s.
	// The 60st call consumes the burst; the 61st must wait ~1s.
	l := New(60)
	start := time.Now()
	for i := 0; i < 60; i++ {
		if err := l.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Errorf("60 burst calls took %v, want instant", d)
	}
	if err := l.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	d := time.Since(start)
	// First refill after draining: ~1s for the 61st token.
	if d < 900*time.Millisecond {
		t.Errorf("61st call came after %v, want >= ~900ms (throttled)", d)
	}
	if d > 3*time.Second {
		t.Errorf("61st call took %v, unreasonably long", d)
	}
}

func TestConcurrentRequestsThrottled(t *testing.T) {
	// New(2): 2-token burst, then 1 token / 30s. 10 concurrent waits:
	// only the 2 burst tokens may complete within 2s; the rest must be
	// throttled (still waiting), proving the limiter engages under load.
	l := New(2)
	var fast atomic.Int64
	startPoint = time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = l.Wait(context.Background())
			if time.Since(startPoint) < 2*time.Second {
				fast.Add(1)
			}
		}()
	}
	time.Sleep(2 * time.Second)
	if got := fast.Load(); got > 2 {
		t.Errorf("within 2s completed %d, want <= 2 (burst)", got)
	}
	// NOTE: the remaining goroutines are still blocked on 30s refills here.
	// We deliberately do NOT wait for them — the test asserts the throttle
	// by observing that they did NOT finish. Leaked waiters are harmless
	// (the limiter is not tied to any process lifecycle).
}

var startPoint time.Time

func TestFromEnv(t *testing.T) {
	t.Setenv("TEST_RATE", "60")
	if l := FromEnv("TEST_RATE"); l == nil {
		t.Error("FromEnv should create a limiter for valid value")
	} else if l.rate != 1.0 {
		t.Errorf("rate = %v, want 1.0 (60/min)", l.rate)
	}
	if l := FromEnv("TEST_RATE_UNSET"); l != nil {
		t.Error("FromEnv should return nil (unlimited) for unset env")
	}
	t.Setenv("TEST_RATE_BAD", "banana")
	if l := FromEnv("TEST_RATE_BAD"); l != nil {
		t.Error("FromEnv should return nil (unlimited) for invalid env")
	}
	t.Setenv("TEST_RATE_ZERO", "0")
	if l := FromEnv("TEST_RATE_ZERO"); l != nil {
		t.Error("FromEnv should return nil (unlimited) for 0")
	}
}

func TestTryTake(t *testing.T) {
	l := New(2) // 2-token burst
	if !l.TryTake() {
		t.Error("first TryTake should succeed (burst)")
	}
	if !l.TryTake() {
		t.Error("second TryTake should succeed (burst)")
	}
	if l.TryTake() {
		t.Error("third TryTake should fail once burst is drained")
	}
}

func TestContextCancel(t *testing.T) {
	// Drain the burst first, then a Wait on a cancelled context must return
	// the ctx error rather than blocking for the next token.
	l := New(1)
	if !l.TryTake() {
		t.Fatal("burst token should be available")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := l.Wait(ctx); err == nil {
		t.Error("Wait should return ctx error when cancelled while waiting")
	}
}
