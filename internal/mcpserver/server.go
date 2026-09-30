// Package mcpserver exposes a Home Assistant instance as MCP tools.
//
// Two rules run through the tool surface. Tools are few and each answers a whole
// question, because what a tool costs is not the call but the schema every
// request carries and the choice the model has to make. And output is rendered
// as compact lines rather than dumped as JSON, because the JSON for a few
// hundred entities is most of a context window and the same rows as lines are a
// tenth of that.
package mcpserver

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/Barmore-Genc/mcp-for-homeassistant/internal/homeassistant"
	"github.com/Barmore-Genc/mcp-for-homeassistant/internal/oauth"
	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Server struct {
	ha      *homeassistant.Client
	signer  *oauth.Signer
	origin  string
	version string
	// readOnly drops the write tools from tools/list entirely rather than
	// failing them when called, so a model never plans around a tool it cannot
	// use.
	readOnly bool
	// now is time.Now except in tests, where relative dates need a fixed today.
	now func() time.Time
}

func New(ha *homeassistant.Client, signer *oauth.Signer, origin, version string, readOnly bool) *Server {
	return &Server{ha: ha, signer: signer, origin: origin, version: version, readOnly: readOnly, now: time.Now}
}

// Handler is the http.Handler to mount at /mcp. A request without a valid
// access token gets a 401 carrying the WWW-Authenticate header that tells an
// OAuth client where to go and authenticate.
func (s *Server) Handler() http.Handler {
	srv := s.build()
	h := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return srv }, &mcp.StreamableHTTPOptions{SessionTimeout: time.Hour})
	return sdkauth.RequireBearerToken(s.verify, &sdkauth.RequireBearerTokenOptions{
		ResourceMetadataURL: s.origin + "/.well-known/oauth-protected-resource",
	})(h)
}

// verify resolves the bearer token this server minted at /token. There is one
// user, so the token says which client is calling and nothing more.
func (s *Server) verify(_ context.Context, token string, _ *http.Request) (*sdkauth.TokenInfo, error) {
	p, err := s.signer.Verify(token, oauth.KindAccess)
	if err != nil {
		return nil, fmt.Errorf("access token not accepted: %w", sdkauth.ErrInvalidToken)
	}
	return &sdkauth.TokenInfo{UserID: p.ClientID, Expiration: p.ExpiresAt()}, nil
}

func (s *Server) build() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "homeassistant",
		Title:   "Home Assistant",
		Version: s.version,
	}, &mcp.ServerOptions{
		Instructions: "Tools for reading and changing a Home Assistant instance.",
	})
	s.addAutomationTools(srv)
	s.addStateTools(srv)
	s.addOrganizeTools(srv)
	return srv
}

// text returns a tool result as one text block. Every tool answers this way:
// what the model reads is prose and lines, and a second structured copy of the
// same data would only double the tokens.
func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

// fail turns an error into a tool result rather than a protocol error, which is
// what lets the model read the explanation and try something else.
func fail(err error) (*mcp.CallToolResult, any, error) {
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf("could not complete the request: %v", err)}},
	}, nil, nil
}

func readOnlyTool() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(true)}
}

func writeTool(destructive bool) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{DestructiveHint: ptr(destructive), OpenWorldHint: ptr(true)}
}

func ptr[T any](v T) *T { return &v }
