package manager

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/notifications"
)

type unavailableNotificationOutbox struct {
	notifications.Outbox
	called chan struct{}
	once   sync.Once
}

func (s *unavailableNotificationOutbox) ClaimNotificationDeliveries(
	context.Context,
	int,
	time.Duration,
) ([]notifications.LeasedDelivery, error) {
	s.once.Do(func() { close(s.called) })
	return nil, errors.New("offline")
}
func TestNotificationWorkerGivenConfigurationThenStartsAndStopsWithTheServer(t *testing.T) {
	s := &Service{clock: time.Now}
	stop, err := s.startNotificationWorker(t.Context())
	require.NoError(t, err)
	stop()
	s.cfg.NotificationPrivateKey = "invalid"
	_, err = s.startNotificationWorker(t.Context())
	require.Error(t, err)
	bytes := make([]byte, 32)
	bytes[31] = 3
	key, err := ecdh.P256().NewPrivateKey(bytes)
	require.NoError(t, err)
	s.cfg.NotificationPrivateKey = base64.RawURLEncoding.EncodeToString(bytes)
	s.cfg.NotificationPublicKey = base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())
	s.cfg.NotificationSubscriber = "mailto:push@example.invalid"
	stop, err = s.startNotificationWorker(t.Context())
	require.NoError(t, err)
	stop()
	assert.NotNil(t, s.notificationSender)
	outbox := &unavailableNotificationOutbox{called: make(chan struct{})}
	s.notificationOutbox = outbox
	stop, err = s.startNotificationWorker(t.Context())
	require.NoError(t, err)
	select {
	case <-outbox.called:
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not start")
	}
	stop()
}
