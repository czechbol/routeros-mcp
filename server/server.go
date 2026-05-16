package server

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Version is the advertised routeros-mcp server version reported to clients.
const Version = "0.1.0"

// NewMCP returns a server with no tools registered. The caller (typically
// main) wires in tool packages to avoid an import cycle.
func NewMCP() *mcp.Server {
	return mcp.NewServer(&mcp.Implementation{
		Name:    "routeros-mcp",
		Version: Version,
	}, nil)
}
