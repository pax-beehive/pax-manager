package manager

import (
	"context"
	"fmt"
	"log/slog"
	"runtime/debug"

	"golang.org/x/sync/errgroup"

	"github.com/pax-beehive/pax-manager/internal/manager/logging"
)

type acpActor struct {
	name  string
	attrs []slog.Attr
	run   func(context.Context) error
}

func runACPActors(ctx context.Context, actors ...acpActor) error {
	group, actorCtx := errgroup.WithContext(ctx)
	for _, actor := range actors {
		actor := actor
		group.Go(func() (err error) {
			attrs := append([]slog.Attr{slog.String("actor", actor.name)}, actor.attrs...)
			logging.Info(actorCtx, "acp actor started", attrs...)
			defer func() {
				if recovered := recover(); recovered != nil {
					err = fmt.Errorf("acp actor panic: %v", recovered)
					logging.Error(
						actorCtx,
						"acp actor panic recovered",
						append(
							attrs,
							slog.String("panic", fmt.Sprint(recovered)),
							slog.String("stack", string(debug.Stack())),
						)...,
					)
				}
				if err != nil {
					logging.Warn(
						actorCtx,
						"acp actor exited",
						append(attrs, logging.Err(err))...,
					)
					return
				}
				logging.Info(actorCtx, "acp actor exited", attrs...)
			}()
			return actor.run(actorCtx)
		})
	}
	return group.Wait()
}
