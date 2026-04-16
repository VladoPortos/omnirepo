package api

import "golang.org/x/sys/unix"

// statfsTotalLinux returns the total filesystem capacity in bytes for the
// partition containing path. Returns 0 on any error.
func statfsTotalLinux(path string) int64 {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0
	}
	// Total = blocks * block-size.
	return int64(stat.Blocks) * stat.Bsize
}
