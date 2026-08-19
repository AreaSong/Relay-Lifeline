package admin

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/areasong/relay-lifeline/internal/config"
	"github.com/areasong/relay-lifeline/internal/l10n"
	"github.com/areasong/relay-lifeline/internal/monitoring"
	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

func (h *Handler) beginOIDC(writer http.ResponseWriter, request *http.Request, locale, fallback string) {
	if !h.oidcEnabled || h.oidc == nil {
		h.recordSecurityEvent(monitoring.SecurityEvent{Code: "admin.oidc_start", Outcome: "unavailable"})
		h.writeError(writer, http.StatusServiceUnavailable, "OIDC_UNAVAILABLE", l10n.M("api.admin.oidc_unavailable"), locale, fallback)
		return
	}
	if err := h.oidc.begin(writer, request); err != nil {
		h.recordSecurityEvent(monitoring.SecurityEvent{Code: "admin.oidc_start", Outcome: "failed"})
		h.writeError(writer, http.StatusServiceUnavailable, "OIDC_START_FAILED", l10n.M("api.admin.oidc_failed"), locale, fallback)
		return
	}
	h.recordSecurityEvent(monitoring.SecurityEvent{Code: "admin.oidc_start", Outcome: "succeeded"})
}

func (h *Handler) completeOIDC(writer http.ResponseWriter, request *http.Request) {
	if h.oidc == nil {
		h.oidcCallbackFailed(writer, request, "unavailable")
		return
	}
	identity, err := h.oidc.callback(request.Context(), request)
	if err != nil {
		h.oidcCallbackFailed(writer, request, "denied")
		return
	}
	token, session, err := h.sessions.create(identity.Role, "oidc", identity.PrincipalID, identity.ExpiresAt)
	if err != nil {
		h.oidcCallbackFailed(writer, request, "failed")
		return
	}
	setOIDCTransactionCookie(writer, request, h.oidc.config.RedirectURL, "", -1)
	forceSecure := strings.HasPrefix(h.oidc.config.RedirectURL, "https://")
	setSessionCookieSecure(writer, request, token, int(time.Until(session.ExpiresAt).Seconds()), forceSecure)
	h.recordSecurityEvent(monitoring.SecurityEvent{
		Code: "admin.oidc_session_created", Outcome: "succeeded",
		Details: map[string]any{"authMethod": "oidc", "role": identity.Role, "principalId": identity.PrincipalID},
	})
	http.Redirect(writer, request, "/admin/", http.StatusFound)
}

func (h *Handler) oidcCallbackFailed(writer http.ResponseWriter, request *http.Request, outcome string) {
	redirectURL := ""
	if h.oidc != nil {
		redirectURL = h.oidc.config.RedirectURL
	}
	setOIDCTransactionCookie(writer, request, redirectURL, "", -1)
	h.recordSecurityEvent(monitoring.SecurityEvent{Code: "admin.oidc_authentication_failed", Outcome: outcome})
	http.Redirect(writer, request, "/admin/?auth=oidc_failed", http.StatusFound)
}

const (
	oidcTransactionCookie = "relay_lifeline_oidc"
	oidcTransactionTTL    = 10 * time.Minute
	maxOIDCTransactions   = 256
)

type oidcTransaction struct {
	Verifier     string
	Nonce        string
	CookieDigest [32]byte
	ExpiresAt    time.Time
}

type oidcIdentity struct {
	Role        Role
	PrincipalID string
	ExpiresAt   time.Time
}

type oidcService struct {
	mu           sync.Mutex
	config       config.OIDCConfig
	oauth        oauth2.Config
	verifier     *oidc.IDTokenVerifier
	httpClient   *http.Client
	transactions map[string]oidcTransaction
	now          func() time.Time
}

