package capture

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/areasong/relay-lifeline/internal/config"
)

func testManager(t *testing.T) (*Manager, config.CaptureConfig, string) {
	t.Helper()
	cfg := config.Default().Capture
	cfg.StorageDir = t.TempDir()
	cfg.MaxBodySize = 1 << 20
	cfg.MaxTotalSize = 8 << 20
	cfg.MinimumFreeDisk = 64 << 20
	key := bytes.Repeat([]byte{0x42}, 32)
	encoded := base64.RawStdEncoding.EncodeToString(key)
	manager := New(func() config.CaptureConfig { return cfg }, encoded)
	if status := manager.Status(); !status.Available {
		t.Fatalf("捕获管理器不可用: %+v", status)
	}
	return manager, cfg, encoded
}

func TestCaptureLifecyclePreviewExportAndRestart(t *testing.T) {
	manager, cfg, encoded := testManager(t)
	if err := manager.Activate(1, time.Minute); err != nil {
		t.Fatal(err)
	}
	headers := http.Header{
		"Authorization":    []string{"Bearer secret-token"},
		"X-Authentication": []string{"secret-auth"},
		"Cookie":           []string{"session=secret"},
		"Content-Type":     []string{"application/json"},
		"X-Request-ID":     []string{"request-1"},
	}
	requestBody := []byte(`{"model":"gpt-test","api_key":"sk-request-secret"}`)
	id, err := manager.BeginRequest("request-1", http.MethodPost, "/v1/responses?api_key=url-secret&trace=ok", headers, requestBody)
	if err != nil || id == "" {
		t.Fatalf("开始捕获失败: id=%q err=%v", id, err)
	}
	failedBody := []byte(`{"error":{"message":"Bearer upstream-secret"}}`)
	if err := manager.RecordAttempt("request-1", 1, 503, http.Header{"Content-Type": []string{"application/json"}, "Set-Cookie": []string{"secret=1"}}, bytes.NewReader(failedBody), errors.New("request to https://user:password@example.test failed"), time.Now()); err != nil {
		t.Fatal(err)
	}
	successBody := []byte("data: {\"type\":\"response.completed\",\"token\":\"secret-value\"}\n\n")
	if err := manager.RecordAttempt("request-1", 2, 200, http.Header{"Content-Type": []string{"text/event-stream"}}, bytes.NewReader(successBody), nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := manager.Finish("request-1", "successful", 2); err != nil {
		t.Fatal(err)
	}

	preview, err := manager.Preview(id)
	if err != nil {
		t.Fatal(err)
	}
	joined := ""
	for _, part := range preview.Parts {
		joined += part.Body
	}
	if strings.Contains(joined, "sk-request-secret") || strings.Contains(joined, "upstream-secret") || strings.Contains(joined, "secret-value") {
		t.Fatalf("过滤预览泄露敏感值: %s", joined)
	}
	if _, exists := preview.Record.Request.Headers["Authorization"]; exists {
		t.Fatal("捕获元数据不应保存 Authorization")
	}
	if strings.Contains(preview.Record.Path, "url-secret") || preview.Record.Request.Headers.Get("X-Authentication") != "" || strings.Contains(preview.Record.Attempts[0].Error, "password") {
		t.Fatalf("URL、认证 Header 或错误文本未脱敏: %+v", preview.Record)
	}

	rawFiles := exportFiles(t, manager, id, "raw")
	if !bytes.Equal(rawFiles["request/body.json"], requestBody) || !bytes.Equal(rawFiles["attempts/001/body.json"], failedBody) || !bytes.Equal(rawFiles["final/body.sse"], successBody) {
		t.Fatalf("原始导出正文不完整: %v", fileNames(rawFiles))
	}
	filteredFiles := exportFiles(t, manager, id, "filtered")
	if bytes.Contains(filteredFiles["request/body.json"], []byte("sk-request-secret")) || bytes.Contains(filteredFiles["final/body.sse"], []byte("secret-value")) {
		t.Fatal("过滤导出泄露敏感值")
	}

	restarted := New(func() config.CaptureConfig { return cfg }, encoded)
	if _, err := restarted.Preview(id); err != nil {
		t.Fatalf("重启后无法读取未过期捕获: %v", err)
	}
	if err := restarted.Delete(id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cfg.StorageDir, id)); !os.IsNotExist(err) {
		t.Fatalf("删除后目录仍存在: %v", err)
	}
}

