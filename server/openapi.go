package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	defaultOpenAPITimeout = 10 * time.Second
	openapiCacheDirName   = "routeros-mcp"
	openapiURLTemplate    = "https://tikoci.github.io/restraml/%s/openapi.json"
	openapiCachePerm      = 0o600
	openapiCacheDirPerm   = 0o755

	versionRetryAttempts = 8
	versionRetryStart    = 1 * time.Second
	versionRetryMax      = 8 * time.Second

	fetchRetryAttempts = 3
)

// ErrOpenAPIUnavailable is returned by ResolveLiveSpec when the live spec
// cannot be obtained for any reason — the caller falls back to embedded data.
var ErrOpenAPIUnavailable = errors.New("live OpenAPI spec unavailable")

// ErrPathNotInCatalogue is exported for the tools package to wrap when a
// requested RouterOS path is missing from the active OpenAPI document.
var ErrPathNotInCatalogue = errors.New("path not in OpenAPI catalogue")

// ErrSpecNotPublished signals that tikoci.github.io/restraml has no OpenAPI
// document for the router's RouterOS version. Currently only 7.22.x is hosted.
var ErrSpecNotPublished = errors.New("openapi spec not published for this RouterOS version")

// Sentinel errors wrapped on internal failure paths.
var (
	errVersionShape       = errors.New("unexpected /system/resource shape")
	errVersionMissing     = errors.New("router did not report a version")
	errSpecMissingVersion = errors.New("openapi missing info.version")
	errFetchStatus        = errors.New("fetch non-200")
)

// LiveSpec describes a RouterOS OpenAPI document held on disk. Only the path
// key set is kept in memory; operation bodies are streamed from CachePath on
// demand via LookupPath. This keeps idle memory low for a spec that is ~13 MB
// raw JSON and would otherwise inflate to many tens of MiB once decoded into a
// generic any-tree.
type LiveSpec struct {
	SpecVersion string
	OpenAPI     string
	Source      string // "live", "cache", "live-mismatched"
	CachePath   string
	PathKeys    map[string]struct{}
}

// LookupPath streams the cached spec file and returns the raw JSON for the
// operations object at paths[normalised]. Returns ErrPathNotInCatalogue if
// the key is absent. Callers should check PathKeys for a cheap membership
// test before calling this.
func (s *LiveSpec) LookupPath(normalised string) (json.RawMessage, error) {
	if s == nil || s.CachePath == "" {
		return nil, fmt.Errorf("%w: no cached spec", ErrPathNotInCatalogue)
	}
	f, err := os.Open(s.CachePath) //nolint:gosec // cache path resolved from env / user home
	if err != nil {
		return nil, fmt.Errorf("open cached spec: %w", err)
	}
	defer func() { _ = f.Close() }()
	dec := json.NewDecoder(f)
	if _, err := dec.Token(); err != nil {
		return nil, fmt.Errorf("decode cached spec: %w", err)
	}
	for dec.More() {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("decode cached spec: %w", err)
		}
		key, _ := tok.(string)
		if key != "paths" {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return nil, fmt.Errorf("skip %q: %w", key, err)
			}
			continue
		}
		if _, err := dec.Token(); err != nil {
			return nil, fmt.Errorf("enter paths: %w", err)
		}
		for dec.More() {
			ktok, err := dec.Token()
			if err != nil {
				return nil, fmt.Errorf("read path key: %w", err)
			}
			pathKey, _ := ktok.(string)
			if pathKey == normalised {
				var raw json.RawMessage
				if err := dec.Decode(&raw); err != nil {
					return nil, fmt.Errorf("decode path %q: %w", pathKey, err)
				}
				return raw, nil
			}
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return nil, fmt.Errorf("skip path %q: %w", pathKey, err)
			}
		}
		return nil, fmt.Errorf("%w: %q", ErrPathNotInCatalogue, normalised)
	}
	return nil, fmt.Errorf("%w: %q (no paths object)", ErrPathNotInCatalogue, normalised)
}

// ResolveLiveSpec inspects RouterOS for its version, looks for a cached copy
// of the matching OpenAPI document, and otherwise fetches it from
// tikoci.github.io. Returns nil + ErrOpenAPIUnavailable on any failure so the
// caller can stay on the embedded shards.
//
// Setting DYNAMIC_OPENAPI=0 short-circuits the whole flow.
func ResolveLiveSpec(ctx context.Context, c *Client) (*LiveSpec, error) {
	if os.Getenv("DYNAMIC_OPENAPI") == "0" {
		return nil, fmt.Errorf("%w: DYNAMIC_OPENAPI=0", ErrOpenAPIUnavailable)
	}
	version, err := detectVersionWithRetry(ctx, c)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrOpenAPIUnavailable, err)
	}
	cacheDir := resolveCacheDir()
	cachePath := filepath.Join(cacheDir, fmt.Sprintf("openapi-%s.json", version))

	if spec, cacheErr := readCachedSpec(cachePath, version); cacheErr == nil {
		spec.Source = "cache"
		spec.CachePath = cachePath
		return spec, nil
	}

	spec, raw, err := fetchSpecWithRetry(ctx, version)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrOpenAPIUnavailable, err)
	}
	if spec.Source == "" {
		spec.Source = "live"
	}
	if err := writeCachedSpec(cacheDir, cachePath, raw); err != nil {
		log.Printf("openapi cache write failed (%s): %v", cachePath, err)
		return nil, fmt.Errorf("%w: cache write: %w", ErrOpenAPIUnavailable, err)
	}
	spec.CachePath = cachePath
	return spec, nil
}

