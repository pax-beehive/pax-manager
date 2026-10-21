package manager

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/notifications"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
)

type acceptedNotificationSender struct{}

func (acceptedNotificationSender) Send(
	context.Context,
	notifications.RegistrationRecord,
	notifications.Payload,
	time.Time,
) notifications.AttemptResult {
	return notifications.AttemptResult{Status: 201}
}

func TestNotificationRoutesGivenRealAuthenticationThenGeneratedRoutesUseTheRegistry(t *testing.T) {
	store := NewMemoryStore(time.Now)
	svc := newServer(
		Config{
			AllowLocalUserHeader:  true,
			AdminEmails:           map[string]bool{"admin@example.invalid": true},
			NotificationPublicKey: "public-key",
			NotificationKeyID:     "dev-key",
		},
		store,
	)
	t.Cleanup(func() { require.NoError(t, svc.CloseTransportStore(t.Context())) })
	handler := svc.routes()
	call := func(method, path, email string, body []byte) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		if email != "" {
			r.Header.Set("X-User-Email", email)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	assert.Equal(t, 401, call("GET", userapi.NotificationSubscriptionsPath, "", nil).Code)
	assert.Equal(
		t,
		403,
		call("GET", userapi.NotificationSubscriptionsPath, "one@example.invalid", nil).Code,
	)
	keyBytes := make([]byte, 32)
	keyBytes[31] = 1
	key, err := ecdh.P256().NewPrivateKey(keyBytes)
	require.NoError(t, err)
	input := notifications.Registration{
		Subscription: notifications.WebPushSubscription{
			Endpoint: "https://fcm.googleapis.com/wp/route-test",
			Keys: notifications.WebPushKeys{
				P256DH: base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()),
				Auth:   base64.RawURLEncoding.EncodeToString(make([]byte, 16)),
			},
		},
		Enabled:     true,
		Permission:  "granted",
		VAPIDKeyID:  "dev-key",
		DeviceLabel: "Pixel",
	}
	body, err := json.Marshal(input)
	require.NoError(t, err)
	response := call(
		http.MethodPut,
		userapi.NotificationReceiversPath+"/web-push",
		"one@example.invalid",
		body,
	)
	assert.Equal(t, 200, response.Code, response.Body.String())
	assert.Contains(t, response.Header().Get("Content-Type"), "application/json")
	for _, path := range []string{userapi.NotificationReceiversPath, userapi.NotificationConfigPath} {
		response = call(http.MethodGet, path, "one@example.invalid", nil)
		assert.Equal(t, 200, response.Code, response.Body.String())
	}
	response = call(
		http.MethodGet,
		userapi.NotificationSubscriptionsPath,
		"admin@example.invalid",
		nil,
	)
	assert.Equal(t, 200, response.Code, response.Body.String())
	assert.Contains(t, response.Body.String(), "Pixel")
	assert.NotContains(t, response.Body.String(), "fcm.googleapis.com")
	svc.notificationSender = acceptedNotificationSender{}
	configuration := decodeData[struct {
		TurnCompletionEnabled bool `json:"turn_completion_enabled"`
	}](t,
		call("GET", userapi.NotificationConfigPath, "one@example.invalid", nil).Body.Bytes())
	assert.True(t, configuration.TurnCompletionEnabled)
	testPath := userapi.NotificationReceiversPath + "/" + notifications.ReceiverID(
		input.Subscription.Endpoint,
	) + "/test"
	assert.Equal(t, 401, call("POST", testPath, "", nil).Code)
	assert.Equal(t, 404, call("POST", testPath, "other@example.invalid", nil).Code)
	response = call("POST", testPath, "one@example.invalid", nil)
	require.Equal(t, 202, response.Code, response.Body.String())
	assert.Equal(t, 429, call("POST", testPath, "one@example.invalid", nil).Code)
	worker := notifications.Worker{
		Store:  svc.notificationOutbox,
		Sender: svc.notificationSender,
		KeyID:  "dev-key",
		Now:    time.Now,
	}
	require.NoError(t, worker.Drain(t.Context()))
	response = call("GET", userapi.NotificationReceiversPath, "one@example.invalid", nil)
	assert.Contains(t, response.Body.String(), `"status":"accepted"`)
	response = call(
		http.MethodDelete,
		userapi.NotificationReceiversPath+"/"+notifications.ReceiverID(input.Subscription.Endpoint),
		"one@example.invalid",
		nil,
	)
	assert.Equal(t, 200, response.Code, response.Body.String())
	response = call(
		http.MethodGet,
		userapi.NotificationSubscriptionsPath,
		"admin@example.invalid",
		nil,
	)
	assert.Contains(t, response.Body.String(), `"revoked":1`)
}
