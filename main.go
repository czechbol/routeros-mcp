// Package main is the routeros-mcp HTTP server entrypoint.
package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/czechbol/routeros-mcp/server"
	"github.com/czechbol/routeros-mcp/tools"
)

const (
	defaultBaseURL           = "https://127.0.0.1"
	defaultListenAddr        = "0.0.0.0:8080"
	defaultRouterTimeout     = 30 * time.Second
	defaultReadHeaderTimeout = 10 * time.Second
	defaultReadTimeout       = 60 * time.Second
	defaultWriteTimeout      = 5 * time.Minute
	defaultIdleTimeout       = 2 * time.Minute
	liveSpecLoadTimeout      = 90 * time.Second
	minTokenLength           = 32
	maxMCPRequestBytes       = 1 << 20 // 1 MiB cap on /mcp request bodies
	maxHealthRequestBytes    = 8 << 10 // 8 KiB cap on /healthz request bodies
)

func main() {
	log.SetOutput(os.Stderr)

	cfg := server.Config{
		BaseURL:  getenv("ROS_URL", defaultBaseURL),
		Username: os.Getenv("ROS_USER"),
		Password: os.Getenv("ROS_PASS"),
		Insecure: os.Getenv("ROS_INSECURE") == "1",
		Timeout:  defaultRouterTimeout,
	}
	if cfg.Username == "" || cfg.Password == "" {
		log.Fatal("ROS_USER and ROS_PASS are required")
	}

	mcpToken := os.Getenv("MCP_TOKEN")
	allowAnon := os.Getenv("MCP_ALLOW_ANON") == "1"
	switch {
	case mcpToken == "" && !allowAnon:
		log.Fatal("MCP_TOKEN is required; set MCP_ALLOW_ANON=1 to expose the server unauthenticated")
	case mcpToken != "" && len(mcpToken) < minTokenLength:
		log.Fatalf(
			"MCP_TOKEN must be at least %d characters; generate one with `openssl rand -hex 32`",
			minTokenLength,
		)
	case allowAnon:
		log.Print("WARNING: MCP_ALLOW_ANON=1 — /mcp is exposed without authentication")
	}

	client := server.NewClient(cfg)
	srv := server.NewMCP()
	tools.RegisterRESTTools(srv, client)
	tools.RegisterDiscoveryTools(srv)
	tools.RegisterDescribeTool(srv)

	go loadLiveSpec(client)

	addr := getenv("LISTEN_ADDR", defaultListenAddr)
	allowedOrigins := splitCSV(os.Getenv("ALLOWED_ORIGINS"))

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return srv
	}, nil)

	mux := http.NewServeMux()
	mcpHandler := authGuard(originGuard(handler, allowedOrigins), mcpToken, allowAnon)
	mux.Handle("/mcp", maxBytes(mcpHandler, maxMCPRequestBytes))
	healthHandler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	mux.Handle("/healthz", maxBytes(healthHandler, maxHealthRequestBytes))

	log.Printf("routeros-mcp listening on %s, target %s", addr, cfg.BaseURL)
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: defaultReadHeaderTimeout,
		ReadTimeout:       defaultReadTimeout,
		WriteTimeout:      defaultWriteTimeout,
		IdleTimeout:       defaultIdleTimeout,
	}
	if err := httpSrv.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// loadLiveSpec attempts to fetch the live RouterOS OpenAPI spec at startup
// and hand it to the tools package. Failures fall back to embedded shards.
func loadLiveSpec(c *server.Client) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("loadLiveSpec panic: %v", r)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), liveSpecLoadTimeout)
	defer cancel()
	spec, err := server.ResolveLiveSpec(ctx, c)
	switch {
	case err == nil:
		tools.SetLiveSpec(spec)
		log.Printf(
			"openapi source: %s (RouterOS %s, %d paths)", spec.Source, spec.SpecVersion, len(spec.PathKeys),
		)
	case errors.Is(err, server.ErrSpecNotPublished):
		log.Printf("openapi source: embedded 7.22.3 (no upstream spec published for this RouterOS version)")
	case errors.Is(err, server.ErrOpenAPIUnavailable):
		log.Printf("openapi source: embedded 7.22.3 (live spec unavailable: %v)", err)
	default:
		log.Printf("openapi source: embedded 7.22.3 (unexpected error: %v)", err)
	}
}

// maxBytes wraps r.Body in http.MaxBytesReader so an oversized request
// body trips the SDK decoder with a recoverable error instead of letting
// the handler allocate unbounded memory.
func maxBytes(next http.Handler, n int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, n)
		}
		next.ServeHTTP(w, r)
	})
}

// authGuard wraps next with a constant-time bearer-token check. An empty
// token is accepted only when allowAnon is true (MCP_ALLOW_ANON=1); otherwise
// every request is rejected, even though main also fatals on this combination.
func authGuard(next http.Handler, token string, allowAnon bool) http.Handler {
	if token == "" {
		if allowAnon {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="routeros-mcp"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		})
	}
	expected := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")
		if subtle.ConstantTimeCompare([]byte(got), expected) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="routeros-mcp"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// originGuard, when configured with a non-empty allowlist, requires every
// request to carry an Origin header that matches an entry. An empty or
// missing Origin is rejected with 403 — RouterOS MCP is not designed for
// CSRF-style ambient credentials, but accepting unsourced requests would
// let any process on a reachable network reach /mcp once it knows a token.
func originGuard(next http.Handler, allowed []string) http.Handler {
	if len(allowed) == 0 {
		return next
	}
	set := make(map[string]struct{}, len(allowed))
	for _, o := range allowed {
		set[o] = struct{}{}
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if _, ok := set[origin]; !ok {
			http.Error(w, "forbidden origin", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
