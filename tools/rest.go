// Package tools registers the generic RouterOS REST tools exposed by routeros-mcp.
package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/czechbol/routeros-mcp/server"
)

const (
	formatJSON     = "json"
	formatMarkdown = "markdown"

	defaultPageLimit = 50
	minPageLimit     = 1
	maxPageLimit     = 500
)

// PrintIn parameters for the ros_print tool (GET /rest/<path>).
type PrintIn struct {
	Path   string            `json:"path"            jsonschema:"RouterOS menu path, e.g. ip/address, interface, ip/firewall/filter. Leading slash optional."`
	Fields []string          `json:"fields,omitempty" jsonschema:"only return these property names (server-side .proplist). Greatly reduces context for big lists. e.g. [\"name\",\"running\",\"disabled\"]"`
	Query  map[string]string `json:"query,omitempty"  jsonschema:"extra query parameters per RouterOS REST docs. Use 'fields' for .proplist instead of setting it here."`
	Limit  int               `json:"limit,omitempty"  jsonschema:"client-side cap on returned items (default 50, max 500)"`
	Offset int               `json:"offset,omitempty" jsonschema:"zero-based offset into the upstream result for client-side pagination"`
	Format string            `json:"format,omitempty" jsonschema:"response format: json or markdown (default markdown)"`
}

// PrintOut is the structured output for ros_print.
type PrintOut struct {
	Items      []any `json:"items"`
	Total      int   `json:"total"`
	HasMore    bool  `json:"has_more"`
	NextOffset int   `json:"next_offset,omitempty"`
	Status     int   `json:"status"`
}

// AddIn parameters for ros_add (PUT /rest/<path>).
type AddIn struct {
	Path   string         `json:"path"             jsonschema:"menu path, e.g. ip/firewall/address-list"`
	Body   map[string]any `json:"body"             jsonschema:"object of RouterOS properties to set on the new item"`
	Format string         `json:"format,omitempty" jsonschema:"response format: json or markdown (default json)"`
}

// MutateOut is the structured response for add/set/remove/exec tools.
type MutateOut struct {
	Result map[string]any `json:"result"`
	Status int            `json:"status"`
}

// SetIn parameters for ros_set (PATCH /rest/<path>/<id>).
type SetIn struct {
	Path   string         `json:"path"             jsonschema:"menu path, e.g. ip/address"`
	ID     string         `json:"id"               jsonschema:"RouterOS internal ID (the .id field, typically '*1', '*A', etc.)"`
	Body   map[string]any `json:"body"             jsonschema:"object of properties to update"`
	Format string         `json:"format,omitempty" jsonschema:"response format: json or markdown (default json)"`
}

// RemoveIn parameters for ros_remove (DELETE /rest/<path>/<id>).
type RemoveIn struct {
	Path string `json:"path" jsonschema:"menu path"`
	ID   string `json:"id"   jsonschema:"RouterOS internal ID to remove"`
}

// ExecIn parameters for ros_exec (POST /rest/<path>/<command>).
// Use this for commands that aren't add/set/remove: monitor, ping, reboot,
// scheduler/run, system/reboot, etc. Also for /print with a filter body.
// Destructive-action confirmation is delegated to the MCP client via the
// DestructiveHint annotation; the server does not gate calls itself.
type ExecIn struct {
	Path   string         `json:"path"            jsonschema:"full action path including command, e.g. system/reboot, interface/wireguard/peers/print, ping"`
	Body   map[string]any `json:"body,omitempty"  jsonschema:"JSON body forwarded as the action's arguments"`
	Format string         `json:"format,omitempty" jsonschema:"response format: json or markdown (default json)"`
}

