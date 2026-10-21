package userapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/notifications"
)

type notificationRegistryFixture struct {
	calls  int
	filter notifications.SubscriptionFilter
	owner  string
	err    error
}

type notificationOutboxFixture struct {
	notifications.Outbox
	owner string
	id    string
	err   error
	calls int
}

func (f *notificationOutboxFixture) EnqueueNotificationTest(
	_ context.Context,
	owner, id string,
) (notifications.DeliveryMetadata, error) {
	f.owner, f.id = owner, id
	f.calls++
	return notifications.DeliveryMetadata{Status: "pending"}, f.err
}
func TestNotificationHTTPGivenTestRequestThenOnlyOwnedQueuedDeliveryIsAccepted(t *testing.T) {
	outbox := &notificationOutboxFixture{}
	handler := &NotificationHandler{
		Registry:       &notificationRegistryFixture{},
		Outbox:         outbox,
		PublicKey:      "public",
		SendingEnabled: true,
		Now:            time.Now,
		Principal: func(*http.Request) (domain.UserPrincipal, error) {
			return domain.UserPrincipal{User: domain.User{UserID: "owner"}}, nil
		},
	}
	call := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest("POST", path, nil))
		return w
	}
	path := NotificationReceiversPath + "/receiver/test"
	response := call(path)
	require.Equal(t, 202, response.Code)
	assert.Equal(t, "owner", outbox.owner)
	assert.Equal(t, "receiver", outbox.id)
	assert.Contains(t, response.Body.String(), "pending")
	for _, tc := range []struct {
		err    error
		status int
	}{{notifications.ErrReceiverNotFound, 404}, {notifications.ErrReceiverDisabled, 409}, {notifications.ErrTestRateLimited, 429}, {errors.New("private provider response"), 503}} {
		outbox.err = tc.err
		response = call(path)
		assert.Equal(t, tc.status, response.Code)
		assert.NotContains(t, response.Body.String(), "private provider")
		if tc.status == 429 {
			assert.Equal(t, "30", response.Header().Get("Retry-After"))
		}
	}
	assert.Equal(t, 404, call(NotificationReceiversPath+"/a/b/test").Code)
	assert.Equal(t, 404, call(NotificationReceiversPath+"//test").Code)
	handler.SendingEnabled = false
	assert.Equal(t, 503, call(path).Code)
}

func (f *notificationRegistryFixture) RegisterNotificationReceiver(
	_ context.Context,
	owner string,
	_ notifications.Registration,
) (notifications.ReceiverMetadata, error) {
	f.calls++
	f.owner = owner
	return notifications.ReceiverMetadata{ID: "receiver"}, f.err
}

func (f *notificationRegistryFixture) RevokeNotificationReceiver(
	_ context.Context,
	owner, id string,
) error {
	f.calls++
	f.owner = owner
	return f.err
}

func (f *notificationRegistryFixture) ListNotificationSubscriptions(
	_ context.Context,
	filter notifications.SubscriptionFilter,
) (notifications.SubscriptionPage, error) {
	f.calls++
	f.filter = filter
	return notifications.SubscriptionPage{Receivers: []notifications.ReceiverMetadata{}}, f.err
}

func TestNotificationHTTPGivenRequestsThenAuthorizationPrecedesMetadataReads(t *testing.T) {
	fixture := &notificationRegistryFixture{}
	principal := domain.UserPrincipal{User: domain.User{UserID: "authenticated-user"}}
	var authErr error
	handler := &NotificationHandler{Registry: fixture, Region: "us", Now: time.Now,
		Principal: func(*http.Request) (domain.UserPrincipal, error) { return principal, authErr }, PublicKey: "public-key", KeyID: "dev-key"}
	call := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, httptest.NewRequest(method, path, strings.NewReader(body)))
		return w
	}
	authErr = errors.New("secret auth detail")
	assert.Equal(t, 401, call("GET", NotificationSubscriptionsPath, "").Code)
	authErr = nil
	assert.Equal(t, 403, call("GET", NotificationSubscriptionsPath, "").Code)
	assert.Zero(t, fixture.calls)
	assert.Equal(t, 200, call("GET", NotificationReceiversPath+"?user_id=other", "").Code)
	assert.Equal(t, principal.User.UserID, fixture.filter.UserID)
	principal.IsAdmin = true
	response := call(
		"GET",
		NotificationSubscriptionsPath+"?search=pixel&status=enabled&limit=2&user_id=chosen",
		"",
	)
	assert.Equal(t, 200, response.Code)
	assert.Equal(t, "chosen", fixture.filter.UserID)
	assert.Equal(t, 2, fixture.filter.Limit)
	assert.Contains(t, response.Body.String(), `"region":"us"`)
	assert.Contains(t, response.Header().Get("Cache-Control"), "no-store")
	assert.Equal(t, 400, call("GET", NotificationSubscriptionsPath+"?limit=no", "").Code)
	assert.Equal(t, 400, call("GET", NotificationSubscriptionsPath+"?status=online", "").Code)
	assert.Equal(t, 200, call("GET", NotificationConfigPath, "").Code)
	body := `{"subscription":{},"device_label":"Pixel","enabled":true,"permission":"granted","vapid_key_id":"dev-key"}`
	assert.Equal(t, 200, call("PUT", NotificationReceiversPath+"/web-push", body).Code)
	assert.Equal(t, principal.User.UserID, fixture.owner)
	assert.Equal(t, 400, call("PUT", NotificationReceiversPath+"/web-push", body+" {}").Code)
	assert.Equal(
		t,
		400,
		call("PUT", NotificationReceiversPath+"/web-push", `{"user_id":"other"}`).Code,
	)
	assert.Equal(t, 400, call("PUT", NotificationReceiversPath+"/web-push", `{`).Code)
	assert.Equal(
		t,
		409,
		call(
			"PUT",
			NotificationReceiversPath+"/web-push",
			strings.ReplaceAll(body, "dev-key", "old-key"),
		).Code,
	)
	assert.Equal(t, 200, call("DELETE", NotificationReceiversPath+"/receiver", "").Code)
	assert.Equal(t, 405, call("POST", NotificationSubscriptionsPath, "").Code)
	assert.Equal(t, 404, call("GET", NotificationReceiversPath+"/unknown", "").Code)
	for _, tc := range []struct {
		err  error
		code int
	}{
		{notifications.ErrInvalidRegistration, 400}, {notifications.ErrReceiverConflict, 409},
		{notifications.ErrReceiverLimit, 409}, {notifications.ErrReceiverNotFound, 404}, {errors.New("secret database detail"), 503},
	} {
		fixture.err = tc.err
		response = call("DELETE", NotificationReceiversPath+"/receiver", "")
		assert.Equal(t, tc.code, response.Code)
		assert.NotContains(t, response.Body.String(), "secret database")
	}
	fixture.err = errors.New("offline")
	assert.Equal(t, 503, call("GET", NotificationSubscriptionsPath, "").Code)
	assert.Equal(t, 503, call("PUT", NotificationReceiversPath+"/web-push", body).Code)
	handler.PublicKey = ""
	fixture.err = nil
	assert.Contains(
		t,
		call("GET", NotificationSubscriptionsPath, "").Body.String(),
		`"registration_enabled":false`,
	)
	assert.Contains(t, call("GET", NotificationConfigPath, "").Body.String(), `"enabled":false`)
	assert.Equal(t, 503, call("PUT", NotificationReceiversPath+"/web-push", body).Code)
	handler.Registry = nil
	assert.Equal(t, 503, call("GET", NotificationSubscriptionsPath, "").Code)
}
