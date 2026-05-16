package server

import (
	"reflect"
	"testing"
)

func TestRedact_TopLevel(t *testing.T) {
	t.Setenv("REDACT", "")
	resetRedactor()

	in := map[string]any{
		"name":     "wg-client",
		"password": "hunter2",
	}
	got := Redact(in)
	want := map[string]any{
		"name":     "wg-client",
		"password": redactionMask,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestRedact_NestedAndArrays(t *testing.T) {
	t.Setenv("REDACT", "")
	resetRedactor()

	in := []any{
		map[string]any{
			"name":        "peer-1",
			"private-key": "abc",
			"endpoint":    "1.2.3.4",
		},
		map[string]any{
			"community": "public",
			"nested": map[string]any{
				"psk": "secretvalue",
			},
		},
	}
	got := Redact(in).([]any)
	first := got[0].(map[string]any)
	if first["private-key"] != redactionMask || first["endpoint"] != "1.2.3.4" {
		t.Fatalf("first item: %#v", first)
	}
	second := got[1].(map[string]any)
	if second["community"] != redactionMask {
		t.Fatalf("community not redacted: %#v", second)
	}
	nested := second["nested"].(map[string]any)
	if nested["psk"] != redactionMask {
		t.Fatalf("nested psk not redacted: %#v", nested)
	}
}

func TestRedact_Disabled(t *testing.T) {
	t.Setenv("REDACT", "0")
	resetRedactor()

	in := map[string]any{"password": "hunter2"}
	got := Redact(in).(map[string]any)
	if got["password"] != "hunter2" {
		t.Fatalf("expected REDACT=0 to leave value untouched, got %v", got)
	}
}

func TestRedact_Extra(t *testing.T) {
	t.Setenv("REDACT", "")
	t.Setenv("REDACT_EXTRA", "comment, custom-field ")
	resetRedactor()

	in := map[string]any{
		"comment":      "should-redact",
		"custom-field": "x",
		"name":         "keep",
	}
	got := Redact(in).(map[string]any)
	if got["comment"] != redactionMask || got["custom-field"] != redactionMask {
		t.Fatalf("extras not redacted: %#v", got)
	}
	if got["name"] != "keep" {
		t.Fatalf("non-sensitive field changed: %#v", got)
	}
}
