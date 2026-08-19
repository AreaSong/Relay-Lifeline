package admin

import (
	"encoding/json"
	"testing"

	"github.com/areasong/relay-lifeline/internal/config"
)

func FuzzMapOIDCRole(f *testing.F) {
	f.Add([]byte(`{"groups":["viewer"]}`))
	f.Add([]byte(`{"groups":["operator","sensitive"]}`))
	f.Add([]byte(`{"realm":{"groups":["viewer"]}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 64*1024 {
			data = data[:64*1024]
		}
		var claims map[string]any
		if json.Unmarshal(data, &claims) != nil || claims == nil {
			return
		}
		cfg := config.Default().ManagementSecurity.OIDC
		role, authorized := mapOIDCRole(claims, cfg)
		if !authorized {
			return
		}
		switch role {
		case RoleViewer, RoleOperator, RoleSensitive:
		default:
			t.Fatalf("unknown authorized OIDC role %q", role)
		}
	})
}
