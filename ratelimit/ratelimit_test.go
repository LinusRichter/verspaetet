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
	// 60/min = 1/s. First call instant (burst token), subsequent calls
	// must be ~1s apart.
	l := New(60)
	start := time.Now()
	if err := l.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Errorf("first (burst) call took %v, want instant", d)
	}
	if err := l.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	d := time.Since(start)
	// Second token should take ~1s to refill.
	if d < 900*time.Millisecond {
		t.Errorf("second call came after %v, want >= ~900ms (throttled)", d)
	}
	if d > 2*time.Second {
		t.Errorf("second call took %v, unreasonably long", d)
	}
}

func TestConcurrentRequestsThrottled(t *testing.T) {
	// 10 goroutines hitting a 120/min limiter (2/s): 10 tokens should take
	// ~4-5s total. Proves the limiter serializes concurrent callers.
	l := New(120)
	var wg sync.WaitGroup
	var count atomic.Int64
	start := time.Now()
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := l.Wait(context.Background()); err == nil {
				count.Add(1)
			}
		}()
	}
	wg.Wait()
	d := time.Since(start)
	if count.Load() != 10 {
		t.Fatalf("only %d of 10 waits succeeded", count.Load())
	}
	// 10 tokens at 2/s: first is instant (burst), 9 more take ~4.5s.
	if d < 3*time.Second {
		t.Errorf("10 concurrent waits finished in %v, want >= ~3s (throttled)", d)
	}
	if d > 15*time.Second {
		t.Errorf("10 concurrent waits took %v, unreasonably long", d)
	}
}

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
	l := New(600) // 10/s
	if !l.TryTake() {
		t.Error("first TryTake should succeed (burst)")
	}
	// Drain the burst; eventually TryTake must fail without blocking.
	drained := l.TryTake()
	for i := 0; i < 20 && drained; i++ {
		drained = l.TryTake()
	}
	if drained {
		t.Error("TryTake should eventually fail once burst is drained")
	}
}

func TestContextCancel(t *testing.T) {
	l := New(1) // 1/60s — extremely slow
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := l.Wait(ctx); err == nil {
		t.Error("Wait should return ctx error when cancelled while waiting")
	}
}