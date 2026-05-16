package server

import (
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Render builds a CallToolResult with text content rendered either as
// pretty-printed JSON or via the supplied markdown closure.
func Render(format string, out any, markdown func() string) *mcp.CallToolResult {
	var text string
	if format == "json" || markdown == nil {
		b, _ := json.MarshalIndent(out, "", "  ")
		text = string(b)
	} else {
		text = markdown()
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
	}
}
