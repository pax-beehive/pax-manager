package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/config"
)

func TestHostingPolicyConfigurationDefaultsAndOverrides(t *testing.T) {
	for _, key := range []string{"HOSTING_MODELS_FILE", "HOSTING_REQUESTS_PER_MINUTE", "HOSTING_GLOBAL_REQUESTS_PER_MINUTE"} {
		t.Setenv(key, "")
	}
	cfg := config.Load()
	require.Equal(t, 20, cfg.HostingRequestsPerMinute)
	require.Equal(t, 200, cfg.HostingGlobalRequestsPerMinute)
	t.Setenv("HOSTING_MODELS_FILE", "/etc/pax/models.json")
	t.Setenv("HOSTING_REQUESTS_PER_MINUTE", "5")
	t.Setenv("HOSTING_GLOBAL_REQUESTS_PER_MINUTE", "50")
	cfg = config.Load()
	require.Equal(t, "/etc/pax/models.json", cfg.HostingModelsFile)
	require.Equal(t, 5, cfg.HostingRequestsPerMinute)
	require.Equal(t, 50, cfg.HostingGlobalRequestsPerMinute)
}

func TestHostingInvalidModelFileRejectsStartup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	require.NoError(t, os.WriteFile(path, []byte(`{"policy_version":"bad"}`), 0o600))
	err := (config.Config{ObjectStorageBucket: "artifacts", HostingModelsFile: path}).Validate()
	require.ErrorContains(t, err, "HOSTING_MODELS_FILE")
}

func TestHostingMonthlyGrantDefaultsAndExplicitPoolPause(t *testing.T) {
	for _, key := range []string{"HOSTING_GRANT_MICROUSD", "HOSTING_GRANT_TOKENS", "HOSTING_GLOBAL_MICROUSD", "HOSTING_MODELS_FILE"} {
		t.Setenv(key, "")
	}
	cfg := config.Load()
	require.Equal(t, 5_000_000, cfg.HostingGrantMicrousd)
	require.Equal(t, 200_000, cfg.HostingGrantTokens)
	require.Zero(t, cfg.HostingGlobalMicrousd)

	t.Setenv("HOSTING_GLOBAL_MICROUSD", "0")
	cfg = config.Load()
	cfg.ObjectStorageBucket = "artifacts"
	require.Zero(t, cfg.HostingGlobalMicrousd)
	require.NoError(t, cfg.Validate())

	t.Setenv("HOSTING_GRANT_MICROUSD", "7000000")
	t.Setenv("HOSTING_GRANT_TOKENS", "1000000")
	t.Setenv("HOSTING_GLOBAL_MICROUSD", "250000000")
	cfg = config.Load()
	require.Equal(t, 7_000_000, cfg.HostingGrantMicrousd)
	require.Equal(t, 1_000_000, cfg.HostingGrantTokens)
	require.Equal(t, 250_000_000, cfg.HostingGlobalMicrousd)
}

func TestHostingMalformedBudgetsRejectStartupInsteadOfRestoringDefaults(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		value string
	}{
		{"HOSTING_GLOBAL_MICROUSD", "-1"},
		{"HOSTING_GLOBAL_MICROUSD", "PRIVATE_INVALID_VALUE"},
		{"HOSTING_GLOBAL_MICROUSD", "1000000000000001"},
		{"HOSTING_GLOBAL_MICROUSD", "999999999999999999999999"},
		{"HOSTING_GRANT_MICROUSD", "0"},
		{"HOSTING_GRANT_MICROUSD", "-1"},
		{"HOSTING_GRANT_MICROUSD", "PRIVATE_INVALID_VALUE"},
		{"HOSTING_GRANT_MICROUSD", "1000000000000001"},
		{"HOSTING_GRANT_TOKENS", "0"},
		{"HOSTING_GRANT_TOKENS", "PRIVATE_INVALID_VALUE"},
		{"HOSTING_GRANT_TOKENS", "1000000001"},
	} {
		t.Run(scenario.name+"/"+scenario.value, func(t *testing.T) {
			for _, key := range []string{"HOSTING_GRANT_MICROUSD", "HOSTING_GRANT_TOKENS", "HOSTING_GLOBAL_MICROUSD", "HOSTING_MODELS_FILE"} {
				t.Setenv(key, "")
			}
			t.Setenv(scenario.name, scenario.value)
			cfg := config.Load()
			cfg.ObjectStorageBucket = "artifacts"
			err := cfg.Validate()
			require.Error(t, err)
			require.NotContains(t, err.Error(), scenario.value)
		})
	}
}
