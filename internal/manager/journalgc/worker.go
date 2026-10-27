// Package journalgc bounds retained transport history without blocking startup.
package journalgc

import (
	"context"
	"fmt"
	"time"
)

type Store interface {
	PruneCompletedTransportFrames(context.Context, time.Time, int, int64) (int64, error)
}

type Options struct {
	Retention time.Duration
	Interval  time.Duration
	Timeout   time.Duration
	BatchSize int
	KeepTail  int64
	Now       func() time.Time
	Report    func(int64, error)
}

func (o Options) defaults() (Options, error) {
	if o.Retention < 0 || o.Interval < 0 || o.Timeout < 0 ||
		o.BatchSize < 0 || o.BatchSize > 10000 || o.KeepTail < 0 {
		return o, fmt.Errorf("invalid transport journal retention policy")
	}
	if o.Retention == 0 {
		o.Retention = 72 * time.Hour
	}
	if o.Interval == 0 {
		o.Interval = 10 * time.Second
	}
	if o.Timeout == 0 {
		o.Timeout = 5 * time.Second
	}
	if o.BatchSize == 0 {
		o.BatchSize = 1000
	}
	if o.KeepTail == 0 {
		o.KeepTail = 100
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	return o, nil
}

// Start waits until the first tick and performs at most one batch per tick.
// The returned stop function cancels and joins the worker before storage closes.
func Start(ctx context.Context, store Store, options Options) (func(), error) {
	options, err := options.defaults()
	if err != nil {
		return nil, err
	}
	if store == nil {
		return nil, fmt.Errorf("transport journal cleanup store is required")
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(options.Interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if ctx.Err() != nil {
					return
				}
				batchCtx, stop := context.WithTimeout(ctx, options.Timeout)
				deleted, err := store.PruneCompletedTransportFrames(
					batchCtx, options.Now().UTC().Add(-options.Retention),
					options.BatchSize, options.KeepTail,
				)
				stop()
				if ctx.Err() == nil && options.Report != nil {
					options.Report(deleted, err)
				}
			}
		}
	}()
	return func() { cancel(); <-done }, nil
}
