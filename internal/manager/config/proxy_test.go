package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/config"
)

func TestCloudflareProxyConfiguration(t *testing.T) {
	for _, value := range []string{"garbage", "0.0.0.0/0", "::/0", "192.0.2.1/32,", "::ffff:192.0.2.1/128"} {
		t.Run(value, func(t *testing.T) {
			cfg := config.Config{ObjectStorageBucket: "test", TrustedCloudflareProxyCIDRs: value}
			require.Error(t, cfg.Validate())
		})
	}
	t.Setenv("TRUSTED_CLOUDFLARE_PROXY_CIDRS", "192.0.2.1/32, 2001:db8::1/128")
	cfg := config.Load()
	prefixes, err := cfg.CloudflareProxyPrefixes()
	require.NoError(t, err)
	require.Len(t, prefixes, 2)
	t.Setenv("TRUSTED_CLOUDFLARE_PROXY_CIDRS", "")
	prefixes, err = config.Load().CloudflareProxyPrefixes()
	require.NoError(t, err)
	require.Empty(t, prefixes)
}
