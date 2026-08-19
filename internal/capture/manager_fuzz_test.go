package capture

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	"github.com/areasong/relay-lifeline/internal/config"
)

func FuzzCaptureMetadataInitialize(f *testing.F) {
	f.Add([]byte(`{"id":"record","requestId":"request","state":"active"}`))
	f.Add([]byte(`{}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64*1024 {
			data = data[:64*1024]
		}
		root := t.TempDir()
		recordDir := filepath.Join(root, "record")
		if err := os.MkdirAll(recordDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(recordDir, "metadata.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
		cfg := config.Default().Capture
		cfg.StorageDir = root
		key := base64.RawStdEncoding.EncodeToString(bytes.Repeat([]byte{0x29}, 32))
		manager := New(func() config.CaptureConfig { return cfg }, key)
		if manager.Status().Available {
			if record, ok := manager.Get("record"); ok && record.ID != "record" {
				t.Fatalf("loaded record has mismatched id: %q", record.ID)
			}
		}
	})
}
