package config_test

import (
	"testing"
	"time"

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
			require.Equal(t, 15*time.Minute, cfg.PaxdArtifactDownloadTTL)
			require.Equal(t, "https://ws.paxtech.net", cfg.PaxdVerificationBaseURL)
			require.Equal(t, "dry_run", cfg.TeamMemexExecutor)
			require.Empty(t, cfg.DeepSeekAPIKey)
			require.Equal(t, "https://api.deepseek.com", cfg.DeepSeekBaseURL)
			require.Equal(t, "deepseek-v4-flash", cfg.DeepSeekModel)
			require.Equal(t, 120*time.Second, cfg.DeepSeekTimeout)
			require.Equal(t, 384000, cfg.DeepSeekMaxTokens)
			require.InDelta(t, 0.2, cfg.DeepSeekTemperature, 0.001)
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
			t.Setenv("PAXD_ARTIFACT_DOWNLOAD_URL_TTL_SECONDS", "60")
			t.Setenv("PAXD_ARTIFACT_UPLOAD_AUDIENCE", "https://manager.example.com")
			t.Setenv(
				"PAXD_ARTIFACT_UPLOAD_PRINCIPALS",
				"release-bot@example.iam.gserviceaccount.com",
			)
			t.Setenv(
				"PAXD_ARTIFACT_SIGNING_SERVICE_ACCOUNT",
				"signer@example.iam.gserviceaccount.com",
			)
			t.Setenv("PAXD_ARTIFACT_GCS_MOCK", "true")
			t.Setenv("PAXD_VERIFICATION_BASE_URL", "https://app.example.com")
			t.Setenv("TEAM_MEMEX_EXECUTOR", "deepseek")
			t.Setenv("DEEPSEEK_API_KEY", "deepseek_test")
			t.Setenv("DEEPSEEK_BASE_URL", "https://deepseek.example.com")
			t.Setenv("DEEPSEEK_MODEL", "deepseek-test")
			t.Setenv("DEEPSEEK_TIMEOUT_SECONDS", "30")
			t.Setenv("DEEPSEEK_MAX_TOKENS", "4096")
			t.Setenv("DEEPSEEK_TEMPERATURE", "0.4")

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
			require.Equal(t, time.Minute, cfg.PaxdArtifactDownloadTTL)
			require.Equal(t, "https://manager.example.com", cfg.PaxdArtifactUploadAudience)
			require.True(
				t,
				cfg.PaxdArtifactUploadPrincipals["release-bot@example.iam.gserviceaccount.com"],
			)
			require.Equal(
				t,
				"signer@example.iam.gserviceaccount.com",
				cfg.PaxdArtifactSigningServiceAccount,
			)
			require.True(t, cfg.PaxdArtifactGCSMock)
			require.Equal(t, "https://app.example.com", cfg.PaxdVerificationBaseURL)
			require.Equal(t, "deepseek", cfg.TeamMemexExecutor)
			require.Equal(t, "deepseek_test", cfg.DeepSeekAPIKey)
			require.Equal(t, "https://deepseek.example.com", cfg.DeepSeekBaseURL)
			require.Equal(t, "deepseek-test", cfg.DeepSeekModel)
			require.Equal(t, 30*time.Second, cfg.DeepSeekTimeout)
			require.Equal(t, 4096, cfg.DeepSeekMaxTokens)
			require.InDelta(t, 0.4, cfg.DeepSeekTemperature, 0.001)
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
		"PAXD_ARTIFACT_DOWNLOAD_URL_TTL_SECONDS",
		"PAXD_ARTIFACT_UPLOAD_AUDIENCE",
		"PAXD_ARTIFACT_UPLOAD_PRINCIPALS",
		"PAXD_ARTIFACT_SIGNING_SERVICE_ACCOUNT",
		"PAXD_ARTIFACT_GCS_MOCK",
		"PAXD_VERIFICATION_BASE_URL",
		"TEAM_MEMEX_EXECUTOR",
		"DEEPSEEK_API_KEY",
		"DEEPSEEK_BASE_URL",
		"DEEPSEEK_MODEL",
		"DEEPSEEK_TIMEOUT_SECONDS",
		"DEEPSEEK_MAX_TOKENS",
		"DEEPSEEK_TEMPERATURE",
	} {
		t.Setenv(key, "")
	}
}
