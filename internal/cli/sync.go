package cli

import (
	"context"
	"fmt"

	"github.com/NullifiedSec/gitmirror/internal/config"
	"github.com/NullifiedSec/gitmirror/internal/poller"
	"github.com/NullifiedSec/gitmirror/internal/queue"
	"github.com/NullifiedSec/gitmirror/internal/syncer"
)

type directEnqueuer struct {
	ctx       context.Context
	processor *syncer.Syncer
	processed int
}

func (d *directEnqueuer) Enqueue(event queue.Event) (bool, error) {
	if err := d.processor.Process(d.ctx, event); err != nil {
		return false, err
	}
	d.processed++
	return true, nil
}

func (a *App) runSync(configPath string, args []string) error {
	if len(args) != 0 {
		return fmt.Errorf("usage: gitmirror [--config PATH] sync")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	ctx := context.Background()
	processor := syncer.New(cfg)
	direct := &directEnqueuer{ctx: ctx, processor: processor}
	if err := poller.New(cfg, direct).ReconcileAll(ctx); err != nil {
		return err
	}

	fmt.Fprintf(a.Out, "Manual reconciliation complete: %d pair(s), %d branch transition(s) processed.\n", len(cfg.Pairs), direct.processed)
	return nil
}
