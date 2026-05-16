// Package server holds the RouterOS REST client and the cross-cutting helpers
// (errors, response rendering) used by the MCP tools.
package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	defaultClientTimeout = 30 * time.Second
	httpErrorBoundary    = 400
	errBodyTruncate      = 512
)

// ErrUpstream signals a non-2xx response from the RouterOS REST API. Callers
// can errors.Is against it; the wrapped text carries the upstream body.
var ErrUpstream = errors.New("upstream RouterOS REST error")

// Config holds the credentials and connection knobs for the RouterOS client.
type Config struct {
	BaseURL  string
	Username string
	Password string
	Insecure bool
	Timeout  time.Duration
}

// Client speaks RouterOS REST over HTTP(S) with basic auth.
type Client struct {
	http *http.Client
	cfg  Config
}

// NewClient returns a Client primed with cfg. A zero Timeout defaults to 30s.
func NewClient(cfg Config) *Client {
	if cfg.Timeout == 0 {
		cfg.Timeout = defaultClientTimeout
	}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: cfg.Insecure, //nolint:gosec // opt-in via ROS_INSECURE for self-signed RouterOS certs
			MinVersion:         tls.VersionTLS12,
		},
	}
	return &Client{
		http: &http.Client{Timeout: cfg.Timeout, Transport: tr},
		cfg:  cfg,
	}
}

// Do executes a REST call against /rest/<path>. method is GET/POST/PATCH/PUT/DELETE.
// body, if non-nil, is JSON-encoded. query map appended as ?k=v. Returns raw decoded JSON.
func (c *Client) Do(
	ctx context.Context, method, restPath string, query map[string]string, body any,
) (any, int, error) {
	u, err := c.buildURL(restPath, query)
	if err != nil {
		return nil, 0, err
	}

	var reqBody io.Reader
	if body != nil {
		buf, encErr := json.Marshal(body)
		if encErr != nil {
			return nil, 0, fmt.Errorf("encode body: %w", encErr)
		}
		reqBody = bytes.NewReader(buf)
	}

	req, err := http.NewRequestWithContext(ctx, method, u, reqBody)
	if err != nil {
		return nil, 0, fmt.Errorf("build request: %w", err)
	}
	req.SetBasicAuth(c.cfg.Username, c.cfg.Password)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("http: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read body: %w", err)
	}

	parsed, parseErr := rawToAny(raw)
	if parseErr != nil {
		log.Printf("upstream %s /rest/%s: JSON parse failed: %v", method, restPath, parseErr)
	}
	if resp.StatusCode >= httpErrorBoundary {
		return Redact(parsed), resp.StatusCode, fmt.Errorf(
			"%w: status %d: %s", ErrUpstream, resp.StatusCode, truncate(string(raw), errBodyTruncate),
		)
	}
	if len(raw) == 0 {
		return nil, resp.StatusCode, nil
	}
	return Redact(parsed), resp.StatusCode, nil
}

// buildURL combines BaseURL + /rest/ + path and applies the query map.
func (c *Client) buildURL(restPath string, query map[string]string) (string, error) {
	base := strings.TrimRight(c.cfg.BaseURL, "/")
	clean := strings.TrimLeft(restPath, "/")
	// Allow callers to pass either "ip/address" or "/ip/address".
	full := base + "/rest/" + clean
	u, err := url.Parse(full)
	if err != nil {
		return "", fmt.Errorf("bad path %q: %w", restPath, err)
	}
	if len(query) > 0 {
		q := u.Query()
		for k, v := range query {
			q.Set(k, v)
		}
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}

// rawToAny decodes JSON, falling back to the raw string on parse failure but
// returning the parse error so callers can surface it.
func rawToAny(b []byte) (any, error) {
	if len(b) == 0 {
		return nil, nil
	}
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		return string(b), err
	}
	return v, nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
