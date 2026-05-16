package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
}

func TestAuthGuard(t *testing.T) {
	cases := []struct {
		name       string
		token      string
		allowAnon  bool
		authHeader string
		wantStatus int
		wantWWW    bool
	}{
		{"empty token, no anon: reject", "", false, "", http.StatusUnauthorized, true},
		{"empty token, anon: allow", "", true, "", http.StatusOK, false},
		{"token, missing header: 401", "abc", false, "", http.StatusUnauthorized, true},
		{"token, wrong: 401", "abc", false, "Bearer wrong", http.StatusUnauthorized, true},
		{"token, no prefix: 401", "abc", false, "abc", http.StatusUnauthorized, true},
		{"token, correct: 200", "abc", false, "Bearer abc", http.StatusOK, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := authGuard(okHandler(), tc.token, tc.allowAnon)
			req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
			if tc.authHeader != "" {
				req.Header.Set("Authorization", tc.authHeader)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != tc.wantStatus {
				t.Fatalf("status: got %d, want %d", w.Code, tc.wantStatus)
			}
			gotWWW := w.Header().Get("WWW-Authenticate")
			if tc.wantWWW && gotWWW == "" {
				t.Fatalf("expected WWW-Authenticate header, got none")
			}
			if !tc.wantWWW && gotWWW != "" {
				t.Fatalf("unexpected WWW-Authenticate=%q", gotWWW)
			}
		})
	}
}

func TestOriginGuard(t *testing.T) {
	cases := []struct {
		name       string
		allowed    []string
		origin     string
		wantStatus int
	}{
		{"empty allowlist: passthrough", nil, "https://x", http.StatusOK},
		{"matched: 200", []string{"https://x"}, "https://x", http.StatusOK},
		{"mismatched: 403", []string{"https://x"}, "https://y", http.StatusForbidden},
		{"no Origin header: passthrough", []string{"https://x"}, "", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := originGuard(okHandler(), tc.allowed)
			req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != tc.wantStatus {
				t.Fatalf("status: got %d, want %d", w.Code, tc.wantStatus)
			}
		})
	}
}
