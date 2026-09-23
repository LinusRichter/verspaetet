package shared

import (
	"hash/fnv"
	"strings"
)

// slugify derives a compact, stable slug from a station name:
// lowercase, umlauts transliterated, non-alphanumerics dropped.
// This is NOT the old bahnhof.de slug — it only serves as a stable,
// unique, URL-safe identifier for the UI/API.
func slugify(name string) string {
	repl := strings.NewReplacer(
		"ä", "ae", "ö", "oe", "ü", "ue", "ß", "ss",
		"Ä", "ae", "Ö", "oe", "Ü", "ue",
	)
	s := strings.ToLower(repl.Replace(name))
	var b strings.Builder
	lastDash := true // avoid leading dash
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// FetchOffsetHash maps a key deterministically to a raw FNV-1a 64 hash.
// Callers apply their own modulo (cadence) — never bake the modulo in here,
// or `% cadence` at call sites becomes a no-op for cadence > 30 (the
// 2026-09-23 incident: 10 of 40 slots stayed empty because the hash was
// already reduced mod 30).
func FetchOffsetHash(key string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(key))
	return h.Sum64()
}
