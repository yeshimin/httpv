package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
)

func authenticatedRequest(method, path, sessionID, csrf string) *http.Request {
	request := httptest.NewRequest(method, path, nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: sessionID})
	if csrf != "" {
		request.Header.Set("X-HTTPV-CSRF", csrf)
	}
	return request
}

func TestAuthMiddlewareEnforcesRoleAndCSRF(t *testing.T) {
	t.Setenv("HTTPV_ADMIN_TOKEN", "admin-test-token")
	t.Setenv("HTTPV_OPERATOR_TOKEN", "operator-test-token")
	auth := newAuthManagerFromEnv()
	adminID, admin, ok := auth.login("admin-test-token")
	if !ok || admin.Role != RoleAdmin {
		t.Fatal("admin login failed")
	}
	operatorID, operator, ok := auth.login("operator-test-token")
	if !ok || operator.Role != RoleOperator {
		t.Fatal("operator login failed")
	}
	handler := auth.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, authenticatedRequest(http.MethodPut, "/api/config", operatorID, operator.CSRFToken))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("operator config update = %d, want 403", recorder.Code)
	}

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, authenticatedRequest(http.MethodPost, "/api/pending/block-all", operatorID, ""))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF = %d, want 403", recorder.Code)
	}

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, authenticatedRequest(http.MethodPost, "/api/pending/block-all", operatorID, operator.CSRFToken))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("operator pending action = %d, want 204", recorder.Code)
	}

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, authenticatedRequest(http.MethodPut, "/api/config", adminID, admin.CSRFToken))
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("admin config update = %d, want 204", recorder.Code)
	}
}

func TestAuditLogPersistsWithoutSecrets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	logger := newAuditLogger(path)
	request := httptest.NewRequest(http.MethodPost, "/api/config", nil)
	logger.record(request, "config.apply", "2", "success")
	reloaded := newAuditLogger(path)
	entries := reloaded.recent(10)
	if len(entries) != 1 || entries[0].Action != "config.apply" || entries[0].Target != "2" {
		t.Fatalf("unexpected audit entries: %#v", entries)
	}
}

func TestDangerousActionRequiresExactConfirmation(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/pending/block-all", nil)
	recorder := httptest.NewRecorder()
	if requireConfirmation(recorder, request, "block-all-pending") || recorder.Code != http.StatusPreconditionRequired {
		t.Fatalf("missing confirmation result = %v, %d", false, recorder.Code)
	}
	request.Header.Set("X-HTTPV-Confirm", "block-all-pending")
	if !requireConfirmation(httptest.NewRecorder(), request, "block-all-pending") {
		t.Fatal("exact confirmation was rejected")
	}
}

func TestWebSocketOriginMustMatchRequestHost(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "http://console.local/api/ws", nil)
	request.Header.Set("Origin", "http://console.local")
	if !sameOrigin(request) {
		t.Fatal("matching origin was rejected")
	}
	request.Header.Set("Origin", "https://attacker.example")
	if sameOrigin(request) {
		t.Fatal("cross-origin websocket was accepted")
	}
}
