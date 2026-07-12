package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

func runContext(opts options) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runContextWithContext(ctx, opts)
}

func runContextWithContext(ctx context.Context, opts options) error {
	cfg, err := load(opts)
	if err != nil {
		return err
	}
	if opts.statePath != "" {
		cfg.Server.StatePath = opts.statePath
	}
	payload, err := agentContextService(cfg).ContextForProfile(ctx, opts.profile)
	if err != nil {
		return err
	}
	return printJSON(payload, false)
}
