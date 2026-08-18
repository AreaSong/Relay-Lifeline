package proxy

import (
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func TestReplayBufferSpillsAndDeletes(t *testing.T) {
	directory := t.TempDir()
	buffer := NewReplayBuffer(4, directory)
	if _, err := io.WriteString(buffer, "0123456789"); err != nil {
		t.Fatal(err)
	}
	if buffer.file == nil {
		t.Fatal("应转存临时文件")
	}
	name := buffer.file.Name()
	info, err := os.Stat(name)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("临时文件权限 = %o，期望 600", info.Mode().Perm())
	}
	var output strings.Builder
	if _, err := buffer.WriteTo(&output); err != nil {
		t.Fatal(err)
	}
	if output.String() != "0123456789" {
		t.Fatalf("缓存内容错误: %s", output.String())
	}
	if err := buffer.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(name); !os.IsNotExist(err) {
		t.Fatal("临时文件未删除")
	}
}

func TestReplayBufferEnforcesLimitsAndReleasesBudget(t *testing.T) {
	budget := &cacheBudget{}
	first := newLimitedReplayBuffer(16, 8, 10, 0, t.TempDir(), budget)
	if _, err := io.WriteString(first, "12345678"); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(first, "9"); !errors.Is(err, errResponseBodyTooLarge) {
		t.Fatalf("单响应上限未生效: %v", err)
	}
	second := newLimitedReplayBuffer(16, 8, 10, 0, t.TempDir(), budget)
	if _, err := io.WriteString(second, "123"); !errors.Is(err, errCacheBudgetExceeded) {
		t.Fatalf("总缓存预算未生效: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(second, "123"); err != nil {
		t.Fatalf("缓存关闭后预算未释放: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestReplayBufferPreservesDiskReserveAndReleasesReservation(t *testing.T) {
	budget := &cacheBudget{}
	buffer := newLimitedReplayBuffer(1, 8, 8, int64(^uint64(0)>>1), t.TempDir(), budget)
	if _, err := io.WriteString(buffer, "12"); !errors.Is(err, errCacheDiskSpace) {
		t.Fatalf("最小剩余磁盘保护未生效: %v", err)
	}
	if budget.used != 0 {
		t.Fatalf("写入失败后缓存预算未释放: %d", budget.used)
	}
	if err := buffer.Close(); err != nil {
		t.Fatal(err)
	}
}
