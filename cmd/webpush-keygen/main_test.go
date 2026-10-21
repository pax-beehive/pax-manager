package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/notifications"
)

func TestKeygenGivenOutputPathThenWritesPrivateFileWithoutPrintingOrOverwritingKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "keys.json")
	var output bytes.Buffer
	args := []string{
		"-out",
		path,
		"-subscriber",
		"mailto:push@example.invalid",
		"-key-id",
		"test-key",
	}
	require.NoError(t, run(args, &output))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var values map[string]string
	require.NoError(t, json.Unmarshal(data, &values))
	assert.NoError(
		t,
		(notifications.WebPushConfig{PublicKey: values["public_key"], PrivateKey: values["private_key"], Subscriber: values["subscriber"]}).Validate(),
	)
	assert.NotContains(t, output.String(), values["private_key"])
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
	assert.Error(t, run(args, &output))
	for _, invalid := range [][]string{nil, {"-unknown"}, {"-out", path + "-bad", "-subscriber", "invalid"}, {"-out", path + "-empty", "-key-id", ""}} {
		assert.Error(t, run(invalid, &output))
	}
	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = append(
		[]string{"webpush-keygen"},
		"-out",
		filepath.Join(t.TempDir(), "main-keys.json"),
		"-subscriber",
		"https://example.invalid/contact",
	)
	main()
}