// RegisterRESTTools wires the five generic REST tools onto srv.
func RegisterRESTTools(srv *mcp.Server, c *server.Client) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ros_print",
		Description: "Read items from a RouterOS menu path (GET /rest/<path>). Use for any /print equivalent: list interfaces, addresses, firewall rules, leases, etc. Pass `fields` to ask the router for only specific properties (server-side .proplist) and dramatically reduce context. Supports client-side pagination via limit/offset.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:   true,
			IdempotentHint: true,
			OpenWorldHint:  ptr(true),
		},
	}, printHandler(c))

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ros_add",
		Description: "Create a new item under a RouterOS menu path (PUT /rest/<path>). Body is the property map.",
		Annotations: &mcp.ToolAnnotations{
			DestructiveHint: ptr(false),
			OpenWorldHint:   ptr(true),
		},
	}, addHandler(c))

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ros_set",
		Description: "Update properties of an existing item by its RouterOS .id (PATCH /rest/<path>/<id>).",
		Annotations: &mcp.ToolAnnotations{
			DestructiveHint: ptr(false),
			IdempotentHint:  true,
			OpenWorldHint:   ptr(true),
		},
	}, setHandler(c))

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ros_remove",
		Description: "Delete an item by its RouterOS .id (DELETE /rest/<path>/<id>).",
		Annotations: &mcp.ToolAnnotations{
			DestructiveHint: ptr(true),
			IdempotentHint:  true,
			OpenWorldHint:   ptr(true),
		},
	}, removeHandler(c))

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ros_exec",
		Description: "Execute an arbitrary RouterOS REST action (POST /rest/<path>). Use for commands not covered by add/set/remove: ping, monitor, system/reboot, scheduler/run, /print with body filter, etc. Set acknowledged_destructive=true for reboot/reset paths.",
		Annotations: &mcp.ToolAnnotations{
			DestructiveHint: ptr(true),
			OpenWorldHint:   ptr(true),
		},
	}, execHandler(c))
}

type printFn = func(context.Context, *mcp.CallToolRequest, PrintIn) (*mcp.CallToolResult, PrintOut, error)

type mutateFnAdd = func(context.Context, *mcp.CallToolRequest, AddIn) (*mcp.CallToolResult, MutateOut, error)

type mutateFnSet = func(context.Context, *mcp.CallToolRequest, SetIn) (*mcp.CallToolResult, MutateOut, error)

type mutateFnRemove = func(context.Context, *mcp.CallToolRequest, RemoveIn) (*mcp.CallToolResult, MutateOut, error)

type mutateFnExec = func(context.Context, *mcp.CallToolRequest, ExecIn) (*mcp.CallToolResult, MutateOut, error)

func printHandler(c *server.Client) printFn {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in PrintIn) (*mcp.CallToolResult, PrintOut, error) {
		if in.Path == "" {
			return server.ToolError("path is required; example: ip/address"), PrintOut{}, nil
		}
		limit := clamp(in.Limit, defaultPageLimit, minPageLimit, maxPageLimit)
		if in.Offset < 0 {
			in.Offset = 0
		}
		if in.Format == "" {
			in.Format = formatMarkdown
		}

		query := mergeProplist(in.Query, in.Fields)
		raw, status, err := c.Do(ctx, "GET", in.Path, query, nil)
		if err != nil {
			return server.ToolError("GET /rest/%s failed: %v", in.Path, err), PrintOut{Status: status}, nil
		}

		items := toItems(raw)
		out := paginate(items, in.Offset, limit, status)
		return server.Render(in.Format, out, func() string { return renderItems(in.Path, out) }), out, nil
	}
}

func addHandler(c *server.Client) mutateFnAdd {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in AddIn) (*mcp.CallToolResult, MutateOut, error) {
		if in.Path == "" {
			return server.ToolError("path is required"), MutateOut{}, nil
		}
		if in.Format == "" {
			in.Format = formatJSON
		}
		raw, status, err := c.Do(ctx, "PUT", in.Path, nil, in.Body)
		if err != nil {
			return server.ToolError("PUT /rest/%s failed: %v", in.Path, err),
				MutateOut{Result: toMap(raw), Status: status}, nil
		}
		out := MutateOut{Result: toMap(raw), Status: status}
		return server.Render(in.Format, out, nil), out, nil
	}
}

