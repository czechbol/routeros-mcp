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

func newTestClient(t *testing.T, h http.Handler) (*Client, *httptest.Server) {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	c := NewClient(Config{BaseURL: ts.URL, Username: "u", Password: "p"})
	return c, ts
}

func TestDoRedactsErrorBody(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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

func TestDoBodyTruncation(t *testing.T) {
	body := strings.Repeat("x", 1024)
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(body))
	}))
	_, _, err := c.Do(context.Background(), "GET", "x", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "…") {
		t.Fatalf("expected truncation marker in error: %v", err)
	}
}

func TestDoEmptyBody(t *testing.T) {
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

func TestDoBuildURLAppendsQuery(t *testing.T) {
	var gotURL string
	c, _ := newTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
