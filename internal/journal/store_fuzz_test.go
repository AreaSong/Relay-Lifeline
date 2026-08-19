package journal

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func FuzzVerifyJournal(f *testing.F) {
	f.Add([]byte(""))
	f.Add([]byte("{}\n"))
	f.Add([]byte("{\"schemaVersion\":1}\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1024*1024 {
			data = data[:1024*1024]
		}
		path := filepath.Join(t.TempDir(), "events.jsonl")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		_, _ = Verify(path)
	})
}

func FuzzRebuildChain(f *testing.F) {
	f.Add([]byte(`{"value":"seed"}`))
	f.Add([]byte(`null`))
	f.Fuzz(func(t *testing.T, payload []byte) {
		if len(payload) > 64*1024 || !json.Valid(payload) {
			payload = []byte(`null`)
		}
		entries := rebuildChain([]Entry{
			{SchemaVersion: SchemaVersion, EntityID: "entity", Type: "event", Payload: append([]byte(nil), payload...)},
			{SchemaVersion: SchemaVersion, EntityID: "entity", Type: "event-2", Payload: []byte(`{"ok":true}`)},
		})
		data := make([]byte, 0, 512)
		for _, entry := range entries {
			line, err := json.Marshal(entry)
			if err != nil {
				t.Fatal(err)
			}
			data = append(data, line...)
			data = append(data, '\n')
		}
		path := filepath.Join(t.TempDir(), "rebuilt.jsonl")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Verify(path); err != nil {
			t.Fatalf("rebuilt chain failed verification: %v", err)
		}
	})
}
