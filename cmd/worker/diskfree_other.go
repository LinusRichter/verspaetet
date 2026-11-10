//go:build !linux

package main

// diskFreeBytes is unknown on non-Linux platforms: ok=false makes the
// caller skip the free-space check.
func diskFreeBytes(path string) (int64, bool) {
	return 0, false
}
