package notifications

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type pushHTTPFunc func(*http.Request) (*http.Response, error)

func (f pushHTTPFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }
func testPushConfig(t *testing.T) WebPushConfig {
	t.Helper()
	private := make([]byte, 32)
	private[31] = 2
	key, err := ecdh.P256().NewPrivateKey(private)
	require.NoError(t, err)
	return WebPushConfig{
		PublicKey:  base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()),
		PrivateKey: base64.RawURLEncoding.EncodeToString(private),
		Subscriber: "mailto:push@example.invalid",
	}
}
func TestWebPushGivenInvalidKeysOrContactThenConfigurationIsRejected(t *testing.T) {
	for _, change := range []func(*WebPushConfig){func(c *WebPushConfig) { c.PrivateKey = "!" }, func(c *WebPushConfig) { c.PrivateKey = "AA" }, func(c *WebPushConfig) { c.PublicKey = "other" }, func(c *WebPushConfig) { c.Subscriber = "http://example.invalid" }, func(c *WebPushConfig) { c.Subscriber = "mailto:invalid" }, func(c *WebPushConfig) { c.Subscriber = "https:" }, func(c *WebPushConfig) { c.Subscriber = "https://%" }} {
		config := testPushConfig(t)
		change(&config)
		_, err := NewWebPushSender(config)
		assert.ErrorIs(t, err, ErrPushConfiguration)
	}
	config := testPushConfig(t)
	config.Subscriber = "https://example.invalid/contact"
	assert.NoError(t, config.Validate())
}
func TestWebPushGivenProviderResponseThenPayloadIsEncryptedAndAcceptanceIsTruthful(t *testing.T) {
	now := time.Now().UTC()
	record := RegistrationRecord{Receiver: Receiver{Subscription: testSubscription(t)}}
	for _, retry := range []string{"60", now.Add(2 * time.Minute).Format(http.TimeFormat), "invalid"} {
		sender, err := NewWebPushSender(testPushConfig(t))
		require.NoError(t, err)
		sender.now = func() time.Time { return now }
		sender.client = pushHTTPFunc(func(request *http.Request) (*http.Response, error) {
			assert.Equal(t, "aes128gcm", request.Header.Get("Content-Encoding"))
			assert.Equal(t, "299", request.Header.Get("TTL"))
			assert.Contains(t, request.Header.Get("Authorization"), "vapid ")
			body, err := io.ReadAll(request.Body)
			require.NoError(t, err)
			assert.Greater(t, len(body), 100)
			assert.NotContains(t, string(body), "Notifications are working")
			return &http.Response{
				StatusCode: 429,
				Header:     http.Header{"Retry-After": []string{retry}},
				Body:       io.NopCloser(strings.NewReader("private provider response")),
			}, nil
		})
		result := sender.Send(
			t.Context(),
			record,
			BuildTestNotification("owner", "nonce", now).Payload,
			now.Add(299*time.Second),
		)
		assert.Equal(t, 429, result.Status)
		assert.Equal(t, "provider_rejected", result.ErrorCategory)
		if retry != "invalid" {
			assert.Positive(t, result.RetryAfter)
		}
	}
	sender, err := NewWebPushSender(testPushConfig(t))
	require.NoError(t, err)
	sender.client = pushHTTPFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: 201,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader("")),
		}, nil
	})
	assert.Equal(t, 201, sender.Send(t.Context(), record, Payload{}, now.Add(time.Minute)).Status)
	sender.client = pushHTTPFunc(
		func(*http.Request) (*http.Response, error) { return nil, errors.New("private transport details") },
	)
	assert.Equal(
		t,
		"transport",
		sender.Send(t.Context(), record, Payload{}, now.Add(time.Minute)).ErrorCategory,
	)
	assert.True(t, sender.Send(t.Context(), record, Payload{}, now.Add(-time.Minute)).Suppressed)
	record.Subscription.Endpoint = "https://attacker.example.invalid/path"
	assert.Equal(
		t,
		"destination_not_allowed",
		sender.Send(t.Context(), record, Payload{}, now.Add(time.Minute)).ErrorCategory,
	)
}
func TestWebPushGivenUntrustedResolvedDestinationsThenNoPrivateNetworkDialOccurs(t *testing.T) {
	sender, err := NewWebPushSender(testPushConfig(t))
	require.NoError(t, err)
	for _, address := range []string{"127.0.0.1", "10.1.2.3", "169.254.169.254", "100.64.0.1", "192.0.2.1", "::1", "fc00::1", "fe80::1", "::ffff:127.0.0.1", "2001:db8::1", "64:ff9b::a00:1", "0.0.0.0", "224.0.0.1", "invalid"} {
		assert.False(t, publicPushIP(net.ParseIP(address)), address)
	}
	assert.True(t, publicPushIP(net.ParseIP("8.8.8.8")))
	assert.True(t, sender.allowedHost("updates.push.services.mozilla.com"))
	assert.False(t, sender.allowedHost("push.services.mozilla.com.attacker.invalid"))
	assert.False(t, sender.allowedHost("push.services.mozilla.com"))
	sender.lookupIP = func(context.Context, string, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("127.0.0.1")}, nil
	}
	sender.dial = func(context.Context, string, string) (net.Conn, error) {
		t.Fatal("must not dial private IP")
		return nil, nil
	}
	_, err = sender.dialContext(t.Context(), "tcp", "fcm.googleapis.com:443")
	assert.Error(t, err)
	for _, address := range []string{"fcm.googleapis.com:80", "attacker.example.invalid:443", "invalid"} {
		_, err = sender.dialContext(t.Context(), "tcp", address)
		assert.Error(t, err)
	}
	sender.lookupIP = func(context.Context, string, string) ([]net.IP, error) { return nil, errors.New("DNS unavailable") }
	_, err = sender.dialContext(t.Context(), "tcp", "fcm.googleapis.com:443")
	assert.Error(t, err)
	sender.lookupIP = func(context.Context, string, string) ([]net.IP, error) { return []net.IP{net.ParseIP("8.8.8.8")}, nil }
	sender.dial = func(_ context.Context, _ string, address string) (net.Conn, error) {
		assert.Equal(t, "8.8.8.8:443", address)
		return nil, errors.New("offline")
	}
	_, err = sender.dialContext(t.Context(), "tcp", "fcm.googleapis.com:443")
	assert.Error(t, err)
	sender.dial = func(context.Context, string, string) (net.Conn, error) {
		a, b := net.Pipe()
		t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
		return a, nil
	}
	conn, err := sender.dialContext(t.Context(), "tcp", "fcm.googleapis.com:443")
	require.NoError(t, err)
	require.NotNil(t, conn)
	client := sender.client.(*http.Client)
	assert.ErrorIs(t, client.CheckRedirect(nil, nil), http.ErrUseLastResponse)
}
