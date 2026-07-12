package cli

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"

	"github.com/agensfield/scriba/internal/agentmcp"
)

func runMCP(opts options) error {
	cfg, err := load(opts)
	if err != nil {
		return err
	}
	if opts.statePath != "" {
		cfg.Server.StatePath = opts.statePath
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	err = agentmcp.RunStdio(ctx, agentContextService(cfg))
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		return nil
	}
	return err
}
