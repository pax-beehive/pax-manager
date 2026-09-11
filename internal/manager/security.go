package manager

import (
	"context"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/nodepolicy"
)

func (s *Service) protect() app.HandlerFunc {
	return func(c context.Context, ctx *app.RequestContext) {
		path := string(ctx.Path())
		if nodepolicy.Managed(path) &&
			!nodepolicy.Allowed(string(ctx.Method()), ctx.FullPath()) {
			writeError(ctx, http.StatusNotFound, "not found")
			return
		}

		if s.maxBodyBytes > 0 {
			contentLength := ctx.Request.Header.ContentLength()
			if contentLength > int(s.maxBodyBytes) ||
				len(ctx.Request.Body()) > int(s.maxBodyBytes) {
				writeError(ctx, http.StatusRequestEntityTooLarge, "request body too large")
				return
			}
		}

		if strings.HasPrefix(path, "/api/") {
			limiter := s.apiLimiter
			if path == "/api/agent/register" ||
				strings.HasPrefix(path, "/api/v1/node/registration/") {
				limiter = s.registerLimiter
			}
			if limiter != nil && !limiter.allow(s.clientAddress(ctx)) {
				ctx.Header("Retry-After", "60")
				writeError(ctx, http.StatusTooManyRequests, "rate limit exceeded")
				return
			}
		}

		ctx.Next(c)
	}
}

// Forwarded headers are not connection evidence. Only explicitly trusted
// Cloudflare connectors may supply a single, valid CF-Connecting-IP value.
func (s *Service) clientAddress(ctx *app.RequestContext) string {
	peer, err := netip.ParseAddrPort(ctx.RemoteAddr().String())
	if err != nil {
		return "unknown"
	}
	address := peer.Addr().Unmap()
	if address.IsUnspecified() {
		return "unknown"
	}
	if s.trustedCloudflarePeer(address) {
		raw := strings.TrimSpace(string(ctx.GetHeader("CF-Connecting-IP")))
		forwarded, err := netip.ParseAddr(raw)
		forwarded = forwarded.Unmap()
		if err == nil && forwarded.Zone() == "" &&
			!forwarded.IsUnspecified() && !forwarded.IsMulticast() {
			return forwarded.Unmap().String()
		}
	}
	return address.String()
}

func (s *Service) trustedCloudflarePeer(peer netip.Addr) bool {
	prefixes, err := s.cfg.CloudflareProxyPrefixes()
	if err != nil {
		return false
	}
	for _, prefix := range prefixes {
		if prefix.Contains(peer.Unmap()) {
			return true
		}
	}
	return false
}

func (s *Service) trustedCloudflareRequest(ctx *app.RequestContext) bool {
	peer, err := netip.ParseAddrPort(ctx.RemoteAddr().String())
	return err == nil && s.trustedCloudflarePeer(peer.Addr())
}

type rateLimiter struct {
	mu       sync.Mutex
	now      func() time.Time
	rate     float64
	burst    float64
	visitors map[string]*rateBucket
}

type rateBucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter(perMinute int, burst int, now func() time.Time) *rateLimiter {
	if perMinute <= 0 || burst <= 0 {
		return nil
	}
	if now == nil {
		now = time.Now
	}
	return &rateLimiter{
		now:      now,
		rate:     float64(perMinute) / 60.0,
		burst:    float64(burst),
		visitors: make(map[string]*rateBucket),
	}
}

func (l *rateLimiter) allow(key string) bool {
	if l == nil {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	bucket := l.visitors[key]
	if bucket == nil {
		l.visitors[key] = &rateBucket{tokens: l.burst - 1, last: now}
		l.cleanup(now)
		return true
	}

	elapsed := now.Sub(bucket.last).Seconds()
	if elapsed > 0 {
		bucket.tokens = minFloat(l.burst, bucket.tokens+elapsed*l.rate)
		bucket.last = now
	}
	if bucket.tokens < 1 {
		return false
	}
	bucket.tokens--
	return true
}

func (l *rateLimiter) cleanup(now time.Time) {
	if len(l.visitors) < 10000 {
		return
	}
	for key, bucket := range l.visitors {
		if now.Sub(bucket.last) > 10*time.Minute {
			delete(l.visitors, key)
		}
	}
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
