package buildinfo

import (
	"testing"
	"time"
)

func TestSnapshotContainsStableContractIdentity(t *testing.T) {
	started := time.Now().Add(-2 * time.Second)
	info := New("0.4.0", "abc123", "2026-07-27T00:00:00Z", "relay-lifeline:test", started).Snapshot(1)
	if info.Version != "0.4.0" || info.Revision != "abc123" || info.AdminAPIVersion != "1" || info.ConfigSchemaVersion != 1 {
		t.Fatalf("运行身份不完整: %+v", info)
	}
	if info.GoVersion == "" || info.Platform == "" || info.UptimeSeconds < 1 {
		t.Fatalf("运行环境信息不完整: %+v", info)
	}
}
