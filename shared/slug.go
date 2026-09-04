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

// FetchOffset maps a key (slug) deterministically to a minute slot [0, 30).
// FNV-1a: same key → same slot across restarts, giving an even fetch spread.
func FetchOffset(key string) int {
	h := fnv.New64a()
	h.Write([]byte(key))
	return int(h.Sum64() % 30)
}