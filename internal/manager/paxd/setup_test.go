package paxd

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSetupRejectsUnconfiguredOrUnsafeOrigins(t *testing.T) {
	for _, origin := range []string{"", "https://user:secret@example.test", "https://example.test/path", "https://example.test?secret=x", "https://example.test#x", "http://example.test", "file:///tmp/install.sh"} {
		_, err := (SetupConfig{DownloadURL: origin, MachineURL: "https://node.example.test"}).Info()
		require.Error(t, err, origin)
		_, err = (SetupConfig{DownloadURL: "https://download.example.test", MachineURL: origin}).Script()
		require.Error(t, err, origin)
	}
}

func TestSetupScriptExecutesCompleteInstallerAndStopsOnFailures(t *testing.T) {
	for _, scenario := range []string{"success", "browser", "html", "empty", "partial", "syntax", "http-error", "login", "install-error"} {
		t.Run(scenario, func(t *testing.T) {
			var cfg SetupConfig
			calls := 0
			server := httptest.NewServer(
				http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Query().Get("setup") == "1" {
						script, err := cfg.Script()
						require.NoError(t, err)
						w.Header().Set("Content-Type", "text/x-shellscript")
						_, _ = fmt.Fprint(w, script)
						return
					}
					calls++
					switch scenario {
					case "html", "login":
						if scenario == "login" && r.URL.Path != "/login" {
							http.Redirect(w, r, "/login", http.StatusFound)
							return
						}
						_, _ = fmt.Fprint(w, "<!DOCTYPE html>\n<html>Login</html>")
					case "empty":
					case "partial":
						w.Header().Set("Content-Length", "4096")
						_, _ = fmt.Fprint(w, "#!/bin/bash\nprintf 'executed'\n")
					case "syntax":
						_, _ = fmt.Fprint(w, "#!/bin/bash\nprintf 'executed'\nif\n")
					case "http-error":
						w.WriteHeader(503)
					default:
						_, _ = fmt.Fprint(w, `#!/usr/bin/env bash
set -eu
[ "$PAX_DOWNLOAD_URL" = "$EXPECTED_DOWNLOAD" ]
[ "$PAX_CLOUD_URL" = "$EXPECTED_MACHINE" ]
[ "$PAX_REGISTRATION_TOKEN" = "$EXPECTED_TOKEN" ]
[ "$PAX_SETUP_AFTER_INSTALL" = 1 ]
printf 'installer-executed\n'
`)
						if scenario == "install-error" {
							_, _ = fmt.Fprint(w, "exit 7\n")
						}
					}
				}),
			)
			defer server.Close()
			cfg = SetupConfig{
				DownloadURL: server.URL,
				MachineURL:  "https://node-region.example.test",
			}
			info, err := cfg.Info()
			require.NoError(t, err)
			temp := t.TempDir()
			token := "fake'$(exit 81)\n`exit 82`"
			if scenario == "browser" {
				token = ""
			}
			command := exec.Command(
				"bash",
				"-o",
				"pipefail",
				"-c",
				"curl -fsSL --max-redirs 1 '"+info.ScriptURL+"' | bash",
			)
			command.Env = append(
				os.Environ(),
				"TMPDIR="+temp,
				"PAX_DOWNLOAD_URL=https://wrong.example",
				"PAX_CLOUD_URL=https://wrong.example",
				"PAX_REGISTRATION_TOKEN="+token,
				"EXPECTED_TOKEN="+token,
				"EXPECTED_DOWNLOAD="+server.URL,
				"EXPECTED_MACHINE="+cfg.MachineURL,
			)
			output, err := command.CombinedOutput()
			if scenario == "success" || scenario == "browser" {
				require.NoError(t, err, string(output))
				require.Contains(t, string(output), "installer-executed")
			} else {
				require.Error(t, err, string(output))
				if scenario == "install-error" {
					require.Equal(t, 7, command.ProcessState.ExitCode())
				} else {
					require.NotContains(t, string(output), "executed")
				}
			}
			require.NotContains(t, string(output), token+"secret")
			if token != "" {
				require.NotContains(t, string(output), token)
			}
			leftovers, err := filepath.Glob(filepath.Join(temp, "*"))
			require.NoError(t, err)
			require.Empty(t, leftovers)
			require.GreaterOrEqual(t, calls, 1)
		})
	}
}

func TestSetupHasNoRegionTableOrEmbeddedCredentials(t *testing.T) {
	cfg := SetupConfig{
		DownloadURL: "https://artifact.new-region.test",
		MachineURL:  "https://machine.new-region.test",
	}
	script, err := cfg.Script()
	require.NoError(t, err)
	require.Contains(t, script, cfg.DownloadURL)
	require.Contains(t, script, cfg.MachineURL)
	require.False(t, strings.Contains(script, "hk-api"))
}
