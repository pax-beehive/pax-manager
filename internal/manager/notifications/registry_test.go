package notifications

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegistryGivenRefreshWhenMaterialUnchangedThenRevisionAndIdentityRemainStable(
	t *testing.T,
) {
	now := testCompletion().CompletedAt
	input := Registration{
		Subscription: testSubscription(t),
		DeviceLabel:  "Pixel",
		Permission:   "granted",
		Enabled:      true,
		VAPIDKeyID:   "key-one",
	}
	record, err := RefreshRegistration(RegistrationRecord{}, "user-one", input, now)
	require.NoError(t, err)
	assert.Equal(t, int64(1), record.Revision)
	assert.Equal(t, SubscriptionEnabled, record.Metadata("one@example.invalid", now).Status)
	refreshed, err := RefreshRegistration(record, "user-one", input, now.Add(time.Minute))
	require.NoError(t, err)
	assert.Equal(t, record.ID, refreshed.ID)
	assert.Equal(t, record.Revision, refreshed.Revision)
	assert.Equal(t, record.CreatedAt, refreshed.CreatedAt)
	assert.Equal(t, now.Add(time.Minute), refreshed.LastRegisteredAt)
	input.Enabled = false
	disabled, err := RefreshRegistration(refreshed, "user-one", input, now.Add(2*time.Minute))
	require.NoError(t, err)
	assert.Equal(t, int64(2), disabled.Revision)
	assert.Equal(t, SubscriptionDisabled, disabled.Metadata("", now).Status)
	_, err = RefreshRegistration(record, "different-user", input, now)
	assert.ErrorIs(t, err, ErrReceiverConflict)
	data, err := json.Marshal(record.Metadata("one@example.invalid", now))
	require.NoError(t, err)
	for _, secret := range []string{input.Subscription.Endpoint, input.Subscription.Keys.Auth, input.Subscription.Keys.P256DH, "subscription", "endpoint", "p256dh"} {
		assert.NotContains(t, string(data), secret)
	}
}

func TestRegistryGivenTerminalStatesWhenProjectedThenStatusIsTruthful(t *testing.T) {
	now := testCompletion().CompletedAt
	record := RegistrationRecord{Receiver: Receiver{Enabled: true}}
	assert.Equal(t, SubscriptionEnabled, record.Metadata("", now).Status)
	record.ExpiresAt = &now
	assert.Equal(t, SubscriptionExpired, record.Metadata("", now).Status)
	record.InvalidatedAt = &now
	assert.Equal(t, SubscriptionInvalidated, record.Metadata("", now).Status)
	record.RevokedAt = &now
	assert.Equal(t, SubscriptionRevoked, record.Metadata("", now).Status)
	assert.Nil(t, record.Metadata("", now).LatestDelivery)
}

func TestRegistryGivenBrowserExpirationWhenRegisteredThenExpiryIsPreservedAndRefreshIsIdempotent(
	t *testing.T,
) {
	now := testCompletion().CompletedAt
	millis := now.Add(time.Hour).UnixMilli()
	input := Registration{
		Subscription: testSubscription(t),
		Permission:   "granted",
		Enabled:      true,
		VAPIDKeyID:   "key-one",
	}
	input.Subscription.ExpirationTime = &millis
	record, err := RefreshRegistration(RegistrationRecord{}, "user-one", input, now)
	require.NoError(t, err)
	require.NotNil(t, record.ExpiresAt)
	assert.Equal(t, time.UnixMilli(millis).UTC(), *record.ExpiresAt)
	assert.Nil(t, record.Subscription.ExpirationTime)
	refreshed, err := RefreshRegistration(record, "user-one", input, now.Add(time.Minute))
	require.NoError(t, err)
	assert.Equal(t, record.Revision, refreshed.Revision)
	millis = now.Add(-time.Minute).UnixMilli()
	_, err = RefreshRegistration(record, "user-one", input, now)
	assert.ErrorIs(t, err, ErrInvalidRegistration)
	millis = int64(1<<63 - 1)
	_, err = RefreshRegistration(record, "user-one", input, now)
	assert.ErrorIs(t, err, ErrInvalidRegistration)
}

func TestRegistryGivenInvalidInputThenRegistrationOrFilterIsRejected(t *testing.T) {
	now := testCompletion().CompletedAt
	valid := Registration{
		Subscription: testSubscription(t),
		Enabled:      true,
		Permission:   "granted",
		VAPIDKeyID:   "key-one",
	}
	for _, change := range []func(*Registration){
		func(r *Registration) { r.DeviceLabel = strings.Repeat("x", 121) },
		func(r *Registration) { r.VAPIDKeyID = "" },
		func(r *Registration) { r.Permission = "online" },
		func(r *Registration) { r.Subscription.Endpoint = "http://invalid" },
		func(r *Registration) { r.ExpiresAt = &now },
	} {
		input := valid
		change(&input)
		_, err := RefreshRegistration(RegistrationRecord{}, "user-one", input, now)
		assert.ErrorIs(t, err, ErrInvalidRegistration)
	}
	_, err := RefreshRegistration(RegistrationRecord{}, "", valid, now)
	assert.ErrorIs(t, err, ErrInvalidRegistration)
	for _, filter := range []SubscriptionFilter{
		{Limit: -1}, {Limit: 101}, {Status: "online"}, {Channel: "apns"},
		{Search: strings.Repeat("x", 201)}, {After: strings.Repeat("x", 81)},
	} {
		assert.Error(t, filter.Validate())
	}
	assert.NoError(t, (SubscriptionFilter{}).Validate())
}
