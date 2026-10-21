package manager

import (
	"context"

	"github.com/pax-beehive/pax-manager/internal/manager/logging"
	"github.com/pax-beehive/pax-manager/internal/manager/notifications"
)

func (s *Service) startNotificationWorker(parent context.Context) (func(), error) {
	if s.cfg.NotificationPrivateKey == "" {
		return func() {}, nil
	}
	sender, err := notifications.NewWebPushSender(notifications.WebPushConfig{
		PublicKey: s.cfg.NotificationPublicKey, PrivateKey: s.cfg.NotificationPrivateKey,
		Subscriber: s.cfg.NotificationSubscriber, AllowedHosts: s.cfg.NotificationAllowedHosts,
	})
	if err != nil {
		return nil, err
	}
	s.notificationSender = sender
	if s.notificationOutbox == nil {
		return func() {}, nil
	}
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	worker := notifications.Worker{
		Store:   s.notificationOutbox,
		Sender:  sender,
		KeyID:   s.cfg.NotificationKeyID,
		Now:     s.clock,
		OnError: func() { logging.Warn(ctx, "notification worker storage operation failed") },
	}
	go func() { defer close(done); worker.Run(ctx) }()
	return func() { cancel(); <-done }, nil
}