func TestActiveCaptureSerializesAttemptsAsEmptyArray(t *testing.T) {
	manager, cfg, _ := testManager(t)
	if err := manager.Activate(1, time.Minute); err != nil {
		t.Fatal(err)
	}
	id, err := manager.BeginRequest("active-request", http.MethodPost, "/v1/responses", nil, nil)
	if err != nil {
		t.Fatal(err)
	}

	records := manager.List()
	if len(records) != 1 || records[0].Attempts == nil || len(records[0].Attempts) != 0 {
		t.Fatalf("活动捕获应返回空尝试数组: %+v", records)
	}
	payload, err := json.Marshal(records)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(payload, []byte(`"attempts":[]`)) {
		t.Fatalf("API JSON 未保持空数组契约: %s", payload)
	}
	metadata, err := os.ReadFile(filepath.Join(cfg.StorageDir, id, "metadata.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(metadata, []byte(`"attempts": []`)) {
		t.Fatalf("持久化元数据未保持空数组契约: %s", metadata)
	}
}

func TestEncryptedObjectRejectsTampering(t *testing.T) {
	key := bytes.Repeat([]byte{0x11}, 32)
	var encrypted bytes.Buffer
	if _, _, _, err := encryptChunks(key, &encrypted, strings.NewReader("sensitive"), 1024); err != nil {
		t.Fatal(err)
	}
	data := encrypted.Bytes()
	data[len(data)-1] ^= 0xff
	if err := decryptChunks(key, bytes.NewReader(data), io.Discard); err == nil {
		t.Fatal("篡改后的密文不应通过认证")
	}
}

func TestCaptureUnavailableWithoutKeyDoesNotCreatePlaintext(t *testing.T) {
	cfg := config.Default().Capture
	cfg.StorageDir = t.TempDir()
	manager := New(func() config.CaptureConfig { return cfg }, "")
	if manager.Status().Available {
		t.Fatal("缺少密钥时捕获不应可用")
	}
	if err := manager.Activate(1, time.Minute); err == nil {
		t.Fatal("缺少密钥时不应允许启动捕获")
	}
	entries, err := os.ReadDir(cfg.StorageDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("缺少密钥时不应落盘任何正文")
	}
}

func TestCleanerStopsWithContext(t *testing.T) {
	manager, _, _ := testManager(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() { manager.StartCleaner(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("清理器未响应取消")
	}
}

func TestCaptureExpiresAndDeletesEncryptedObjects(t *testing.T) {
	manager, cfg, _ := testManager(t)
	base := time.Now()
	manager.now = func() time.Time { return base }
	if err := manager.Activate(1, time.Minute); err != nil {
		t.Fatal(err)
	}
	id, err := manager.BeginRequest("expiring", http.MethodPost, "/v1/responses", http.Header{"Content-Type": []string{"application/json"}}, []byte(`{"value":"captured"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.Finish("expiring", "successful", 0); err != nil {
		t.Fatal(err)
	}
	base = base.Add(cfg.Retention.Duration + time.Second)
	deleted, err := manager.DeleteExpired()
	if err != nil || deleted != 1 {
		t.Fatalf("到期删除异常: deleted=%d err=%v", deleted, err)
	}
	if _, err := os.Stat(filepath.Join(cfg.StorageDir, id)); !os.IsNotExist(err) {
		t.Fatalf("到期密文目录仍存在: %v", err)
	}
}

func TestCaptureTruncatesBodyAtConfiguredLimit(t *testing.T) {
	cfg := config.Default().Capture
	cfg.StorageDir = t.TempDir()
	cfg.MaxBodySize = 8
	cfg.MaxTotalSize = 1 << 20
	cfg.MinimumFreeDisk = 1
	key := base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{0x52}, 32))
	manager := New(func() config.CaptureConfig { return cfg }, key)
	if err := manager.Activate(1, time.Minute); err != nil {
		t.Fatal(err)
	}
	id, err := manager.BeginRequest("truncated", http.MethodPost, "/v1/responses", nil, []byte("0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	preview, err := manager.Preview(id)
	if err != nil {
		t.Fatal(err)
	}
	if !preview.Record.Request.Truncated || preview.Parts[0].Body != "01234567" {
		t.Fatalf("正文截断异常: %+v", preview)
	}
}

func TestCaptureStopsBodiesAfterAttemptLimit(t *testing.T) {
	cfg := config.Default().Capture
	cfg.StorageDir = t.TempDir()
	cfg.MaxAttemptsPerRequest = 1
	cfg.MaxBodySize = 1 << 20
	cfg.MaxTotalSize = 8 << 20
	cfg.MinimumFreeDisk = 64 << 20
	key := base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{0x62}, 32))
	manager := New(func() config.CaptureConfig { return cfg }, key)
	if err := manager.Activate(1, time.Minute); err != nil {
		t.Fatal(err)
	}
	id, err := manager.BeginRequest("limited", http.MethodPost, "/v1/responses", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 1; attempt <= 3; attempt++ {
		if err := manager.RecordAttempt("limited", attempt, 503, nil, strings.NewReader("failure"), nil, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	record, ok := manager.Get(id)
	if !ok || record.Attempts[0].Response == nil || record.Attempts[1].Response != nil || record.Attempts[2].Response != nil || len(record.Warnings) != 1 {
		t.Fatalf("尝试上限后仍保存正文: %+v", record)
	}
}

func exportFiles(t *testing.T, manager *Manager, id, mode string) map[string][]byte {
	t.Helper()
	var archive bytes.Buffer
	if err := manager.Export(id, mode, map[string]any{"id": "request-1"}, &archive); err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(archive.Bytes()), int64(archive.Len()))
	if err != nil {
		t.Fatal(err)
	}
	result := make(map[string][]byte)
	for _, file := range reader.File {
		handle, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		result[file.Name], err = io.ReadAll(handle)
		handle.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	return result
}

func fileNames(files map[string][]byte) []string {
	result := make([]string, 0, len(files))
	for name := range files {
		result = append(result, name)
	}
	return result
}
