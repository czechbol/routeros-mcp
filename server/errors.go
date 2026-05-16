package server

import (
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ToolError returns a tool-level error result with a printf-formatted message.
// Callers should also return a nil Go error; the protocol channel stays open.
func ToolError(format string, args ...any) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(format, args...)}},
	}
}
