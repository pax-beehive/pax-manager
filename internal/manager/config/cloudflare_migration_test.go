package config_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/config"
)

func TestCloudflareAccessMigrationConfig(t *testing.T) {
	t.Run(
		"Given no migration environment when loading config then the gate is disabled",
		func(t *testing.T) {
			clearConfigEnv(t)

			cfg := config.Load()

			require.False(t, cfg.CloudflareAccessMigrationEnabled)
			require.Empty(t, cfg.CloudflareAccessMigrationIssuer)
			require.Empty(t, cfg.CloudflareAccessMigrationAud)
			require.Empty(t, cfg.CloudflareAccessMigrationJWKS)
		},
	)

	t.Run(
		"Given migration environment when loading config then it loads the secondary account",
		func(t *testing.T) {
			clearConfigEnv(t)
			t.Setenv("CLOUDFLARE_ACCESS_MIGRATION_ENABLED", "true")
			t.Setenv("CLOUDFLARE_ACCESS_MIGRATION_ISSUER", "https://new.cloudflareaccess.com")
			t.Setenv("CLOUDFLARE_ACCESS_MIGRATION_AUD", "new-aud")
			t.Setenv(
				"CLOUDFLARE_ACCESS_MIGRATION_JWKS_URL",
				"https://new.cloudflareaccess.com/certs",
			)

			cfg := config.Load()

			require.True(t, cfg.CloudflareAccessMigrationEnabled)
			require.Equal(
				t,
				"https://new.cloudflareaccess.com",
				cfg.CloudflareAccessMigrationIssuer,
			)
			require.Equal(t, "new-aud", cfg.CloudflareAccessMigrationAud)
			require.Equal(
				t,
				"https://new.cloudflareaccess.com/certs",
				cfg.CloudflareAccessMigrationJWKS,
			)
		},
	)

	t.Run(
		"Given migration access and local auth bypass when validating then it is rejected",
		func(t *testing.T) {
			err := (config.Config{
				ObjectStorageBucket:              "pax-artifacts",
				CloudflareAccessDisabled:         true,
				CloudflareAccessMigrationEnabled: true,
			}).Validate()

			require.ErrorContains(t, err, "CLOUDFLARE_ACCESS_DISABLED")
		},
	)

	t.Run(
		"Given migration access without primary config when validating then it is rejected",
		func(t *testing.T) {
			err := (config.Config{
				ObjectStorageBucket:              "pax-artifacts",
				CloudflareAccessMigrationEnabled: true,
			}).Validate()

			require.ErrorContains(t, err, "primary Cloudflare Access")
		},
	)

	t.Run(
		"Given migration access without secondary config when validating then it is rejected",
		func(t *testing.T) {
			err := (config.Config{
				ObjectStorageBucket:              "pax-artifacts",
				CloudflareAccessIssuer:           "https://old.cloudflareaccess.com",
				CloudflareAccessAud:              "old-aud",
				CloudflareAccessJWKS:             "https://old.cloudflareaccess.com/certs",
				CloudflareAccessMigrationEnabled: true,
			}).Validate()

			require.ErrorContains(t, err, "migration Cloudflare Access")
		},
	)

	t.Run(
		"Given complete migration access config when validating then it is accepted",
		func(t *testing.T) {
			err := (config.Config{
				ObjectStorageBucket:              "pax-artifacts",
				CloudflareAccessIssuer:           "https://old.cloudflareaccess.com",
				CloudflareAccessAud:              "old-aud",
				CloudflareAccessJWKS:             "https://old.cloudflareaccess.com/certs",
				CloudflareAccessMigrationEnabled: true,
				CloudflareAccessMigrationIssuer:  "https://new.cloudflareaccess.com",
				CloudflareAccessMigrationAud:     "new-aud",
				CloudflareAccessMigrationJWKS:    "https://new.cloudflareaccess.com/certs",
			}).Validate()

			require.NoError(t, err)
		},
	)
}
