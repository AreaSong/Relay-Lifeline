package buildinfo

import (
	"testing"
	"time"
)

func TestSnapshotContainsStableContractIdentity(t *testing.T) {
	started := time.Now().Add(-2 * time.Second)
	info := New("2.0.0", "abc123", "2026-07-27T00:00:00Z", "relay-lifeline:test", started).Snapshot(2)
	if info.Version != "2.0.0" || info.Revision != "abc123" || info.AdminAPIVersion != "2" || info.ConfigSchemaVersion != 2 {
		t.Fatalf("运行身份不完整: %+v", info)
	}
	if info.GoVersion == "" || info.Platform == "" || info.UptimeSeconds < 1 {
		t.Fatalf("运行环境信息不完整: %+v", info)
	}
	if info.Process.PID < 1 || info.Process.Goroutines < 1 || info.Process.CPUCount < 1 || info.Process.GOMAXPROCS < 1 {
		t.Fatalf("进程身份信息不完整: %+v", info.Process)
	}
	if info.Process.SystemMemoryBytes == 0 || info.Process.SampledAt.IsZero() {
		t.Fatalf("进程资源快照不完整: %+v", info.Process)
	}
}