func newOIDCService(ctx context.Context, cfg config.OIDCConfig, clientSecret string) (*oidcService, error) {
	client := &http.Client{
		Timeout: 15 * time.Second,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 3 || len(via) > 0 && (request.URL.Scheme != via[0].URL.Scheme || request.URL.Host != via[0].URL.Host) {
				return errors.New("OIDC redirect left the configured issuer origin")
			}
			return nil
		},
	}
	discoveryCtx, cancel := context.WithTimeout(context.WithValue(ctx, oauth2.HTTPClient, client), 15*time.Second)
	defer cancel()
	provider, err := oidc.NewProvider(discoveryCtx, cfg.IssuerURL)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery: %w", err)
	}
	scopes := []string{oidc.ScopeOpenID, "profile", "email"}
	seen := map[string]struct{}{oidc.ScopeOpenID: {}, "profile": {}, "email": {}}
	for _, scope := range cfg.Scopes {
		if _, exists := seen[scope]; !exists {
			scopes = append(scopes, scope)
			seen[scope] = struct{}{}
		}
	}
	return &oidcService{
		config: cfg,
		oauth: oauth2.Config{
			ClientID: cfg.ClientID, ClientSecret: clientSecret, Endpoint: provider.Endpoint(), RedirectURL: cfg.RedirectURL, Scopes: scopes,
		},
		verifier: provider.Verifier(&oidc.Config{ClientID: cfg.ClientID, SupportedSigningAlgs: append([]string(nil), cfg.SigningAlgorithms...)}), httpClient: client,
		transactions: make(map[string]oidcTransaction), now: time.Now,
	}, nil
}

func (s *oidcService) begin(writer http.ResponseWriter, request *http.Request) error {
	state, err := randomURLToken()
	if err != nil {
		return err
	}
	nonce, err := randomURLToken()
	if err != nil {
		return err
	}
	verifier, err := randomURLToken()
	if err != nil {
		return err
	}
	browserToken, err := randomURLToken()
	if err != nil {
		return err
	}
	now := s.now()
	s.mu.Lock()
	s.cleanupLocked(now)
	if len(s.transactions) >= maxOIDCTransactions {
		s.evictOldestLocked()
	}
	s.transactions[state] = oidcTransaction{
		Verifier: verifier, Nonce: nonce, CookieDigest: sha256.Sum256([]byte(browserToken)), ExpiresAt: now.Add(oidcTransactionTTL),
	}
	s.mu.Unlock()
	setOIDCTransactionCookie(writer, request, s.config.RedirectURL, browserToken, int(oidcTransactionTTL.Seconds()))
	challenge := sha256.Sum256([]byte(verifier))
	authorizationURL := s.oauth.AuthCodeURL(
		state,
		oidc.Nonce(nonce),
		oauth2.SetAuthURLParam("code_challenge", base64.RawURLEncoding.EncodeToString(challenge[:])),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)
	http.Redirect(writer, request, authorizationURL, http.StatusFound)
	return nil
}

func (s *oidcService) callback(ctx context.Context, request *http.Request) (oidcIdentity, error) {
	state := request.URL.Query().Get("state")
	code := request.URL.Query().Get("code")
	if request.URL.Query().Get("error") != "" || state == "" || code == "" || len(state) > 256 || len(code) > 8192 {
		return oidcIdentity{}, errors.New("OIDC callback was rejected")
	}
	cookie, err := request.Cookie(oidcTransactionCookie)
	if err != nil || cookie.Value == "" {
		return oidcIdentity{}, errors.New("OIDC transaction cookie is missing")
	}
	transaction, ok := s.consume(state)
	if !ok || !transaction.ExpiresAt.After(s.now()) {
		return oidcIdentity{}, errors.New("OIDC transaction is missing or expired")
	}
	providedDigest := sha256.Sum256([]byte(cookie.Value))
	if subtle.ConstantTimeCompare(providedDigest[:], transaction.CookieDigest[:]) != 1 {
		return oidcIdentity{}, errors.New("OIDC transaction cookie did not match")
	}
	oidcContext, cancel := context.WithTimeout(context.WithValue(ctx, oauth2.HTTPClient, s.httpClient), 15*time.Second)
	defer cancel()
	token, err := s.oauth.Exchange(oidcContext, code, oauth2.SetAuthURLParam("code_verifier", transaction.Verifier))
	if err != nil {
		return oidcIdentity{}, fmt.Errorf("OIDC code exchange: %w", err)
	}
	rawIDToken, ok := token.Extra("id_token").(string)
	if !ok || rawIDToken == "" || len(rawIDToken) > 64<<10 {
		return oidcIdentity{}, errors.New("OIDC response did not contain a usable ID token")
	}
	idToken, err := s.verifier.Verify(oidcContext, rawIDToken)
	if err != nil {
		return oidcIdentity{}, fmt.Errorf("OIDC ID token verification: %w", err)
	}
	if idToken.Nonce != transaction.Nonce || idToken.Subject == "" || !idToken.Expiry.After(s.now()) {
		return oidcIdentity{}, errors.New("OIDC ID token identity checks failed")
	}
	var claims map[string]any
	if err := idToken.Claims(&claims); err != nil {
		return oidcIdentity{}, errors.New("OIDC claims could not be decoded")
	}
	role, ok := mapOIDCRole(claims, s.config)
	if !ok {
		return oidcIdentity{}, errors.New("OIDC identity has no authorized role")
	}
	principalHash := sha256.Sum256([]byte(s.config.IssuerURL + "\x00" + idToken.Subject))
	return oidcIdentity{Role: role, PrincipalID: base64.RawURLEncoding.EncodeToString(principalHash[:12]), ExpiresAt: idToken.Expiry}, nil
}

