package tools

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/czechbol/routeros-mcp/server"
)

//go:embed paths.txt
var pathsBlob string

var pathIndex []string

func init() {
	lines := strings.Split(pathsBlob, "\n")
	pathIndex = make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			pathIndex = append(pathIndex, l)
		}
	}
}

// ListPathsIn parameters for the ros_list_paths tool.
type ListPathsIn struct {
	Match  string `json:"match,omitempty"  jsonschema:"substring filter; case-insensitive. Empty returns top-level paths only."`
	Limit  int    `json:"limit,omitempty"  jsonschema:"max paths to return (default 50, max 500)"`
	Offset int    `json:"offset,omitempty" jsonschema:"zero-based offset"`
}

// ListPathsOut is the structured output for ros_list_paths.
type ListPathsOut struct {
	Paths      []string `json:"paths"`
	Total      int      `json:"total"`
	HasMore    bool     `json:"has_more"`
	NextOffset int      `json:"next_offset,omitempty"`
	Note       string   `json:"note,omitempty"`
}

// RegisterDiscoveryTools wires the ros_list_paths tool onto srv.
func RegisterDiscoveryTools(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "ros_list_paths",
		Description: "Search the RouterOS REST API path catalog (RouterOS 7.22.3, 3635 paths). Use this to discover the right path before calling ros_print/ros_add/ros_set/ros_remove/ros_exec. Substring filter is case-insensitive. With no match, returns only top-level menus.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:   true,
			IdempotentHint: true,
			OpenWorldHint:  ptr(false),
		},
	}, listPaths)
}

func listPaths(
	_ context.Context, _ *mcp.CallToolRequest, in ListPathsIn,
) (*mcp.CallToolResult, ListPathsOut, error) {
	limit := clamp(in.Limit, defaultPageLimit, minPageLimit, maxPageLimit)
	if in.Offset < 0 {
		in.Offset = 0
	}

	matched := selectPaths(in.Match)

	out := ListPathsOut{Total: len(matched)}
	if in.Offset > len(matched) {
		in.Offset = len(matched)
	}
	end := min(in.Offset+limit, len(matched))
	out.Paths = matched[in.Offset:end]
	out.HasMore = end < len(matched)
	if out.HasMore {
		out.NextOffset = end
	}
	if in.Match == "" {
		out.Note = "showing top-level menus only; pass match=<substring> to drill in"
	}

	return server.Render(formatMarkdown, out, func() string { return renderPaths(in.Match, out) }), out, nil
}

func selectPaths(match string) []string {
	if match == "" {
		return topLevelMenus()
	}
	needle := strings.ToLower(match)
	matched := make([]string, 0, len(pathIndex)/8)
	for _, p := range pathIndex {
		if strings.Contains(strings.ToLower(p), needle) {
			matched = append(matched, p)
		}
	}
	return matched
}

const initialMenuCapacity = 32

func topLevelMenus() []string {
	seen := make(map[string]struct{}, initialMenuCapacity)
	out := make([]string, 0, initialMenuCapacity)
	for _, p := range pathIndex {
		parts := strings.SplitN(strings.TrimLeft(p, "/"), "/", 2)
		if len(parts) == 0 || parts[0] == "" {
			continue
		}
		top := "/" + parts[0]
		if _, ok := seen[top]; !ok {
			seen[top] = struct{}{}
			out = append(out, top)
		}
	}
	return out
}

func renderPaths(match string, out ListPathsOut) string {
	title := "top-level menus"
	if match != "" {
		title = fmt.Sprintf("paths matching %q", match)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# %s (%d of %d)\n\n", title, len(out.Paths), out.Total)
	for _, p := range out.Paths {
		fmt.Fprintf(&b, "- `%s`\n", p)
	}
	if out.HasMore {
		fmt.Fprintf(&b, "\n_more results; offset=%d_\n", out.NextOffset)
	}
	if out.Note != "" {
		b.WriteString("\n_" + out.Note + "_\n")
	}
	return b.String()
}
