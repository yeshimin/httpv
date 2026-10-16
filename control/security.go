package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	parsed, err := url.Parse(origin)
	return err == nil && strings.EqualFold(parsed.Host, r.Host)
}

const sessionCookieName = "httpv_session"

type Role int

const (
	RoleNone Role = iota
	RoleViewer
	RoleOperator
	RoleAdmin
)

func (role Role) String() string {
	switch role {
	case RoleAdmin:
		return "admin"
	case RoleOperator:
		return "operator"
	case RoleViewer:
		return "viewer"
	default:
		return "none"
	}
}

type authSession struct {
	Role      Role
	CSRFToken string
	ExpiresAt time.Time
}

type authContextKey struct{}

type AuthManager struct {
	mu       sync.Mutex
	tokens   map[Role]string
	sessions map[string]authSession
	ttl      time.Duration
}

func newAuthManagerFromEnv() *AuthManager {
	return &AuthManager{
		tokens: map[Role]string{
			RoleViewer:   strings.TrimSpace(os.Getenv("HTTPV_VIEWER_TOKEN")),
			RoleOperator: strings.TrimSpace(os.Getenv("HTTPV_OPERATOR_TOKEN")),
			RoleAdmin:    strings.TrimSpace(os.Getenv("HTTPV_ADMIN_TOKEN")),
		},
		sessions: map[string]authSession{},
		ttl:      8 * time.Hour,
	}
}

func (a *AuthManager) enabled() bool {
	if a == nil {
		return false
	}
	for _, token := range a.tokens {
		if token != "" {
			return true
		}
	}
	return false
}

