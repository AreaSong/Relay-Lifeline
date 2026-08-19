package capture

import (
	"encoding/base64"
	"encoding/json"
	"testing"
)

func FuzzParseKeyring(f *testing.F) {
	key := base64.RawStdEncoding.EncodeToString(make([]byte, 32))
	f.Add("active", key, "{\"active\":\""+key+"\"}")
	f.Add("", "", "")
	f.Fuzz(func(t *testing.T, activeID, legacy, ring string) {
		if len(activeID) > 256 || len(legacy) > 4096 || len(ring) > 64*1024 {
			return
		}
		result, err := ParseKeyring(activeID, legacy, ring)
		if err != nil {
			return
		}
		if result.ActiveID == "" || len(result.Keys[result.ActiveID]) != 32 {
			t.Fatalf("invalid successful keyring: active=%q keys=%v", result.ActiveID, result.IDs())
		}
	})
}

func FuzzKeyringJSONShape(f *testing.F) {
	f.Add([]byte(`{"active":"value"}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64*1024 {
			data = data[:64*1024]
		}
		var values map[string]string
		_ = json.Unmarshal(data, &values)
	})
}