func (s *oidcService) consume(state string) (oidcTransaction, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	transaction, ok := s.transactions[state]
	delete(s.transactions, state)
	return transaction, ok
}

func (s *oidcService) cleanupLocked(now time.Time) {
	for state, transaction := range s.transactions {
		if !transaction.ExpiresAt.After(now) {
			delete(s.transactions, state)
		}
	}
}

func (s *oidcService) evictOldestLocked() {
	var oldestState string
	var oldest time.Time
	for state, transaction := range s.transactions {
		if oldestState == "" || transaction.ExpiresAt.Before(oldest) {
			oldestState, oldest = state, transaction.ExpiresAt
		}
	}
	delete(s.transactions, oldestState)
}

func mapOIDCRole(claims map[string]any, cfg config.OIDCConfig) (Role, bool) {
	value, ok := nestedClaim(claims, cfg.RoleClaim)
	if !ok {
		return "", false
	}
	values, ok := oidcClaimValues(value)
	if !ok {
		return "", false
	}
	if matchesOIDCValue(values, cfg.SensitiveValues) {
		return RoleSensitive, true
	}
	if matchesOIDCValue(values, cfg.OperatorValues) {
		return RoleOperator, true
	}
	if matchesOIDCValue(values, cfg.ViewerValues) {
		return RoleViewer, true
	}
	return "", false
}

func nestedClaim(claims map[string]any, path string) (any, bool) {
	var current any = claims
	for _, part := range strings.Split(path, ".") {
		object, ok := current.(map[string]any)
		if !ok || part == "" {
			return nil, false
		}
		current, ok = object[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func oidcClaimValues(value any) ([]string, bool) {
	switch typed := value.(type) {
	case string:
		if typed == "" || len(typed) > 256 {
			return nil, false
		}
		return []string{typed}, true
	case []any:
		if len(typed) == 0 || len(typed) > 256 {
			return nil, false
		}
		values := make([]string, 0, len(typed))
		for _, item := range typed {
			text, ok := item.(string)
			if !ok || text == "" || len(text) > 256 {
				return nil, false
			}
			values = append(values, text)
		}
		return values, true
	default:
		return nil, false
	}
}

func matchesOIDCValue(values, allowed []string) bool {
	for _, value := range values {
		for _, candidate := range allowed {
			if value == candidate {
				return true
			}
		}
	}
	return false
}

func randomURLToken() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func setOIDCTransactionCookie(writer http.ResponseWriter, request *http.Request, redirectURL, token string, maxAge int) {
	parsed, _ := url.Parse(redirectURL)
	http.SetCookie(writer, &http.Cookie{
		Name: oidcTransactionCookie, Value: token, Path: "/admin/api/session/oidc/callback", MaxAge: maxAge,
		HttpOnly: true, Secure: request.TLS != nil || parsed != nil && parsed.Scheme == "https", SameSite: http.SameSiteLaxMode,
	})
}
