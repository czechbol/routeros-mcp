package tools

import (
	"context"
	"io"
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

func TestAddHandlerHTTP(t *testing.T) {
	var method, path string
	var body []byte
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		path = r.URL.Path
		body = readAll(t, r)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ret":"*9"}`))
	}))
	defer ts.Close()
	c := server.NewClient(server.Config{BaseURL: ts.URL, Username: "u", Password: "p"})
	_, out, err := addHandler(c)(context.Background(), nil, AddIn{
		Path: "ip/address",
		Body: map[string]any{"address": "10.0.0.1/24"},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if method != "PUT" {
		t.Errorf("method: %q", method)
	}
	if path != "/rest/ip/address" {
		t.Errorf("path: %q", path)
	}
	if !strings.Contains(string(body), `"address":"10.0.0.1/24"`) {
		t.Errorf("body: %q", body)
	}
	if out.Status != http.StatusCreated {
		t.Errorf("status: %d", out.Status)
	}
}

func TestSetHandlerHTTPEscapesID(t *testing.T) {
	var path string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		_, _ = w.Write([]byte(`{}`))
	}))
	defer ts.Close()
	c := server.NewClient(server.Config{BaseURL: ts.URL, Username: "u", Password: "p"})
	_, _, err := setHandler(c)(context.Background(), nil, SetIn{
		Path: "ip/address",
		ID:   "*A",
		Body: map[string]any{"disabled": "no"},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if path != "/rest/ip/address/*A" {
		t.Errorf("path: %q", path)
	}
}

func TestSetHandlerRejectsBadID(t *testing.T) {
	c := server.NewClient(server.Config{BaseURL: "http://invalid", Username: "u", Password: "p"})
	res, _, err := setHandler(c)(context.Background(), nil, SetIn{
		Path: "ip/address",
		ID:   "*A?injected=1",
		Body: map[string]any{"x": "y"},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatal("expected ToolError for injected ID")
	}
}

func TestRemoveHandlerHTTP(t *testing.T) {
	var method, path string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		method = r.Method
		path = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()
	c := server.NewClient(server.Config{BaseURL: ts.URL, Username: "u", Password: "p"})
	_, out, err := removeHandler(c)(context.Background(), nil, RemoveIn{
		Path: "ip/address",
		ID:   "*A",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if method != "DELETE" || path != "/rest/ip/address/*A" {
		t.Errorf("method=%q path=%q", method, path)
	}
	if out.Status != http.StatusNoContent {
		t.Errorf("status: %d", out.Status)
	}
}

func TestPrintHandlerForwardsProplist(t *testing.T) {
	var rawQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawQuery = r.URL.RawQuery
		_, _ = w.Write([]byte(`[]`))
	}))
	defer ts.Close()
	c := server.NewClient(server.Config{BaseURL: ts.URL, Username: "u", Password: "p"})
	_, _, err := printHandler(c)(context.Background(), nil, PrintIn{
		Path:   "ip/address",
		Fields: []string{"address", "interface"},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !strings.Contains(rawQuery, ".proplist=address%2Cinterface") {
		t.Errorf("expected .proplist in query: %q", rawQuery)
	}
}

func readAll(t *testing.T, r *http.Request) []byte {
	t.Helper()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return b
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
