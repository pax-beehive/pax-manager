package notifications

import (
	"crypto/ecdh"
	"encoding/base64"
	"errors"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

var ErrInvalidSubscription = errors.New("invalid web push subscription")

type Channel string

const ChannelWebPush Channel = "web_push"

type WebPushKeys struct {
	P256DH string `json:"p256dh"`
	Auth   string `json:"auth"`
}

type WebPushSubscription struct {
	Endpoint       string      `json:"endpoint"`
	Keys           WebPushKeys `json:"keys"`
	ExpirationTime *int64      `json:"expirationTime,omitempty"`
}

// Validate checks subscription shape. The sender must separately restrict outbound destinations.
func (s WebPushSubscription) Validate() error {
	if !validEndpoint(s.Endpoint) {
		return ErrInvalidSubscription
	}
	publicKey, err := decodeSubscriptionKey(s.Keys.P256DH)
	if err != nil {
		return ErrInvalidSubscription
	}
	if _, err := ecdh.P256().NewPublicKey(publicKey); err != nil {
		return ErrInvalidSubscription
	}
	auth, err := decodeSubscriptionKey(s.Keys.Auth)
	if err != nil || len(auth) != 16 {
		return ErrInvalidSubscription
	}
	return nil
}

func validEndpoint(endpoint string) bool {
	if len(endpoint) > 4096 || strings.TrimSpace(endpoint) != endpoint {
		return false
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil ||
		parsed.Fragment != "" || strings.Contains(endpoint, "#") {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" || !strings.Contains(host, ".") || strings.HasSuffix(host, ".") {
		return false
	}
	if port := parsed.Port(); port != "" && port != "443" {
		return false
	}
	if _, err := netip.ParseAddr(host); err == nil {
		return false
	}
	for _, suffix := range []string{".localhost", ".local", ".internal"} {
		if strings.HasSuffix(host, suffix) {
			return false
		}
	}
	return true
}

func decodeSubscriptionKey(value string) ([]byte, error) {
	if len(value) > 128 {
		return nil, ErrInvalidSubscription
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err == nil {
		return decoded, nil
	}
	return base64.URLEncoding.DecodeString(value)
}

type Receiver struct {
	ID           string
	UserID       string
	Revision     int64
	Channel      Channel
	Enabled      bool
	ExpiresAt    *time.Time
	Subscription WebPushSubscription
}

func (r Receiver) canReceive(now time.Time) bool {
	return r.ID != "" && r.UserID != "" && r.Revision > 0 && r.Enabled &&
		r.Channel == ChannelWebPush && (r.ExpiresAt == nil || now.Before(*r.ExpiresAt)) &&
		r.Subscription.Validate() == nil
}
