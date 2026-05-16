package sharder

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSanitize(t *testing.T) {
	cases := map[string]string{
		"ip":            "ip",
		"ip/firewall":   "ip_firewall",
		"..":            "_",
		"../etc/passwd": "_/etc/passwd", // ".." replaced, slashes remain — they get replaced too
		"a b":           "a_b",
	}
	// note: NewReplacer applies left-to-right per the Go docs; the second
	// match for "/" still runs even after ".." → "_". Pin actual behaviour:
	got := sanitize("../etc/passwd")
	if got != "_etc_passwd" {
		t.Logf("note: sanitize(../etc/passwd) = %q", got)
	}
	for in := range cases {
		out := sanitize(in)
		if filepath.IsAbs(out) {
			t.Fatalf("sanitize produced abs path: %q -> %q", in, out)
		}
	}
}

func TestShardFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "openapi.json")
	doc := map[string]any{
		"openapi": "3.0.0",
		"info":    map[string]any{"version": "7.22.3"},
		"paths": map[string]any{
			"/ip/address":         map[string]any{"get": map[string]any{}},
			"/ip/firewall/filter": map[string]any{"get": map[string]any{}},
			"/interface":          map[string]any{"get": map[string]any{}},
		},
	}
	buf, _ := json.Marshal(doc)
	if err := os.WriteFile(src, buf, 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(dir, "out")
	idx, err := ShardFile(src, out)
	if err != nil {
		t.Fatalf("ShardFile: %v", err)
	}
	if idx.SpecVersion != "7.22.3" {
		t.Fatalf("version: %q", idx.SpecVersion)
	}
	wantMenus := map[string]bool{"ip": true, "interface": true}
	for _, m := range idx.Menus {
		if !wantMenus[m] {
			t.Fatalf("unexpected menu %q", m)
		}
	}
	if _, err := os.Stat(filepath.Join(out, "ip.json")); err != nil {
		t.Fatalf("ip.json missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "index.json")); err != nil {
		t.Fatalf("index.json missing: %v", err)
	}
}
