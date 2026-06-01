//go:build !linux

package api

// statfsTotalLinux is a no-op on non-Linux platforms. The caller falls back to
// the settings table value.
func statfsTotalLinux(_ string) int64 { return 0 }
