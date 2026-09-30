package mcpserver

import "github.com/modelcontextprotocol/go-sdk/mcp"

// addReadTools registers the read-only tools, which stay available when MCP_READ_ONLY is set.
func (s *Server) addReadTools(srv *mcp.Server) {}
