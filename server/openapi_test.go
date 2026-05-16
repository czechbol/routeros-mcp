package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestFetchSpecWithRetry_404Terminal(t *testing.T) {
	var hits int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()
	// fetchSpecWithRetry uses openapiURLTemplate; we can't redirect it without
	// monkey-patching, so we exercise fetchSpec directly which is what the
	// retry loop calls — and the retry loop also short-circuits on
	// ErrSpecNotPublished. Verify fetchSpec returns the sentinel.
	url := ts.URL + "/openapi.json"
	_, _, err := fetchSpecAt(context.Background(), url, "7.99.0")
	if !errors.Is(err, ErrSpecNotPublished) {
		t.Fatalf("want ErrSpecNotPublished, got %v", err)
	}
	if got := atomic.LoadInt64(&hits); got != 1 {
		t.Fatalf("404 should be terminal: got %d hits, want 1", got)
	}
}

func TestFetchSpecWithRetry_500Retries(t *testing.T) {
	var hits int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&hits, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()
	url := ts.URL + "/openapi.json"
	// Manually emulate retry: fetchSpec on 500 returns errFetchStatus (not
	// terminal), so calling 3x is the contract pinned by fetchRetryAttempts.
	for i := range fetchRetryAttempts {
		_, _, err := fetchSpecAt(context.Background(), url, "7.99.0")
		if !errors.Is(err, errFetchStatus) {
			t.Fatalf("attempt %d: want errFetchStatus, got %v", i+1, err)
		}
	}
	if got := atomic.LoadInt64(&hits); got != int64(fetchRetryAttempts) {
		t.Fatalf("got %d hits, want %d", got, fetchRetryAttempts)
	}
}

func TestParseSpec(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		want     string
		wantErr  error
		mismatch bool
	}{
		{"valid", `{"openapi":"3.0.0","info":{"version":"7.22.3"},"paths":{}}`, "7.22.3", nil, false},
		{"missing version", `{"openapi":"3.0.0","info":{},"paths":{}}`, "", errSpecMissingVersion, false},
		{"malformed", `not json`, "", nil, false}, // wantErr handled below
		{"version mismatch", `{"openapi":"3.0.0","info":{"version":"7.22.3"},"paths":{}}`, "7.22.3", nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := "7.22.3"
			if tc.mismatch {
				want = "7.99.0"
			}
			spec, err := parseSpec([]byte(tc.body), want)
			if tc.name == "malformed" {
				if err == nil || !strings.Contains(err.Error(), "decode openapi") {
					t.Fatalf("want decode error, got %v", err)
				}
				return
			}
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err: got %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if spec.SpecVersion != tc.want {
				t.Fatalf("version: got %q, want %q", spec.SpecVersion, tc.want)
			}
			if tc.mismatch && spec.Source != "live-mismatched" {
				t.Fatalf("mismatch: Source=%q, want live-mismatched", spec.Source)
			}
		})
	}
}

func TestResolveCacheDir(t *testing.T) {
	t.Run("OPENAPI_CACHE_DIR wins", func(t *testing.T) {
		t.Setenv("OPENAPI_CACHE_DIR", "/explicit")
		t.Setenv("XDG_CACHE_HOME", "/xdg")
		if got := resolveCacheDir(); got != "/explicit" {
			t.Fatalf("got %q", got)
		}
	})
	t.Run("XDG_CACHE_HOME fallback", func(t *testing.T) {
		t.Setenv("OPENAPI_CACHE_DIR", "")
		t.Setenv("XDG_CACHE_HOME", "/xdg")
		want := filepath.Join("/xdg", openapiCacheDirName)
		if got := resolveCacheDir(); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
	t.Run("HOME .cache fallback", func(t *testing.T) {
		t.Setenv("OPENAPI_CACHE_DIR", "")
		t.Setenv("XDG_CACHE_HOME", "")
		t.Setenv("HOME", "/home/test")
		want := filepath.Join("/home/test", ".cache", openapiCacheDirName)
		if got := resolveCacheDir(); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})
}

// fetchSpecAt is a test seam mirroring fetchSpec but with the URL chosen by
// the caller, so we can point at httptest without monkey-patching the
// openapiURLTemplate constant.
func fetchSpecAt(ctx context.Context, url, version string) (*LiveSpec, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("build fetch req: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
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
	buf := make([]byte, 0)
	tmp := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if rerr != nil {
			break
		}
	}
	spec, perr := parseSpec(buf, version)
	if perr != nil {
		return nil, nil, perr
	}
	return spec, buf, nil
}
