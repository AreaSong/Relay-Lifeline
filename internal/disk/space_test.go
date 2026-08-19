package disk

import (
	"os"
	"testing"
)

func TestAvailableBytesForTempDirectory(t *testing.T) {
	available, err := AvailableBytes(os.TempDir())
	if err != nil {
		t.Skipf("platform does not provide disk space lookup: %v", err)
	}
	if available <= 0 {
		t.Fatalf("available disk must be positive, got %d", available)
	}
}
