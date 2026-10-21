package notifications

import (
	"context"
	"crypto/ecdh"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

var ErrPushConfiguration = errors.New("invalid Web Push configuration")

const DefaultPushHosts = "fcm.googleapis.com,.push.services.mozilla.com,web.push.apple.com,.notify.windows.com"

type WebPushConfig struct{ PublicKey, PrivateKey, Subscriber, AllowedHosts string }
type WebPushSender struct {
	config   WebPushConfig
	client   webpush.HTTPClient
	now      func() time.Time
	lookupIP func(context.Context, string, string) ([]net.IP, error)
	dial     func(context.Context, string, string) (net.Conn, error)
}

func (c WebPushConfig) Validate() error {
	private, err := base64.RawURLEncoding.DecodeString(c.PrivateKey)
	if err != nil {
		return ErrPushConfiguration
	}
	key, err := ecdh.P256().NewPrivateKey(private)
	if err != nil || base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes()) != c.PublicKey {
		return ErrPushConfiguration
	}
	subject, err := url.Parse(c.Subscriber)
	if err != nil || (subject.Scheme != "mailto" && subject.Scheme != "https") ||
		(subject.Scheme == "https" && subject.Hostname() == "") || (subject.Scheme == "mailto" && !strings.Contains(subject.Opaque, "@")) {
		return ErrPushConfiguration
	}
	return nil
}

func NewWebPushSender(config WebPushConfig) (*WebPushSender, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if config.AllowedHosts == "" {
		config.AllowedHosts = DefaultPushHosts
	}
	sender := &WebPushSender{
		config:   config,
		now:      time.Now,
		lookupIP: net.DefaultResolver.LookupIP,
		dial:     (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
	}
	transport := &http.Transport{
		Proxy:                  nil,
		DialContext:            sender.dialContext,
		TLSHandshakeTimeout:    5 * time.Second,
		ResponseHeaderTimeout:  8 * time.Second,
		MaxResponseHeaderBytes: 16 * 1024,
		IdleConnTimeout:        time.Minute,
	}
	sender.client = &http.Client{
		Transport:     transport,
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return sender, nil
}

func (s *WebPushSender) allowedHost(host string) bool {
	host = strings.ToLower(host)
	for _, allowed := range strings.Split(s.config.AllowedHosts, ",") {
		allowed = strings.ToLower(strings.TrimSpace(allowed))
		if allowed != "" &&
			(host == allowed || (strings.HasPrefix(allowed, ".") && strings.HasSuffix(host, allowed) && host != strings.TrimPrefix(allowed, "."))) {
			return true
		}
	}
	return false
}

func publicPushIP(ip net.IP) bool {
	addr, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	addr = addr.Unmap()
	if !addr.IsGlobalUnicast() || addr.IsPrivate() || addr.IsLoopback() ||
		addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() ||
		addr.IsUnspecified() {
		return false
	}
	for _, cidr := range []string{"100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "64:ff9b::/96"} {
		if netip.MustParsePrefix(cidr).Contains(addr) {
			return false
		}
	}
	return true
}

func (s *WebPushSender) dialContext(
	ctx context.Context,
	network, address string,
) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port != "443" || !s.allowedHost(host) {
		return nil, errors.New("push destination not allowed")
	}
	addresses, err := s.lookupIP(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return nil, errors.New("push destination unavailable")
	}
	if slices.ContainsFunc(addresses, func(ip net.IP) bool { return !publicPushIP(ip) }) {
		return nil, errors.New("push destination not allowed")
	}
	for _, ip := range addresses {
		conn, err := s.dial(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
	}
	return nil, errors.New("push destination unavailable")
}

func (s *WebPushSender) Send(
	ctx context.Context,
	receiver RegistrationRecord,
	payload Payload,
	expiresAt time.Time,
) AttemptResult {
	endpoint, err := url.Parse(receiver.Subscription.Endpoint)
	if err != nil || receiver.Subscription.Validate() != nil ||
		!s.allowedHost(endpoint.Hostname()) {
		return AttemptResult{Status: 400, ErrorCategory: "destination_not_allowed"}
	}
	ttl := int(expiresAt.Sub(s.now()).Seconds())
	if ttl <= 0 {
		return AttemptResult{Suppressed: true, ErrorCategory: "expired"}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return AttemptResult{Status: 400, ErrorCategory: "invalid_payload"}
	}
	response, err := webpush.SendNotificationWithContext(ctx, body, &webpush.Subscription{
		Endpoint: receiver.Subscription.Endpoint, Keys: webpush.Keys{Auth: receiver.Subscription.Keys.Auth, P256dh: receiver.Subscription.Keys.P256DH},
	}, &webpush.Options{HTTPClient: s.client, Subscriber: strings.TrimPrefix(s.config.Subscriber, "mailto:"), VAPIDPublicKey: s.config.PublicKey, VAPIDPrivateKey: s.config.PrivateKey, TTL: ttl, Urgency: webpush.UrgencyNormal})
	if err != nil {
		return AttemptResult{ErrorCategory: "transport"}
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
	result := AttemptResult{Status: response.StatusCode}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		result.ErrorCategory = "provider_rejected"
	}
	retry := response.Header.Get("Retry-After")
	if seconds, err := strconv.ParseInt(retry, 10, 32); err == nil && seconds > 0 {
		result.RetryAfter = time.Duration(seconds) * time.Second
	} else if at, err := http.ParseTime(retry); err == nil && at.After(s.now()) {
		result.RetryAfter = at.Sub(s.now())
	}
	return result
}
