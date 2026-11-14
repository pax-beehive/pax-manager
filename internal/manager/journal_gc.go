package manager

import (
	"context"
	"log/slog"

	"github.com/pax-beehive/pax-manager/internal/manager/journalgc"
	"github.com/pax-beehive/pax-manager/internal/manager/logging"
	"github.com/pax-beehive/pax-manager/internal/manager/runtimeconfig"
)

// startTransportJournalGC runs cleanup while transport.journal_gc_disabled is
// false; the flag is read on every tick, so it can be toggled without restart.
func startTransportJournalGC(
	ctx context.Context,
	store Store,
	cfg Config,
	config *runtimeconfig.Store,
) (func(), error) {
	pruner, ok := store.(journalgc.Store)
	if !ok {
		return func() {}, nil
	}
	return journalgc.Start(ctx, pruner, journalgc.Options{
		Disabled: func() bool {
			return config.Snapshot().Bool("transport.journal_gc_disabled")
		},
		Retention: cfg.TransportJournalRetention,
		Interval:  cfg.TransportJournalGCInterval,
		BatchSize: cfg.TransportJournalGCBatchSize,
		Report: func(deleted int64, err error) {
			if err != nil {
				logging.Warn(ctx, "transport journal cleanup failed", logging.Err(err))
			} else if deleted > 0 {
				logging.Info(ctx, "transport journal cleanup completed", slog.Int64("deleted", deleted))
			}
		},
	})
}
