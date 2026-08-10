package manager

import (
	"context"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pax-beehive/pax-manager/internal/manager/logging"
)

const (
	e2eeCommandChannel = "pax_agent_commands"
	e2eeEventChannel   = "pax_agent_events"
)

type e2eeNotificationListener struct {
	databaseURL string
	onCommand   func(string)
	onEvent     func(string)
	onReconnect func()
}

func newE2EENotificationListener(databaseURL string, service *Service) *e2eeNotificationListener {
	return &e2eeNotificationListener{
		databaseURL: databaseURL,
		onCommand:   service.acpTunnels.wakeAgent,
		onEvent:     service.e2eeEventWakes.wake,
		onReconnect: func() {
			service.acpTunnels.wakeAllAgents()
			service.e2eeEventWakes.wakeAll()
		},
	}
}

func (l *e2eeNotificationListener) Run(ctx context.Context) {
	backoff := 100 * time.Millisecond
	for ctx.Err() == nil {
		err := l.listen(ctx)
		if ctx.Err() != nil {
			return
		}
		logging.Warn(
			ctx,
			"E2EE PostgreSQL listener disconnected",
			slog.String("error", err.Error()),
		)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		backoff *= 2
		if backoff > 10*time.Second {
			backoff = 10 * time.Second
		}
	}
}

func (l *e2eeNotificationListener) listen(ctx context.Context) error {
	conn, err := pgx.Connect(ctx, l.databaseURL)
	if err != nil {
		return err
	}
	defer func() { _ = conn.Close(context.Background()) }()
	if _, err := conn.Exec(ctx, "LISTEN "+e2eeCommandChannel); err != nil {
		return err
	}
	if _, err := conn.Exec(ctx, "LISTEN "+e2eeEventChannel); err != nil {
		return err
	}
	l.onReconnect()
	logging.Info(ctx, "E2EE PostgreSQL listener ready")
	for {
		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		l.dispatch(notification.Channel, notification.Payload)
	}
}

func (l *e2eeNotificationListener) dispatch(channel string, payload string) {
	switch channel {
	case e2eeCommandChannel:
		l.onCommand(payload)
	case e2eeEventChannel:
		l.onEvent(payload)
	}
}
