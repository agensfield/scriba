package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/agensfield/scriba/internal/agentcontext"
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
	statePath := cfg.Server.StatePath
	if opts.statePath != "" {
		statePath = opts.statePath
	}
	payload, err := agentcontext.New(agentcontext.Config{
		CacheDir:  cfg.CacheDir,
		StorePath: resolveServerStatePath(statePath),
		ProfileID: "default",
	}).Context(ctx)
	if err != nil {
		return err
	}
	return printJSON(payload, false)
}
