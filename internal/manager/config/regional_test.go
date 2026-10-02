package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRegionalConfigGivenPartialConfigurationThenRejectStartup(t *testing.T) {
	for _, cfg := range []Config{{}, {Region: "us", RegionProvisioningSecret: strings.Repeat("x", 32)}, {Region: "hk", RegionProvisioningSecret: strings.Repeat("x", 32)}} {
		require.NoError(t, cfg.validateRegion())
	}
	for _, cfg := range []Config{{Region: "hk"}, {RegionProvisioningSecret: strings.Repeat("x", 32)}, {Region: "eu", RegionProvisioningSecret: strings.Repeat("x", 32)}} {
		assert.Error(t, cfg.Validate())
	}
	t.Setenv("PAX_REGION", "hk")
	t.Setenv("REGION_PROVISIONING_SECRET", strings.Repeat("x", 32))
	cfg := Load()
	assert.Equal(t, "hk", cfg.Region)
	assert.Len(t, cfg.RegionProvisioningSecret, 32)
}
