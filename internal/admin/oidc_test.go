package admin

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/state"
	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

func TestOIDCAuthorizationCodePKCECreatesAuthorizedSession(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: privateKey}, &jose.SignerOptions{ExtraHeaders: map[jose.HeaderKey]any{jose.HeaderKey("kid"): "test-key"}})
	if err != nil {
		t.Fatal(err)
	}
	publicJWK := jose.JSONWebKey{Key: &privateKey.PublicKey, KeyID: "test-key", Algorithm: string(jose.RS256), Use: "sig"}

	var issuerURL string
	var nonce string
	var expectedChallenge string
	var tokenMu sync.Mutex
	idp := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/.well-known/openid-configuration":
			writeTestJSON(writer, map[string]any{
				"issuer": issuerURL, "authorization_endpoint": issuerURL + "/authorize", "token_endpoint": issuerURL + "/token",
				"jwks_uri": issuerURL + "/jwks", "response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
				"id_token_signing_alg_values_supported": []string{"RS256"}, "code_challenge_methods_supported": []string{"S256"},
			})
		case "/jwks":
			writeTestJSON(writer, jose.JSONWebKeySet{Keys: []jose.JSONWebKey{publicJWK}})
		case "/token":
			if err := request.ParseForm(); err != nil {
				t.Error(err)
			}
			verifier := request.Form.Get("code_verifier")
			if verifier == "" || pkceChallenge(verifier) != expectedChallenge {
				t.Errorf("PKCE verifier 不匹配: verifier=%q", verifier)
			}
			tokenMu.Lock()
			claimsNonce := nonce
			tokenMu.Unlock()
			now := time.Now()
			rawToken, signErr := jwt.Signed(signer).Claims(map[string]any{
				"iss": issuerURL, "sub": "operator-123", "aud": []string{"relay-client"},
				"iat": now.Unix(), "exp": now.Add(time.Hour).Unix(), "nonce": claimsNonce, "groups": []string{"relay-operators"},
			}).Serialize()
			if signErr != nil {
				t.Error(signErr)
			}
			writeTestJSON(writer, map[string]any{"access_token": "not-persisted", "token_type": "Bearer", "expires_in": 3600, "id_token": rawToken})
		default:
			http.NotFound(writer, request)
		}
	}))
	defer idp.Close()
	issuerURL = idp.URL

	cfg := config.Default()
	cfg.ManagementSecurity.LocalAccessEnabled = false
	cfg.ManagementSecurity.OIDC = config.OIDCConfig{
		Enabled: true, IssuerURL: issuerURL, ClientID: "relay-client", RedirectURL: "https://relay.example.test/admin/api/session/oidc/callback",
		RoleClaim: "groups", SigningAlgorithms: []string{"RS256"}, OperatorValues: []string{"relay-operators"},
	}
	store := config.NewStore("", cfg)
	handler := New(store, state.NewRegistry(), state.NewController())
	t.Setenv("RELAY_LIFELINE_OIDC_CLIENT_SECRET", "test-secret")
	if err := handler.ConfigureOIDC(context.Background()); err != nil {
		t.Fatal(err)
	}

	start := httptest.NewRequest(http.MethodGet, "/admin/api/session/oidc/start", nil)
	startRecorder := httptest.NewRecorder()
	handler.ServeHTTP(startRecorder, start)
	if startRecorder.Code != http.StatusFound {
		t.Fatalf("OIDC start 状态异常: %d %s", startRecorder.Code, startRecorder.Body.String())
	}
	authorizationURL, err := url.Parse(startRecorder.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	query := authorizationURL.Query()
	if query.Get("state") == "" || query.Get("nonce") == "" || query.Get("code_challenge_method") != "S256" || query.Get("code_challenge") == "" {
		t.Fatalf("OIDC 授权参数不完整: %s", authorizationURL)
	}
	tokenMu.Lock()
	nonce = query.Get("nonce")
	expectedChallenge = query.Get("code_challenge")
	tokenMu.Unlock()
	cookies := startRecorder.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].Path != "/admin/api/session/oidc/callback" {
		t.Fatalf("OIDC transaction Cookie 防护异常: %+v", cookies)
	}

	callback := httptest.NewRequest(http.MethodGet, "/admin/api/session/oidc/callback?state="+url.QueryEscape(query.Get("state"))+"&code=test-code", nil)
	callback.AddCookie(cookies[0])
	callbackRecorder := httptest.NewRecorder()
	handler.ServeHTTP(callbackRecorder, callback)
	if callbackRecorder.Code != http.StatusFound || callbackRecorder.Header().Get("Location") != "/admin/" {
		t.Fatalf("OIDC callback 失败: %d %s", callbackRecorder.Code, callbackRecorder.Body.String())
	}
	var sessionCookie *http.Cookie
	for _, cookie := range callbackRecorder.Result().Cookies() {
		if cookie.Name == managementSessionCookie {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil || !sessionCookie.HttpOnly || !sessionCookie.Secure {
		t.Fatalf("OIDC session Cookie 缺失或不安全: %+v", callbackRecorder.Result().Cookies())
	}
	sessionRequest := httptest.NewRequest(http.MethodGet, "/admin/api/session", nil)
	sessionRequest.AddCookie(sessionCookie)
	sessionRecorder := httptest.NewRecorder()
	handler.ServeHTTP(sessionRecorder, sessionRequest)
	if sessionRecorder.Code != http.StatusOK || !strings.Contains(sessionRecorder.Body.String(), `"role":"operator"`) || !strings.Contains(sessionRecorder.Body.String(), `"authMethod":"oidc"`) {
		t.Fatalf("OIDC 会话权限异常: %d %s", sessionRecorder.Code, sessionRecorder.Body.String())
	}

	replay := httptest.NewRequest(http.MethodGet, callback.URL.String(), nil)
	replay.AddCookie(cookies[0])
	replayRecorder := httptest.NewRecorder()
	handler.ServeHTTP(replayRecorder, replay)
	if replayRecorder.Header().Get("Location") != "/admin/?auth=oidc_failed" {
		t.Fatalf("OIDC state 重放未拒绝: %d %s", replayRecorder.Code, replayRecorder.Body.String())
	}
}

func TestMapOIDCRoleRejectsMalformedClaimsAndUsesHighestRole(t *testing.T) {
	cfg := config.OIDCConfig{RoleClaim: "realm.groups", ViewerValues: []string{"view"}, OperatorValues: []string{"operate"}, SensitiveValues: []string{"sensitive"}}
	role, ok := mapOIDCRole(map[string]any{"realm": map[string]any{"groups": []any{"view", "sensitive"}}}, cfg)
	if !ok || role != RoleSensitive {
		t.Fatalf("OIDC 最高权限映射异常: role=%q ok=%v", role, ok)
	}
	for _, claims := range []map[string]any{
		{"realm": map[string]any{"groups": []any{1}}},
		{"realm": map[string]any{"groups": []any{"unknown"}}},
		{"realm": "invalid"},
	} {
		if _, accepted := mapOIDCRole(claims, cfg); accepted {
			t.Fatalf("不安全 claims 被接受: %+v", claims)
		}
	}
}

func writeTestJSON(writer http.ResponseWriter, value any) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(value)
}

func pkceChallenge(verifier string) string {
	digest := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}
