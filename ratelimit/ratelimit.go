// Package ratelimit provides a token-bucket limiter: 1 token = 1 request.
// Clients call Wait before sending; Wait blocks until a token is available,
// so no request can leave the process faster than the configured rate.
package ratelimit

import (
	"context"
	"os"
	"strconv"
	"sync"
	"time"
)

// Limiter is a token bucket with capacity = rate (1-second refill granularity,
// burst = 1 second worth of tokens). Safe for concurrent use.
type Limiter struct {
	mu       sync.Mutex
	tokens   float64
	max      float64
	rate     float64 // tokens per second
	lastFill time.Time
}

// New creates a limiter allowing `perMinute` requests per minute.
// perMinute <= 0 means unlimited (Wait returns immediately).
// The bucket starts FULL (burst = 1 minute of budget), so the first request
// fires immediately and sustained polling stays at perMinute thereafter.
func New(perMinute int) *Limiter {
	if perMinute <= 0 {
		return nil // nil *Limiter = unlimited
	}
	rate := float64(perMinute) / 60.0
	return &Limiter{
		tokens:   float64(perMinute), // full 1-minute budget as burst
		max:      float64(perMinute),
		rate:     rate,
		lastFill: time.Now(),
	}
}

// FromEnv creates a limiter from an env var holding requests-per-minute.
// Missing/invalid/unset env -> unlimited (nil).
func FromEnv(key string) *Limiter {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return New(n)
		}
	}
	return nil
}

// Wait blocks until a token is available or ctx is done.
// A nil limiter (unlimited) returns immediately.
func (l *Limiter) Wait(ctx context.Context) error {
	if l == nil {
		return nil
	}
	for {
		l.mu.Lock()
		now := time.Now()
		elapsed := now.Sub(l.lastFill).Seconds()
		l.tokens += elapsed * l.rate
		if l.tokens > l.max {
			l.tokens = l.max
		}
		l.lastFill = now
		if l.tokens >= 1 {
			l.tokens--
			l.mu.Unlock()
			return nil
		}
		// Time until the next token is ready.
		deficit := 1 - l.tokens
		wait := time.Duration(deficit / l.rate * float64(time.Second))
		l.mu.Unlock()

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
			// loop: re-check and take
		}
	}
}

// Tokens returns a snapshot of (available tokens, bucket capacity).
// Read-only — no refill happens. Used by metrics to expose how close the
// fleet is to the request budget.
func (l *Limiter) Tokens() (tokens, max float64) {
	if l == nil {
		return 0, 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.tokens, l.max
}

// TryTake takes a token without blocking. Reports success.
func (l *Limiter) TryTake() bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.tokens += now.Sub(l.lastFill).Seconds() * l.rate
	if l.tokens > l.max {
		l.tokens = l.max
	}
	l.lastFill = now
	if l.tokens >= 1 {
		l.tokens--
		return true
	}
	return false
}
