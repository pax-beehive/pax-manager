package config

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSandboxNodeConfigRequiresCompleteAndSeparateWorkerCredentials(t *testing.T) {
	require.NoError(t, Config{}.validateSandboxNodes())
	config := Config{
		HostingSandboxHostKey:  strings.Repeat("n", 32),
		HostingSandboxHostID:   "aws-pilot",
		HostingSandboxCloudURL: "https://node.example.com",
		HostingSandboxOwners:   " usr_one, ,usr_two ",
	}
	require.NoError(t, config.validateSandboxNodes())
	require.Equal(t, []string{"usr_one", "usr_two"}, config.SandboxOwners())
	config.HostingHostKey = config.HostingSandboxHostKey
	require.Error(t, config.validateSandboxNodes())
	config.HostingHostKey = ""
	config.HostingSandboxCloudURL = "https://node.example.com/private"
	require.Error(t, config.validateSandboxNodes())
}
