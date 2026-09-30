package manager

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/auth"
)

func TestCloudflareVerifierMigrationGate(t *testing.T) {
	base := Config{
		CloudflareAccessIssuer: "https://old.cloudflareaccess.com",
		CloudflareAccessAud:    "old-aud",
		CloudflareAccessJWKS:   "https://old.cloudflareaccess.com/certs",
	}

	t.Run(
		"Given the migration gate is disabled when building the verifier then only the primary is used",
		func(t *testing.T) {
			verifier := cloudflareVerifier(base)

			require.IsType(t, &auth.CloudflareAccessVerifier{}, verifier)
		},
	)

	t.Run(
		"Given the migration gate is enabled when building the verifier then both accounts are used",
		func(t *testing.T) {
			cfg := base
			cfg.CloudflareAccessMigrationEnabled = true
			cfg.CloudflareAccessMigrationIssuer = "https://new.cloudflareaccess.com"
			cfg.CloudflareAccessMigrationAud = "new-aud"
			cfg.CloudflareAccessMigrationJWKS = "https://new.cloudflareaccess.com/certs"

			verifier := cloudflareVerifier(cfg)

			require.IsType(t, &auth.UserIdentityVerifierChain{}, verifier)
		},
	)

	t.Run(
		"Given Cloudflare Access is disabled when building the verifier then no verifier is used",
		func(t *testing.T) {
			cfg := base
			cfg.CloudflareAccessDisabled = true
			cfg.CloudflareAccessMigrationEnabled = true

			verifier := cloudflareVerifier(cfg)

			require.Nil(t, verifier)
		},
	)
}
