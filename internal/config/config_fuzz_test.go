package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func FuzzLoadWithSourceVersion(f *testing.F) {
	f.Add([]byte("schema-version: 5\n"))
	f.Add([]byte("schema-version: 4\n"))
	f.Add([]byte("schema-version: 1\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 256*1024 {
			data = data[:256*1024]
		}
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		cfg, _, err := LoadWithSourceVersion(path)
		if err != nil {
			return
		}
		if cfg.SchemaVersion != CurrentSchemaVersion {
			t.Fatalf("successful load returned schema %d", cfg.SchemaVersion)
		}
		if err := cfg.Validate(); err != nil {
			t.Fatalf("successful load returned invalid config: %v", err)
		}
	})
}

func FuzzConfigJSONScalars(f *testing.F) {
	f.Add([]byte(`"1s"`))
	f.Add([]byte(`"1MiB"`))
	f.Add([]byte(`null`))
	f.Fuzz(func(t *testing.T, data []byte) {
		var duration Duration
		_ = json.Unmarshal(data, &duration)
		var size ByteSize
		_ = json.Unmarshal(data, &size)
	})
}
