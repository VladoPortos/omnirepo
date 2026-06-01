//go:build bench

// Package gitbench contains the git memory benchmark harness.
// It samples /proc/<pid>/status VmRSS at 50 ms intervals and enforces the
// hard gate: peak_rss < 3 * repo_bytes_on_disk.
package gitbench

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// RSSSample records a single reading from /proc/<pid>/status.
type RSSSample struct {
	At     time.Time
	VmRSS  int64 // bytes
	VmPeak int64 // bytes
}

// StartSampler polls /proc/<pid>/status every interval and sends samples
// on the returned channel until ctx is cancelled or the process exits.
// The channel is buffered (4096 entries) to avoid blocking the sampler.
func StartSampler(ctx context.Context, pid int, interval time.Duration) <-chan RSSSample {
	ch := make(chan RSSSample, 4096)
	go func() {
		defer close(ch)
		statusPath := fmt.Sprintf("/proc/%d/status", pid)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				data, err := os.ReadFile(statusPath)
				if err != nil {
					return // process exited
				}
				rss := parseFieldKB(data, "VmRSS:") * 1024
				peak := parseFieldKB(data, "VmPeak:") * 1024
				ch <- RSSSample{At: time.Now(), VmRSS: rss, VmPeak: peak}
			}
		}
	}()
	return ch
}

// parseFieldKB scans /proc/<pid>/status output for a line starting with
// fieldName (e.g. "VmRSS:") and returns the value in kB. Returns 0 if
// the field is not found.
func parseFieldKB(data []byte, fieldName string) int64 {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, fieldName) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			return 0
		}
		val, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0
		}
		return val
	}
	return 0
}
