package tools

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/czechbol/routeros-mcp/server"
)

func writeSpecFile(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "openapi.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write spec: %v", err)
	}
	return p
}

func TestLookupOperations_LiveHit(t *testing.T) {
	body := `{
		"openapi":"3.0.0",
		"info":{"version":"7.99.0"},
		"paths":{
			"/system/resource":{"get":{"summary":"sys","operationId":"sysGet"}},
			"/ip/address":{"get":{"summary":"ip","operationId":"ipGet"}}
		}
	}`
	cachePath := writeSpecFile(t, body)
	SetLiveSpec(&server.LiveSpec{
		SpecVersion: "7.99.0",
		Source:      "cache",
		CachePath:   cachePath,
		PathKeys: map[string]struct{}{
			"/system/resource": {},
			"/ip/address":      {},
		},
	})
	t.Cleanup(func() { SetLiveSpec(nil) })

	ops, version, source, err := lookupOperations("/ip/address")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if version != "7.99.0" {
		t.Fatalf("version: got %q", version)
	}
	if source != "cache" {
		t.Fatalf("source: got %q, want %q", source, "cache")
	}
	op, ok := ops["get"]
	if !ok {
		t.Fatalf("missing get op: %#v", ops)
	}
	if op.OperationID != "ipGet" || op.Summary != "ip" {
		t.Fatalf("decoded op wrong: %#v", op)
	}
}

func TestLookupOperations_LiveMissFallsThroughToEmbedded(t *testing.T) {
	body := `{"openapi":"3.0.0","info":{"version":"7.99.0"},"paths":{}}`
	cachePath := writeSpecFile(t, body)
	SetLiveSpec(&server.LiveSpec{
		SpecVersion: "7.99.0",
		Source:      "cache",
		CachePath:   cachePath,
		PathKeys:    map[string]struct{}{},
	})
	t.Cleanup(func() { SetLiveSpec(nil) })

	// /system/resource is in the embedded shards. Live spec lacks it, so the
	// lookup must fall through and still succeed against the embedded data.
	ops, version, source, err := lookupOperations("/system/resource")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if version == "" {
		t.Fatalf("expected embedded version, got empty")
	}
	if source != sourceEmbedded {
		t.Fatalf("source: got %q, want %q", source, sourceEmbedded)
	}
	if len(ops) == 0 {
		t.Fatalf("expected embedded ops, got empty")
	}
}

func TestLookupOperations_NoLiveSpec(t *testing.T) {
	SetLiveSpec(nil)
	ops, _, source, err := lookupOperations("/system/resource")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if source != sourceEmbedded {
		t.Fatalf("source: got %q, want %q", source, sourceEmbedded)
	}
	if len(ops) == 0 {
		t.Fatalf("expected embedded ops")
	}
}

func TestLookupOperations_LiveLookupErrorPropagates(t *testing.T) {
	// PathKeys claims the path exists but the cache file is missing on disk —
	// LookupPath must error and the caller must NOT silently fall through.
	SetLiveSpec(&server.LiveSpec{
		SpecVersion: "7.99.0",
		Source:      "cache",
		CachePath:   "/nonexistent/openapi.json",
		PathKeys:    map[string]struct{}{"/ip/address": {}},
	})
	t.Cleanup(func() { SetLiveSpec(nil) })

	_, _, _, err := lookupOperations("/ip/address")
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
}

func TestDecodeLiveOps(t *testing.T) {
	raw := json.RawMessage(`{"get":{"summary":"s","operationId":"id"},"put":{"operationId":"p"}}`)
	ops, err := decodeLiveOps(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if ops["get"].Summary != "s" || ops["get"].OperationID != "id" {
		t.Fatalf("get wrong: %#v", ops["get"])
	}
	if ops["put"].OperationID != "p" {
		t.Fatalf("put wrong: %#v", ops["put"])
	}
}

func TestDecodeLiveOps_Malformed(t *testing.T) {
	_, err := decodeLiveOps(json.RawMessage(`not json`))
	if err == nil {
		t.Fatalf("expected decode error")
	}
}

func TestLiveSpecLookupPath_Sentinel(t *testing.T) {
	body := `{"openapi":"3.0.0","info":{"version":"7.99.0"},"paths":{"/a":{"get":{}}}}`
	cachePath := writeSpecFile(t, body)
	spec := &server.LiveSpec{CachePath: cachePath}
	if _, err := spec.LookupPath("/missing"); !errors.Is(err, server.ErrPathNotInCatalogue) {
		t.Fatalf("want ErrPathNotInCatalogue, got %v", err)
	}
}
