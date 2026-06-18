package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/pax-beehive/pax-manager/internal/manager"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := manager.Run(ctx); err != nil {
		slog.Error("pax-manager exited", "error", err)
		os.Exit(1)
	}
}
