package admin

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"os"
	"strings"
)

type Role string

const (
	RoleViewer    Role = "viewer"
	RoleOperator  Role = "operator"
	RoleSensitive Role = "sensitive"
)

type sessionInfo struct {
	Authenticated bool     `json:"authenticated"`
	Role          Role     `json:"role"`
	Capabilities  []string `json:"capabilities"`
	CSRFToken     string   `json:"csrfToken,omitempty"`
	AuthMethod    string   `json:"authMethod,omitempty"`
}

type authenticator struct {
	enabled   bool
	viewer    string
	operator  string
	sensitive string
}

func newAuthenticatorFromEnvironment(enabled bool) authenticator {
	return authenticator{
		enabled: enabled,
		viewer:  os.Getenv("RELAY_LIFELINE_VIEWER_KEY"), operator: os.Getenv("RELAY_LIFELINE_ADMIN_KEY"),
		sensitive: os.Getenv("RELAY_LIFELINE_SENSITIVE_KEY"),
	}
}

func (a authenticator) authenticate(request *http.Request) (Role, bool) {
	if !a.enabled {
		return "", false
	}
	authorization := request.Header.Get("Authorization")
	if !strings.HasPrefix(authorization, "Bearer ") {
		return "", false
	}
	provided := strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer "))
	return a.authenticateKey(provided)
}

func (a authenticator) authenticateKey(provided string) (Role, bool) {
	if !a.enabled {
		return "", false
	}
	role := Role("")
	if a.viewer != "" && secureEqual(provided, a.viewer) {
		role = RoleViewer
	}
	if a.operator != "" && secureEqual(provided, a.operator) {
		role = RoleOperator
	}
	if a.sensitive != "" && secureEqual(provided, a.sensitive) {
		role = RoleSensitive
	}
	return role, role != ""
}

func secureEqual(left, right string) bool {
	leftHash := sha256.Sum256([]byte(left))
	rightHash := sha256.Sum256([]byte(right))
	return subtle.ConstantTimeCompare(leftHash[:], rightHash[:]) == 1
}

func (r Role) allows(required Role) bool { return roleRank(r) >= roleRank(required) }

func roleRank(role Role) int {
	switch role {
	case RoleViewer:
		return 1
	case RoleOperator:
		return 2
	case RoleSensitive:
		return 3
	default:
		return 0
	}
}

func sessionFor(role Role) sessionInfo {
	capabilities := []string{"view"}
	if role.allows(RoleOperator) {
		capabilities = append(capabilities, "operate")
	}
	if role.allows(RoleSensitive) {
		capabilities = append(capabilities, "sensitive")
	}
	return sessionInfo{Authenticated: true, Role: role, Capabilities: capabilities}
}

func requiredRole(request *http.Request, path string) Role {
	if request.Method == http.MethodGet && strings.HasPrefix(path, "/captures/") && strings.HasSuffix(path, "/download") && request.URL.Query().Get("mode") == "raw" {
		return RoleSensitive
	}
	if request.Method == http.MethodGet || path == "/session" {
		return RoleViewer
	}
	return RoleOperator
}
