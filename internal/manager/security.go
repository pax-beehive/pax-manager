package manager

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
)

func (s *Service) protect() app.HandlerFunc {
	return func(c context.Context, ctx *app.RequestContext) {
		if s.maxBodyBytes > 0 {
			contentLength := ctx.Request.Header.ContentLength()
			if contentLength > int(s.maxBodyBytes) ||
				len(ctx.Request.Body()) > int(s.maxBodyBytes) {
				writeError(ctx, http.StatusRequestEntityTooLarge, "request body too large")
				return
			}
		}

		path := string(ctx.Path())
		if strings.HasPrefix(path, "/api/") {
			limiter := s.apiLimiter
			if path == "/api/agent/register" {
				limiter = s.registerLimiter
			}
			if limiter != nil && !limiter.allow(clientAddress(ctx)) {
				ctx.Header("Retry-After", "60")
				writeError(ctx, http.StatusTooManyRequests, "rate limit exceeded")
				return
			}
		}

		ctx.Next(c)
	}
}

func clientAddress(ctx *app.RequestContext) string {
	if v := string(ctx.GetHeader("CF-Connecting-IP")); v != "" {
		return strings.TrimSpace(v)
	}
	if v := string(ctx.GetHeader("X-Forwarded-For")); v != "" {
		parts := strings.Split(v, ",")
		if len(parts) > 0 {
			return strings.TrimSpace(parts[0])
		}
	}
	remote := ""
	if ctx.RemoteAddr() != nil {
		remote = ctx.RemoteAddr().String()
	}
	host, _, err := net.SplitHostPort(remote)
	if err == nil && host != "" {
		return host
	}
	if remote != "" {
		return remote
	}
	return "unknown"
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
