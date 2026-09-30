// Command mcp-for-homeassistant serves one Home Assistant instance as an MCP endpoint over HTTP,
// with its own OAuth 2.1 authorization server in front of it because that is
// the only way AI agent harnesses know how to connect.
//
// Everything it needs comes from the environment and nothing is written to
// disk, so the container is the whole deployment.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Barmore-Genc/mcp-for-homeassistant/internal/config"
	"github.com/Barmore-Genc/mcp-for-homeassistant/internal/homeassistant"
	"github.com/Barmore-Genc/mcp-for-homeassistant/internal/mcpserver"
	"github.com/Barmore-Genc/mcp-for-homeassistant/internal/oauth"
	"github.com/Barmore-Genc/mcp-for-homeassistant/internal/server"
)

// version is set by the build: the git tag in a release, dev-<date> otherwise.
var version = "dev"

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("configuration: %v", err)
	}
	for _, w := range cfg.Warnings {
		log.Printf("warning: %s", w)
	}
	// Every credential is signed with MCP_SIGNING_KEY alone, so changing it is
	// how an operator revokes every token at once. Changing the password only
	// affects future sign-ins.
	signer := oauth.NewSigner(cfg.SigningKey)

	ha, err := homeassistant.New(cfg.HAURL, cfg.HAToken, nil)
	if err != nil {
		log.Fatalf("home assistant client: %v", err)
	}
	defer ha.Close()

	mcpSrv := mcpserver.New(ha, signer, cfg.Origin, version, cfg.ReadOnly)
	srv := server.New(cfg, signer, mcpSrv.Handler())

	httpSrv := srv.HTTPServer(cfg.Addr)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		httpSrv.Shutdown(shutdown)
	}()

	mode := "read and write"
	if cfg.ReadOnly {
		mode = "read only"
	}
	signin := "password"
	if cfg.OIDC != nil {
		signin = "OIDC via " + cfg.OIDC.Issuer
	}
	log.Printf("mcp-for-homeassistant %s listening on %s, serving %s/mcp (%s, sign-in: %s)", version, cfg.Addr, cfg.Origin, mode, signin)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("http server: %v", err)
	}
}
