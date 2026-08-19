//go:build !(darwin || linux || windows)

package disk

import "errors"

var errUnsupported = errors.New("disk space lookup is unsupported on this platform")

// AvailableBytes reports an explicit error on platforms without a supported
// filesystem query implementation, so callers fail closed instead of guessing.
func AvailableBytes(string) (int64, error) {
	return 0, errUnsupported
}
