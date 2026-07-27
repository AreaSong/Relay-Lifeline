package diagnostics

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/areasong/relay-lifeline/internal/config"
)

func TestDiagnosticsUseTCPWithoutCallingModelAPI(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	receivedBytes := make(chan int, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			_ = connection.SetReadDeadline(time.Now().Add(time.Second))
			buffer := make([]byte, 256)
			count, _ := connection.Read(buffer)
			connection.Close()
			receivedBytes <- count
		}
	}()
	t.Setenv("RELAY_LIFELINE_ADMIN_KEY", "123456789012345678901234")
	t.Setenv("RELAY_LIFELINE_SENSITIVE_KEY", "abcdefghijklmnopqrstuvwx")
	t.Setenv("RELAY_LIFELINE_CAPTURE_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := config.Default()
	cfg.Upstream.BaseURL = "http://" + listener.Addr().String()
	cfg.Stream.TempDir = t.TempDir()
	cfg.Capture.StorageDir = t.TempDir()
	if err := cfg.Save(path); err != nil {
		t.Fatal(err)
	}
	service := New(config.NewStore(path, cfg), "test", time.Now())
	report := service.Run(context.Background())
	if !report.Healthy {
		t.Fatalf("诊断失败: %+v", report.Checks)
	}
	select {
	case count := <-receivedBytes:
		if count != 0 {
			t.Fatalf("诊断向上游发送了 %d 字节，预期只建立 TCP 连接", count)
		}
	case <-time.After(time.Second):
		t.Fatal("未执行 TCP 连通性检查")
	}
	entries, _ := os.ReadDir(cfg.Stream.TempDir)
	if len(entries) != 0 {
		t.Fatalf("诊断临时文件未清理: %+v", entries)
	}
}

func TestRedactedConfigRemovesURLSecrets(t *testing.T) {
	cfg := config.Default()
	cfg.Upstream.BaseURL = "https://user:secret@example.com/v1?token=hidden"
	cfg.Notifications.WebhookURL = "https://hooks.example.com/private-token"
	encoded, _ := json.Marshal(RedactedConfig(cfg))
	text := string(encoded)
	for _, secret := range []string{"secret", "hidden", "private-token"} {
		if strings.Contains(text, secret) {
			t.Fatalf("诊断配置泄露敏感值 %q: %s", secret, text)
		}
	}
}

func TestDiagnosticsCanRenderEnglish(t *testing.T) {
	t.Setenv("RELAY_LIFELINE_ADMIN_KEY", "123456789012345678901234")
	t.Setenv("RELAY_LIFELINE_SENSITIVE_KEY", "abcdefghijklmnopqrstuvwx")
	t.Setenv("RELAY_LIFELINE_CAPTURE_KEY", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	cfg := config.Default()
	cfg.Upstream.BaseURL = "http://127.0.0.1:1"
	cfg.Stream.TempDir = t.TempDir()
	cfg.Capture.StorageDir = t.TempDir()
	report := New(config.NewStore("", cfg), "test", time.Now()).Run(context.Background(), "en-US", "zh-CN")
	if len(report.Checks) == 0 || report.Checks[0].Name != "Lifeline service" || report.Checks[0].Message != "Service process is running" {
		t.Fatalf("英文诊断异常: %+v", report.Checks)
	}
	if report.Checks[0].NameCode != "diagnostic.service.name" || report.Checks[0].MessageCode != "diagnostic.service.running" {
		t.Fatalf("诊断缺少稳定消息代码: %+v", report.Checks[0])
	}
}
