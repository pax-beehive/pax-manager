package manager

import (
	"context"
	"log/slog"

	"github.com/pax-beehive/pax-manager/internal/manager/journalgc"
	"github.com/pax-beehive/pax-manager/internal/manager/logging"
)

func startTransportJournalGC(ctx context.Context, store Store, cfg Config) (func(), error) {
	pruner, ok := store.(journalgc.Store)
	if cfg.TransportJournalGCDisabled || !ok {
		return func() {}, nil
	}
	return journalgc.Start(ctx, pruner, journalgc.Options{
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
