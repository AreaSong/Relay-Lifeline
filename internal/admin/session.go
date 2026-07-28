package admin

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/areasong/relay-lifeline/internal/config"
)

const managementSessionCookie = "relay_lifeline_session"

var errLoginRateLimited = errors.New("management login rate limited")

type managementSession struct {
	Role     Role
	CSRF     string
	LastSeen time.Time
}

type loginFailures struct {
	Attempts      []time.Time
	CooldownUntil time.Time
}

type sessionManager struct {
	mu       sync.Mutex
	store    *config.Store
	sessions map[string]managementSession
	failures map[string]loginFailures
	now      func() time.Time
}

func newSessionManager(store *config.Store) *sessionManager {
	return &sessionManager{store: store, sessions: make(map[string]managementSession), failures: make(map[string]loginFailures), now: time.Now}
}

func (m *sessionManager) login(request *http.Request, key string, authenticator authenticator) (string, managementSession, error) {
	client := clientAddress(request)
	now := m.now()
	m.mu.Lock()
	if failure := m.failures[client]; failure.CooldownUntil.After(now) {
		m.mu.Unlock()
		return "", managementSession{}, errLoginRateLimited
	}
	m.mu.Unlock()
	role, ok := authenticator.authenticateKey(key)
	if !ok {
		m.recordFailure(client, now)
		return "", managementSession{}, errors.New("invalid management key")
	}
	token, err := randomToken()
	if err != nil {
		return "", managementSession{}, err
	}
	csrf, err := randomToken()
	if err != nil {
		return "", managementSession{}, err
	}
	session := managementSession{Role: role, CSRF: csrf, LastSeen: now}
	m.mu.Lock()
	delete(m.failures, client)
	m.sessions[token] = session
	m.mu.Unlock()
	return token, session, nil
}

func (m *sessionManager) authenticate(request *http.Request) (managementSession, string, bool, string) {
	cookie, err := request.Cookie(managementSessionCookie)
	if err != nil || cookie.Value == "" {
		return managementSession{}, "", false, "missing"
	}
	now := m.now()
	timeout := m.store.Get().ManagementSecurity.SessionIdleTimeout.Duration
	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.sessions[cookie.Value]
	if !ok {
		delete(m.sessions, cookie.Value)
		return managementSession{}, "", false, "invalidated"
	}
	if now.Sub(session.LastSeen) > timeout {
		delete(m.sessions, cookie.Value)
		return managementSession{}, "", false, "idle_timeout"
	}
	session.LastSeen = now
	m.sessions[cookie.Value] = session
	return session, cookie.Value, true, "active"
}

func (m *sessionManager) revoke(token string) {
	m.mu.Lock()
	delete(m.sessions, token)
	m.mu.Unlock()
}

func (m *sessionManager) recordFailure(client string, now time.Time) {
	cfg := m.store.Get().ManagementSecurity
	cutoff := now.Add(-time.Minute)
	m.mu.Lock()
	defer m.mu.Unlock()
	failure := m.failures[client]
	kept := failure.Attempts[:0]
	for _, attempt := range failure.Attempts {
		if attempt.After(cutoff) {
			kept = append(kept, attempt)
		}
	}
	failure.Attempts = append(kept, now)
	if len(failure.Attempts) >= cfg.LoginFailuresPerMinute {
		failure.CooldownUntil = now.Add(cfg.LoginCooldown.Duration)
	}
	m.failures[client] = failure
}

func clientAddress(request *http.Request) string {
	host, _, err := net.SplitHostPort(request.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	return request.RemoteAddr
}

func randomToken() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func setSessionCookie(writer http.ResponseWriter, request *http.Request, token string, maxAge int) {
	http.SetCookie(writer, &http.Cookie{
		Name: managementSessionCookie, Value: token, Path: "/admin/api", MaxAge: maxAge,
		HttpOnly: true, Secure: request.TLS != nil, SameSite: http.SameSiteStrictMode,
	})
}
