package manager

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/network"
	"github.com/stretchr/testify/require"
)

type proxyTestConnection struct {
	network.Conn
	peer net.Addr
}

func (c proxyTestConnection) RemoteAddr() net.Addr { return c.peer }

func proxyTestContext(peer, cf, xff string) *app.RequestContext {
	ctx := app.NewContext(0)
	addr, err := net.ResolveTCPAddr("tcp", peer)
	if err != nil {
		panic(err)
	}
	ctx.SetConn(proxyTestConnection{peer: addr})
	ctx.Request.Header.Set("CF-Connecting-IP", cf)
	ctx.Request.Header.Set("X-Forwarded-For", xff)
	ctx.Request.Header.Set("CF-IPCity", "Spoofed city")
	ctx.Request.Header.Set("CF-IPCountry", "ZZ")
	return ctx
}

func TestClientAddressProxyBoundary(t *testing.T) {
	for _, tc := range []struct{ name, proxies, peer, cf, xff, want string }{
		{"default denies headers", "", "192.0.2.10:1234", "203.0.113.1", "203.0.113.2", "192.0.2.10"},
		{"untrusted peer", "192.0.2.11/32", "192.0.2.10:1234", "203.0.113.1", "203.0.113.2", "192.0.2.10"},
		{"trusted connector", "192.0.2.10/32", "192.0.2.10:1234", "203.0.113.1", "203.0.113.2", "203.0.113.1"},
		{"no XFF fallback", "192.0.2.10/32", "192.0.2.10:1234", "", "203.0.113.2", "192.0.2.10"},
		{"invalid header", "192.0.2.10/32", "192.0.2.10:1234", "not-an-ip", "203.0.113.2", "192.0.2.10"},
		{"address list rejected", "192.0.2.10/32", "192.0.2.10:1234", "203.0.113.1, 203.0.113.2", "", "192.0.2.10"},
		{"port rejected", "192.0.2.10/32", "192.0.2.10:1234", "203.0.113.1:80", "", "192.0.2.10"},
		{"IPv6 connector", "2001:db8::1/128", "[2001:db8::1]:1234", "2001:db8::2", "", "2001:db8::2"},
		{"mapped IPv4 normalized", "192.0.2.10/32", "[::ffff:192.0.2.10]:1234", "::ffff:203.0.113.1", "", "203.0.113.1"},
		{"zone rejected", "192.0.2.10/32", "192.0.2.10:1234", "fe80::1%eth0", "", "192.0.2.10"},
		{"unspecified rejected", "192.0.2.10/32", "192.0.2.10:1234", "0.0.0.0", "", "192.0.2.10"},
		{"invalid config fails closed", "192.0.2.10/32,broken", "192.0.2.10:1234", "203.0.113.1", "", "192.0.2.10"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Service{cfg: Config{TrustedCloudflareProxyCIDRs: tc.proxies}}
			require.Equal(t, tc.want, s.clientAddress(proxyTestContext(tc.peer, tc.cf, tc.xff)))
		})
	}
}

func TestForgedHeadersDoNotEvadeRateLimit(t *testing.T) {
	for _, route := range []string{"/api/proxy-test", "/api/v1/node/registration/start"} {
		t.Run(route, func(t *testing.T) {
			clock := func() time.Time { return time.Unix(0, 0) }
			s := &Service{
				apiLimiter:      newRateLimiter(1, 1, clock),
				registerLimiter: newRateLimiter(1, 1, clock),
			}
			for i, ip := range []string{"203.0.113.1", "203.0.113.2"} {
				ctx := proxyTestContext("192.0.2.10:1234", ip, ip)
				ctx.Request.SetRequestURI(route)
				ctx.Request.Header.SetMethod("POST")
				ctx.SetFullPath(route)
				s.protect()(context.Background(), ctx)
				if i == 0 {
					require.NotEqual(t, 429, ctx.Response.StatusCode())
				} else {
					require.Equal(t, 429, ctx.Response.StatusCode())
				}
			}
		})
	}
}

func TestTrustedClientsHaveSeparateRateBuckets(t *testing.T) {
	s := &Service{cfg: Config{TrustedCloudflareProxyCIDRs: "192.0.2.10/32"},
		apiLimiter: newRateLimiter(1, 1, func() time.Time { return time.Unix(0, 0) })}
	for _, ip := range []string{"203.0.113.1", "203.0.113.2"} {
		ctx := proxyTestContext("192.0.2.10:1234", ip, "")
		ctx.Request.SetRequestURI("/api/proxy-test")
		s.protect()(context.Background(), ctx)
		require.NotEqual(t, 429, ctx.Response.StatusCode())
	}
}

func TestRegistrationNetworkProxyBoundary(t *testing.T) {
	ctx := proxyTestContext("192.0.2.10:1234", "203.0.113.1", "")
	s := &Service{}
	preview := s.nodeRegistrationNetwork(ctx)
	require.Equal(t, "192.0.2.10", preview.IPAddress)
	require.Empty(t, preview.City)
	require.Empty(t, preview.Country)
	s.cfg.TrustedCloudflareProxyCIDRs = "192.0.2.10/32"
	preview = s.nodeRegistrationNetwork(ctx)
	require.Equal(t, "203.0.113.1", preview.IPAddress)
	require.Equal(t, "Spoofed city", preview.City)
	require.Equal(t, "ZZ", preview.Country)
}
