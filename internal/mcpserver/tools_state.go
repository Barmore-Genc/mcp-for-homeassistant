package mcpserver

import "github.com/modelcontextprotocol/go-sdk/mcp"

// addStateTools registers the tools for entity states, services, events, history and logs. Write tools are skipped when s.readOnly is set.
func (s *Server) addStateTools(srv *mcp.Server) {}