func setHandler(c *server.Client) mutateFnSet {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in SetIn) (*mcp.CallToolResult, MutateOut, error) {
		if in.Path == "" || in.ID == "" {
			return server.ToolError("path and id are both required"), MutateOut{}, nil
		}
		if in.Format == "" {
			in.Format = formatJSON
		}
		raw, status, err := c.Do(ctx, "PATCH", in.Path+"/"+in.ID, nil, in.Body)
		if err != nil {
			return server.ToolError("PATCH /rest/%s/%s failed: %v", in.Path, in.ID, err),
				MutateOut{Result: toMap(raw), Status: status}, nil
		}
		out := MutateOut{Result: toMap(raw), Status: status}
		return server.Render(in.Format, out, nil), out, nil
	}
}

func removeHandler(c *server.Client) mutateFnRemove {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in RemoveIn) (*mcp.CallToolResult, MutateOut, error) {
		if in.Path == "" || in.ID == "" {
			return server.ToolError("path and id are both required"), MutateOut{}, nil
		}
		raw, status, err := c.Do(ctx, "DELETE", in.Path+"/"+in.ID, nil, nil)
		if err != nil {
			return server.ToolError("DELETE /rest/%s/%s failed: %v", in.Path, in.ID, err),
				MutateOut{Result: toMap(raw), Status: status}, nil
		}
		out := MutateOut{Result: toMap(raw), Status: status}
		return server.Render(formatJSON, out, nil), out, nil
	}
}

func execHandler(c *server.Client) mutateFnExec {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in ExecIn) (*mcp.CallToolResult, MutateOut, error) {
		if in.Path == "" {
			return server.ToolError("path is required; example: system/reboot or ping"), MutateOut{}, nil
		}
		if in.Format == "" {
			in.Format = formatJSON
		}
		raw, status, err := c.Do(ctx, "POST", in.Path, nil, in.Body)
		if err != nil {
			return server.ToolError("POST /rest/%s failed: %v", in.Path, err),
				MutateOut{Result: toMap(raw), Status: status}, nil
		}
		out := MutateOut{Result: toMap(raw), Status: status}
		return server.Render(in.Format, out, nil), out, nil
	}
}

// mergeProplist copies query and, if fields is non-empty, sets the RouterOS
// `.proplist` query parameter to comma-joined field names. An explicit caller
// value already in query wins.
func mergeProplist(query map[string]string, fields []string) map[string]string {
	if len(fields) == 0 {
		return query
	}
	out := make(map[string]string, len(query)+1)
	for k, v := range query {
		out[k] = v
	}
	if _, ok := out[".proplist"]; !ok {
		out[".proplist"] = strings.Join(fields, ",")
	}
	return out
}

func paginate(items []any, offset, limit, status int) PrintOut {
	if offset < 0 {
		offset = 0
	}
	if offset > len(items) {
		offset = len(items)
	}
	end := min(offset+limit, len(items))
	out := PrintOut{
		Items:   items[offset:end],
		Total:   len(items),
		HasMore: end < len(items),
		Status:  status,
	}
	if out.HasMore {
		out.NextOffset = end
	}
	return out
}

func toMap(raw any) map[string]any {
	switch v := raw.(type) {
	case nil:
		return nil
	case map[string]any:
		return v
	case []any:
		return map[string]any{"items": v}
	default:
		return map[string]any{"value": v}
	}
}

func toItems(raw any) []any {
	switch v := raw.(type) {
	case []any:
		return v
	case nil:
		return nil
	default:
		return []any{v}
	}
}

func clamp(n, def, lo, hi int) int {
	if n <= 0 {
		return def
	}
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

func ptr[T any](v T) *T { return &v }

func renderItems(path string, out PrintOut) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s (%d of %d)\n\n", path, len(out.Items), out.Total)
	for i, it := range out.Items {
		fmt.Fprintf(&b, "## item %d\n", i+1)
		m, ok := it.(map[string]any)
		if !ok {
			fmt.Fprintf(&b, "%v\n\n", it)
			continue
		}
		for k, v := range m {
			fmt.Fprintf(&b, "- **%s**: %v\n", k, v)
		}
		b.WriteString("\n")
	}
	if out.HasMore {
		fmt.Fprintf(&b, "_more results available; offset=%d_\n", out.NextOffset)
	}
	return b.String()
}
