package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/ctfagentpi/ctfagentpi/internal/agent"
	"github.com/ctfagentpi/ctfagentpi/internal/api"
	"github.com/ctfagentpi/ctfagentpi/internal/appdata"
	"github.com/ctfagentpi/ctfagentpi/internal/envfile"
	"github.com/ctfagentpi/ctfagentpi/internal/eventhub"
	"github.com/ctfagentpi/ctfagentpi/internal/modelgateway"
	"github.com/ctfagentpi/ctfagentpi/internal/sandbox"
	"github.com/ctfagentpi/ctfagentpi/internal/storage"
)

func Run() error {
	loadedEnv, err := envfile.Load()
	if err != nil {
		return err
	}
	if loadedEnv != "" {
		slog.Info("loaded daemon configuration from .env", "path", loadedEnv)
	}
	paths, err := appdata.Resolve()
	if err != nil {
		return err
	}
	token, err := appdata.LoadOrCreateToken(paths.Token)
	if err != nil {
		return fmt.Errorf("load daemon token: %w", err)
	}
	address := appdata.Address()
	if err := appdata.WriteConnection(paths.Connection, address, token); err != nil {
		return fmt.Errorf("write daemon connection: %w", err)
	}
	store, err := storage.Open(paths.Database)
	if err != nil {
		return err
	}
	defer store.Close()
	sandboxes, err := sandbox.New()
	if err != nil {
		return err
	}
	defer sandboxes.Close()
	gateway, err := modelgateway.New(modelgateway.Config{
		UpstreamBaseURL:    os.Getenv("CTF_UPSTREAM_MODEL_BASE_URL"),
		UpstreamAPIKey:     os.Getenv("CTF_UPSTREAM_MODEL_API_KEY"),
		ModelID:            os.Getenv("CTF_MODEL_ID"),
		IncludeStreamUsage: streamUsageEnabled(os.Getenv("CTF_MODEL_INCLUDE_STREAM_USAGE")),
	})
	if err != nil {
		return err
	}
	// The model gateway is the only component that receives every sandbox
	// request, making it the authoritative place to persist Token accounting.
	gateway.SetUsageRecorder(store)
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid daemon address %q: %w", address, err)
	}
	hub := eventhub.New()
	agents := agent.NewService(store, hub, sandboxes, gateway, paths.Workspaces, "http://host.docker.internal:"+port)
	server := api.New(address, token, store, hub, agents, sandboxes, gateway)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	result := make(chan error, 1)
	go func() { result <- server.ListenAndServe() }()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		slog.Info("shutting down CTF-BTFly daemon")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}

func streamUsageEnabled(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "0", "false", "no", "off":
		return false
	default:
		return true
	}
}