func secureEqual(left, right string) bool {
	if len(left) == 0 || len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func randomToken(bytes int) (string, error) {
	buffer := make([]byte, bytes)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func (a *AuthManager) login(token string) (string, authSession, bool) {
	if !a.enabled() {
		return "", authSession{Role: RoleAdmin, ExpiresAt: time.Now().Add(a.ttl)}, true
	}
	role := RoleNone
	for _, candidate := range []Role{RoleAdmin, RoleOperator, RoleViewer} {
		if secureEqual(token, a.tokens[candidate]) {
			role = candidate
			break
		}
	}
	if role == RoleNone {
		return "", authSession{}, false
	}
	sessionID, err := randomToken(32)
	if err != nil {
		return "", authSession{}, false
	}
	csrfToken, err := randomToken(24)
	if err != nil {
		return "", authSession{}, false
	}
	session := authSession{Role: role, CSRFToken: csrfToken, ExpiresAt: time.Now().Add(a.ttl)}
	a.mu.Lock()
	a.sessions[sessionID] = session
	for id, candidate := range a.sessions {
		if time.Now().After(candidate.ExpiresAt) {
			delete(a.sessions, id)
		}
	}
	a.mu.Unlock()
	return sessionID, session, true
}

func (a *AuthManager) session(r *http.Request) (string, authSession, bool) {
	if !a.enabled() {
		return "", authSession{Role: RoleAdmin, ExpiresAt: time.Now().Add(a.ttl)}, true
	}
	cookie, err := r.Cookie(sessionCookieName)
	if err != nil || cookie.Value == "" {
		return "", authSession{}, false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	session, ok := a.sessions[cookie.Value]
	if !ok || time.Now().After(session.ExpiresAt) {
		delete(a.sessions, cookie.Value)
		return "", authSession{}, false
	}
	return cookie.Value, session, true
}

func (a *AuthManager) logout(sessionID string) {
	if sessionID == "" {
		return
	}
	a.mu.Lock()
	delete(a.sessions, sessionID)
	a.mu.Unlock()
}

func writeSessionCookie(w http.ResponseWriter, r *http.Request, sessionID string, ttl time.Duration) {
	secure := r.Header.Get("X-Forwarded-Proto") == "https" || r.TLS != nil
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: sessionID, Path: "/", MaxAge: int(ttl.Seconds()),
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode,
	})
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	writeSessionCookie(w, r, "", -time.Hour)
}

func requiredRole(r *http.Request) Role {
	if r.URL.Path == "/api/auth/logout" {
		return RoleViewer
	}
	if strings.HasPrefix(r.URL.Path, "/api/requests/") {
		return RoleAdmin
	}
	if r.Method == http.MethodGet {
		if strings.HasPrefix(r.URL.Path, "/api/audit") {
			return RoleAdmin
		}
		return RoleViewer
	}
	path := r.URL.Path
	if strings.HasPrefix(path, "/api/pending/") || strings.HasPrefix(path, "/api/rules") || path == "/api/demo/burst" || path == "/api/request-rankings" {
		return RoleOperator
	}
	return RoleAdmin
}

func isPublicPath(path string) bool {
	return path == "/" || path == "/healthz" || path == "/api/auth/login" || path == "/api/auth/status"
}

func isMutation(method string) bool {
	return method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch || method == http.MethodDelete
}

func roleFromRequest(r *http.Request) Role {
	role, _ := r.Context().Value(authContextKey{}).(Role)
	return role
}

func (a *AuthManager) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions || isPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		sessionID, session, ok := a.session(r)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication required"})
			return
		}
		if session.Role < requiredRole(r) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "insufficient role"})
			return
		}
		if a.enabled() && isMutation(r.Method) && r.URL.Path != "/api/auth/login" {
			if !secureEqual(r.Header.Get("X-HTTPV-CSRF"), session.CSRFToken) {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "invalid CSRF token"})
				return
			}
		}
		ctx := context.WithValue(r.Context(), authContextKey{}, session.Role)
		ctx = context.WithValue(ctx, sessionCookieName, sessionID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type AuditEntry struct {
	Timestamp int64  `json:"timestamp"`
	Role      string `json:"role"`
	Action    string `json:"action"`
	Target    string `json:"target,omitempty"`
	Outcome   string `json:"outcome"`
}

type AuditLogger struct {
	mu      sync.Mutex
	path    string
	entries []AuditEntry
}

func newAuditLogger(path string) *AuditLogger {
	logger := &AuditLogger{path: path, entries: []AuditEntry{}}
	if path == "" {
		return logger
	}
	file, err := os.Open(path)
	if err != nil {
		return logger
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		var entry AuditEntry
		if json.Unmarshal(scanner.Bytes(), &entry) == nil {
			logger.entries = append(logger.entries, entry)
			if len(logger.entries) > 500 {
				logger.entries = logger.entries[len(logger.entries)-500:]
			}
		}
	}
	return logger
}

func (a *AuditLogger) record(r *http.Request, action, target, outcome string) {
	if a == nil {
		return
	}
	entry := AuditEntry{Timestamp: time.Now().UnixMilli(), Role: roleFromRequest(r).String(), Action: action, Target: target, Outcome: outcome}
	buffer, _ := json.Marshal(entry)
	a.mu.Lock()
	a.entries = append(a.entries, entry)
	if len(a.entries) > 500 {
		a.entries = append([]AuditEntry{}, a.entries[len(a.entries)-500:]...)
	}
	if a.path != "" {
		_ = os.MkdirAll(filepath.Dir(a.path), 0o750)
		if file, err := os.OpenFile(a.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640); err == nil {
			_, _ = file.Write(append(buffer, '\n'))
			_ = file.Close()
		}
	}
	a.mu.Unlock()
}

func (a *AuditLogger) recent(limit int) []AuditEntry {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if limit > len(a.entries) {
		limit = len(a.entries)
	}
	result := make([]AuditEntry, limit)
	for index := 0; index < limit; index++ {
		result[index] = a.entries[len(a.entries)-1-index]
	}
	return result
}

func auditLimit(r *http.Request) int {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	return limit
}

func requireConfirmation(w http.ResponseWriter, r *http.Request, expected string) bool {
	if r.Header.Get("X-HTTPV-Confirm") == expected {
		return true
	}
	writeJSON(w, http.StatusPreconditionRequired, map[string]string{"error": "explicit confirmation is required"})
	return false
}
