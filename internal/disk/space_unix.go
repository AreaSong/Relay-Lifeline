//go:build darwin || linux

package disk

import (
	"math"

	"golang.org/x/sys/unix"
)

// AvailableBytes returns the bytes available to the current user on path.
func AvailableBytes(path string) (int64, error) {
	var stats unix.Statfs_t
	if err := unix.Statfs(path, &stats); err != nil {
		return 0, err
	}
	blocks, size := uint64(stats.Bavail), uint64(stats.Bsize)
	if size != 0 && blocks > math.MaxUint64/size {
		return math.MaxInt64, nil
	}
	available := blocks * size
	if available > math.MaxInt64 {
		return math.MaxInt64, nil
	}
	return int64(available), nil
}
