package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestClient(t *testing.T, h http.Handler) *Client {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return NewClient(Config{BaseURL: ts.URL, Username: "u", Password: "p"})
}

func TestDoRedactsErrorBody(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"password":"secret","detail":"bad"}`))
	}))
	raw, status, err := c.Do(context.Background(), "GET", "ip/address", nil, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("status: got %d, want 401", status)
	}
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("error: got %v, want ErrUpstream", err)
	}
	m, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("raw: not a map, got %T", raw)
	}
	if got := m["password"]; got != redactionMask {
		t.Fatalf("password not redacted: %v", got)
	}
	if got := m["detail"]; got != "bad" {
		t.Fatalf("detail wrongly mutated: %v", got)
	}
}

func TestDoScrubsPlaintextErrorBody(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("login failed for password=hunter2 user=foo"))
	}))
	_, _, err := c.Do(context.Background(), "GET", "ip/address", nil, nil)
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("password leaked in plaintext error: %v", err)
	}
	if !strings.Contains(err.Error(), redactionMask) {
		t.Fatalf("expected [REDACTED] marker in error: %v", err)
	}
}

func TestDoSurfacesParseErrorOnMalformedSuccess(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"password":"hunter2"`)) // unterminated
	}))
	raw, status, err := c.Do(context.Background(), "GET", "ip/address", nil, nil)
	if err != nil {
		t.Fatalf("expected no Go error on 2xx: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("status: got %d", status)
	}
	m, ok := raw.(map[string]any)
	if !ok {
		t.Fatalf("raw: expected synthetic map, got %T", raw)
	}
	if _, ok := m["_parse_error"]; !ok {
		t.Fatalf("expected _parse_error key: %#v", m)
	}
	rawField, _ := m["_raw"].(string)
	if strings.Contains(rawField, "hunter2") {
		t.Fatalf("_raw leaked password: %v", rawField)
	}
}

func TestDoBodyTruncation(t *testing.T) {
	body := strings.Repeat("x", 1024)
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(body))
	}))
	_, _, err := c.Do(context.Background(), "GET", "x", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "…") {
		t.Fatalf("expected truncation marker in error: %v", err)
	}
}

func TestDoEmptyBody(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	raw, status, err := c.Do(context.Background(), "GET", "x", nil, nil)
	if err != nil || status != http.StatusOK || raw != nil {
		t.Fatalf("empty body: raw=%v status=%d err=%v", raw, status, err)
	}
}

func TestDoBasicAuthAndContentType(t *testing.T) {
	var gotAuth, gotCT, gotAccept string
	var gotBody []byte
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotCT = r.Header.Get("Content-Type")
		gotAccept = r.Header.Get("Accept")
		gotBody, _ = io.ReadAll(r.Body)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	_, _, err := c.Do(context.Background(), "PUT", "ip/address", nil, map[string]string{"a": "b"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.HasPrefix(gotAuth, "Basic ") {
		t.Fatalf("Authorization: got %q, want Basic prefix", gotAuth)
	}
	if gotCT != "application/json" {
		t.Fatalf("Content-Type: got %q", gotCT)
	}
	if gotAccept != "application/json" {
		t.Fatalf("Accept: got %q", gotAccept)
	}
	if string(gotBody) != `{"a":"b"}` {
		t.Fatalf("body: got %q", string(gotBody))
	}
}

func TestDoRejectsInjectedQuery(t *testing.T) {
	c := newTestClient(t, http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		t.Fatal("upstream must not be hit on injected path")
	}))
	cases := []string{
		"ip/address?.proplist=password",
		"ip/address#frag",
		`ip\address`,
		"ip/../etc",
		"ip/address with space",
		"",
		"/",
	}
	for _, p := range cases {
		_, _, err := c.Do(context.Background(), "GET", p, nil, nil)
		if !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("path %q: got err=%v, want ErrInvalidPath", p, err)
		}
	}
}

func TestDoAcceptsRouterOSIDSegment(t *testing.T) {
	var gotURL string
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		_, _ = w.Write([]byte(`{}`))
	}))
	_, _, err := c.Do(context.Background(), "PATCH", "ip/address/*A", nil, map[string]string{"x": "y"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(gotURL, "/rest/ip/address/*A") {
		t.Fatalf("URL: got %q, want segment '/rest/ip/address/*A'", gotURL)
	}
}

func TestDoBuildURLAppendsQuery(t *testing.T) {
	var gotURL string
	c := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		_, _ = w.Write([]byte(`[]`))
	}))
	_, _, err := c.Do(context.Background(), "GET", "/interface", map[string]string{"name": "ether1"}, nil)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !strings.Contains(gotURL, "/rest/interface") || !strings.Contains(gotURL, "name=ether1") {
		t.Fatalf("URL: got %q", gotURL)
	}
}