// detectVersionWithRetry handles the post-boot race where the container's
// veth gateway isn't reachable yet. Retries with exponential backoff up to
// versionRetryAttempts*versionRetryMax (~30s worst case).
func detectVersionWithRetry(ctx context.Context, c *Client) (string, error) {
	backoff := versionRetryStart
	var lastErr error
	for attempt := 1; attempt <= versionRetryAttempts; attempt++ {
		v, err := detectVersion(ctx, c)
		if err == nil {
			if attempt > 1 {
				log.Printf("router /system/resource ok after %d attempts", attempt)
			}
			return v, nil
		}
		lastErr = err
		if attempt == versionRetryAttempts {
			break
		}
		log.Printf("router /system/resource not ready (attempt %d/%d): %v; retrying in %s",
			attempt, versionRetryAttempts, err, backoff)
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("context cancelled while waiting for router: %w", ctx.Err())
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > versionRetryMax {
			backoff = versionRetryMax
		}
	}
	return "", lastErr
}

// fetchSpecWithRetry tries fetchSpec up to fetchRetryAttempts times. A 404
// (spec not published for this RouterOS version) is terminal — no retry.
func fetchSpecWithRetry(ctx context.Context, version string) (*LiveSpec, []byte, error) {
	var lastErr error
	for attempt := 1; attempt <= fetchRetryAttempts; attempt++ {
		spec, raw, err := fetchSpec(ctx, version)
		if err == nil {
			return spec, raw, nil
		}
		lastErr = err
		if errors.Is(err, ErrSpecNotPublished) {
			return nil, nil, err
		}
		if attempt == fetchRetryAttempts {
			break
		}
		log.Printf("openapi fetch failed (attempt %d/%d): %v; retrying", attempt, fetchRetryAttempts, err)
		select {
		case <-ctx.Done():
			return nil, nil, fmt.Errorf("context cancelled while fetching openapi: %w", ctx.Err())
		case <-time.After(versionRetryStart):
		}
	}
	return nil, nil, lastErr
}

func detectVersion(ctx context.Context, c *Client) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, defaultOpenAPITimeout)
	defer cancel()
	raw, _, err := c.Do(ctx, "GET", "system/resource", nil, nil)
	if err != nil {
		return "", fmt.Errorf("router /system/resource: %w", err)
	}
	m, ok := raw.(map[string]any)
	if !ok {
		return "", errVersionShape
	}
	v, _ := m["version"].(string)
	if v == "" {
		return "", errVersionMissing
	}
	// Trim trailing " (stable)" / " (testing)" tags.
	if i := strings.Index(v, " "); i >= 0 {
		v = v[:i]
	}
	return v, nil
}

func resolveCacheDir() string {
	if v := os.Getenv("OPENAPI_CACHE_DIR"); v != "" {
		return v
	}
	if v := os.Getenv("XDG_CACHE_HOME"); v != "" {
		return filepath.Join(v, openapiCacheDirName)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".cache", openapiCacheDirName)
	}
	return filepath.Join(os.TempDir(), openapiCacheDirName)
}

type rawOpenAPI struct {
	OpenAPI string                     `json:"openapi"`
	Info    struct{ Version string }   `json:"info"`
	Paths   map[string]json.RawMessage `json:"paths"`
}

func readCachedSpec(path, wantVersion string) (*LiveSpec, error) {
	buf, err := os.ReadFile(path) //nolint:gosec,nolintlint // cache path resolved from env / user home
	if err != nil {
		return nil, fmt.Errorf("read cache: %w", err)
	}
	return parseSpec(buf, wantVersion)
}

func fetchSpec(ctx context.Context, version string) (*LiveSpec, []byte, error) {
	timeout := defaultOpenAPITimeout
	if v := os.Getenv("OPENAPI_FETCH_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			timeout = d
		} else {
			log.Printf("invalid OPENAPI_FETCH_TIMEOUT=%q, using default %s: %v", v, timeout, err)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	url := fmt.Sprintf(openapiURLTemplate, version)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("build fetch req: %w", err)
	}
	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil, fmt.Errorf("%w: %s", ErrSpecNotPublished, version)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("%w: %s status %d", errFetchStatus, url, resp.StatusCode)
	}
	buf, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, fmt.Errorf("read fetch body: %w", err)
	}
	spec, err := parseSpec(buf, version)
	if err != nil {
		return nil, nil, err
	}
	return spec, buf, nil
}

func parseSpec(buf []byte, wantVersion string) (*LiveSpec, error) {
	var raw rawOpenAPI
	if err := json.Unmarshal(buf, &raw); err != nil {
		return nil, fmt.Errorf("decode openapi: %w", err)
	}
	if raw.Info.Version == "" {
		return nil, errSpecMissingVersion
	}
	keys := make(map[string]struct{}, len(raw.Paths))
	for k := range raw.Paths {
		keys[k] = struct{}{}
	}
	out := &LiveSpec{
		SpecVersion: raw.Info.Version,
		OpenAPI:     raw.OpenAPI,
		PathKeys:    keys,
	}
	if wantVersion != "" && raw.Info.Version != wantVersion {
		log.Printf(
			"WARNING: openapi spec reports version %q but router reports %q; using anyway",
			raw.Info.Version, wantVersion,
		)
		out.Source = "live-mismatched"
	}
	return out, nil
}

func writeCachedSpec(dir, path string, raw []byte) error {
	if err := os.MkdirAll(dir, openapiCacheDirPerm); err != nil {
		return err
	}
	return os.WriteFile(path, raw, openapiCachePerm)
}
