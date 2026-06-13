package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/config"
)

func TestLoad(t *testing.T) {
	t.Run(
		"Given no environment when loading config then it keeps Cloudflare validation enabled and built-in admins",
		func(t *testing.T) {
			clearConfigEnv(t)

			cfg := config.Load()

			require.Equal(t, "9879", cfg.Port)
			require.Equal(t, "local@example.local", cfg.LocalUserID)
			require.False(t, cfg.AllowLocalUserHeader)
			require.False(t, cfg.CloudflareAccessDisabled)
			require.Equal(t, int64(1<<20), cfg.MaxBodyBytes)
			require.True(t, cfg.AdminEmails["toddzheng024@gmail.com"])
			require.True(t, cfg.AdminEmails["gengcongkai456789@gmail.com"])
			require.True(t, cfg.AdminEmails["zhangjiahang0725@gmail.com"])
		},
	)

	t.Run(
		"Given environment overrides when loading config then it parses bools numbers and extends admins",
		func(t *testing.T) {
			clearConfigEnv(t)
			t.Setenv("PORT", "9999")
			t.Setenv("ALLOW_LOCAL_USER_HEADER", "true")
			t.Setenv("CLOUDFLARE_ACCESS_DISABLED", "true")
			t.Setenv("CLOUDFLARE_ACCESS_ISSUER", "https://team.cloudflareaccess.com")
			t.Setenv("CLOUDFLARE_ACCESS_AUD", "audience")
			t.Setenv(
				"CLOUDFLARE_ACCESS_JWKS_URL",
				"https://team.cloudflareaccess.com/cdn-cgi/access/certs",
			)
			t.Setenv("ADMIN_EMAILS", " Extra@Example.COM ")
			t.Setenv("MAX_BODY_BYTES", "2048")
			t.Setenv("API_RATE_LIMIT_PER_MINUTE", "10")
			t.Setenv("API_RATE_LIMIT_BURST", "2")
			t.Setenv("REGISTER_RATE_LIMIT_PER_MINUTE", "3")
			t.Setenv("REGISTER_RATE_LIMIT_BURST", "1")

			cfg := config.Load()

			require.Equal(t, "9999", cfg.Port)
			require.True(t, cfg.AllowLocalUserHeader)
			require.True(t, cfg.CloudflareAccessDisabled)
			require.Equal(t, "https://team.cloudflareaccess.com", cfg.CloudflareAccessIssuer)
			require.Equal(t, "audience", cfg.CloudflareAccessAud)
			require.Equal(
				t,
				"https://team.cloudflareaccess.com/cdn-cgi/access/certs",
				cfg.CloudflareAccessJWKS,
			)
			require.True(t, cfg.AdminEmails["extra@example.com"])
			require.True(t, cfg.AdminEmails["toddzheng024@gmail.com"])
			require.Equal(t, int64(2048), cfg.MaxBodyBytes)
			require.Equal(t, 10, cfg.APIRateLimitPerMinute)
			require.Equal(t, 2, cfg.APIRateLimitBurst)
			require.Equal(t, 3, cfg.RegisterLimitPerMinute)
			require.Equal(t, 1, cfg.RegisterLimitBurst)
		},
	)
}

func TestParseEmailSet(t *testing.T) {
	t.Run(
		"Given a comma-separated admin list when parsing then it normalizes and keeps built-in admins",
		func(t *testing.T) {
			admins := config.ParseEmailSet(" One@Example.COM, two@example.com ")

			require.True(t, admins["one@example.com"])
			require.True(t, admins["two@example.com"])
			require.True(t, admins["toddzheng024@gmail.com"])
		},
	)
}

func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"PORT",
		"DATABASE_URL",
		"REGISTRATION_TOKEN",
		"REGISTRATION_TOKEN_OWNER_EMAIL",
		"LOCAL_USER_ID",
		"ALLOW_LOCAL_USER_HEADER",
		"CLOUDFLARE_ACCESS_DISABLED",
		"CLOUDFLARE_ACCESS_ISSUER",
		"CLOUDFLARE_ACCESS_AUD",
		"CLOUDFLARE_ACCESS_JWKS_URL",
		"ADMIN_EMAILS",
		"MAX_BODY_BYTES",
		"API_RATE_LIMIT_PER_MINUTE",
		"API_RATE_LIMIT_BURST",
		"REGISTER_RATE_LIMIT_PER_MINUTE",
		"REGISTER_RATE_LIMIT_BURST",
	} {
		t.Setenv(key, "")
	}
}
