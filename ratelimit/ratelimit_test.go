package ratelimit

import "testing"

func TestTokens(t *testing.T) {
	l := New(60)
	tokens, max := l.Tokens()
	if max != 60 {
		t.Fatalf("max = %v, want 60", max)
	}
	if tokens < 59 || tokens > 60 {
		t.Fatalf("tokens = %v, want ~60 (bucket starts full)", tokens)
	}
	// A take reduces the snapshot.
	if !l.TryTake() {
		t.Fatal("TryTake should succeed on a full bucket")
	}
	tokens, _ = l.Tokens()
	if tokens < 58.9 || tokens > 60 {
		t.Fatalf("tokens after take = %v, want just under 59", tokens)
	}
}

func TestTokensNilLimiter(t *testing.T) {
	var l *Limiter
	tokens, max := l.Tokens()
	if tokens != 0 || max != 0 {
		t.Fatalf("nil limiter: tokens=%v max=%v, want 0/0", tokens, max)
	}
}
