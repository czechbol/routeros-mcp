package tools

import (
	"context"
	"strings"
	"testing"
)

func TestTopLevelMenusDedupes(t *testing.T) {
	menus := topLevelMenus()
	if len(menus) == 0 {
		t.Fatal("expected top-level menus from embedded paths.txt")
	}
	seen := map[string]struct{}{}
	for _, m := range menus {
		if !strings.HasPrefix(m, "/") {
			t.Errorf("menu missing leading slash: %q", m)
		}
		if strings.Count(m, "/") != 1 {
			t.Errorf("menu has nested separator: %q", m)
		}
		if _, dup := seen[m]; dup {
			t.Errorf("duplicate menu: %q", m)
		}
		seen[m] = struct{}{}
	}
}

func TestSelectPathsEmptyReturnsTopLevel(t *testing.T) {
	got := selectPaths("")
	want := topLevelMenus()
	if len(got) != len(want) {
		t.Fatalf("len: got %d, want %d", len(got), len(want))
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("at %d: got %q, want %q", i, got[i], want[i])
		}
	}
}

func TestSelectPathsCaseInsensitiveSubstring(t *testing.T) {
	lower := selectPaths("address")
	upper := selectPaths("ADDRESS")
	mixed := selectPaths("Address")
	if len(lower) == 0 {
		t.Fatal("expected matches for 'address'")
	}
	if len(lower) != len(upper) || len(lower) != len(mixed) {
		t.Fatalf("case-insensitive match diverged: %d / %d / %d",
			len(lower), len(upper), len(mixed))
	}
}

func TestListPathsPaginationOutOfRange(t *testing.T) {
	_, out, err := listPaths(context.Background(), nil, ListPathsIn{
		Match:  "address",
		Offset: 100000,
	})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(out.Paths) != 0 {
		t.Fatalf("expected empty page, got %d", len(out.Paths))
	}
	if out.HasMore {
		t.Fatalf("HasMore should be false when offset past end")
	}
}

func TestListPathsEmptyMatchHasNote(t *testing.T) {
	_, out, err := listPaths(context.Background(), nil, ListPathsIn{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if out.Note == "" {
		t.Fatal("expected note for empty match")
	}
}
