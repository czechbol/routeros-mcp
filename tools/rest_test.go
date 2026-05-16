package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/czechbol/routeros-mcp/server"
)

func TestMergeProplist_NoFields(t *testing.T) {
	q := map[string]string{"?disabled": "false"}
	got := mergeProplist(q, nil)
	if !reflect.DeepEqual(got, q) {
		t.Fatalf("expected query untouched, got %v", got)
	}
}

func TestMergeProplist_AppendsFields(t *testing.T) {
	got := mergeProplist(nil, []string{"name", "running", "disabled"})
	if got[".proplist"] != "name,running,disabled" {
		t.Fatalf(".proplist not set correctly: %v", got)
	}
}

func TestMergeProplist_RespectsExplicitProplist(t *testing.T) {
	q := map[string]string{".proplist": "address"}
	got := mergeProplist(q, []string{"name"})
	if got[".proplist"] != "address" {
		t.Fatalf("explicit .proplist overwritten: %v", got)
	}
}

func TestPaginate(t *testing.T) {
	items := []any{1, 2, 3, 4, 5}
	out := paginate(items, 2, 2, 200)
	if out.Total != 5 || len(out.Items) != 2 || !out.HasMore || out.NextOffset != 4 {
		t.Fatalf("bad paginate result: %#v", out)
	}
	out2 := paginate(items, 4, 10, 200)
	if out2.HasMore || len(out2.Items) != 1 {
		t.Fatalf("bad tail page: %#v", out2)
	}
	out3 := paginate(items, 99, 10, 200)
	if len(out3.Items) != 0 || out3.HasMore {
		t.Fatalf("out-of-range offset should yield empty page: %#v", out3)
	}
	outNeg := paginate(items, -1, 2, 200)
	if len(outNeg.Items) != 2 {
		t.Fatalf("negative offset should clamp to 0: %#v", outNeg)
	}
}

func TestExecHandlerCallsUpstream(t *testing.T) {
	var hits int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if !strings.HasSuffix(r.URL.Path, "/rest/system/reboot") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer ts.Close()
	c := server.NewClient(server.Config{BaseURL: ts.URL, Username: "u", Password: "p"})
	fn := execHandler(c)
	_, _, err := fn(context.Background(), nil, ExecIn{Path: "system/reboot"})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if hits != 1 {
		t.Fatalf("expected 1 upstream call, got %d", hits)
	}
}
