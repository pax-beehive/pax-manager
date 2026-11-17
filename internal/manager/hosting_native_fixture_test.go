package manager

import (
	"encoding/json"
	"encoding/pem"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/hosting"
)

// TestGatewayNativeFixture is an opt-in loopback authority for the gateway's
// independent process smoke test. It never contacts a provider or creates disks.
func TestGatewayNativeFixture(t *testing.T) {
	directory := os.Getenv("PAX_GATEWAY_NATIVE_FIXTURE_DIR")
	if directory == "" {
		t.Skip("set an isolated fixture directory for the cross-repo native smoke")
	}
	require.True(t, filepath.IsAbs(directory))
	registry, err := hosting.LoadModelRegistry(filepath.Join(directory, "models.json"))
	require.NoError(t, err)
	s := hostingTestServer(t, "https://gateway.invalid/v1")
	s.hosting.Config.Models, s.hosting.Config.Model = registry.Models, registry.DefaultModel
	for _, policy := range registry.Models {
		if policy.ID == registry.DefaultModel {
			s.hosting.Config.MaxOutputTokens = policy.MaxOutputTokens
		}
	}
	const email = "native-fixture@example.test"
	const adminEmail = "native-operator@example.test"
	s.cfg.AdminEmails[adminEmail] = true
	owner, err := s.store.EnsureUser(t.Context(), email, "Native fixture", "user")
	require.NoError(t, err)
	_, err = s.hosting.Wake(t.Context(), owner.UserID)
	require.NoError(t, err)
	job, err := s.hosting.Claim(t.Context(), &hosting.Claim{HostID: "native-fixture", Capacity: 1})
	require.NoError(t, err)
	require.NotNil(t, job)
	server := httptest.NewTLSServer(s.routes())
	defer server.Close()
	certificate := pem.EncodeToMemory(
		&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw},
	)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "manager-ca.pem"), certificate, 0600))
	ready, err := json.Marshal(map[string]string{
		"manager_url": server.URL, "manager_key": s.cfg.HostingGatewayKey,
		"token": job.ModelToken, "owner_email": email,
		"admin_email": adminEmail,
	})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "ready.json"), ready, 0600))
	deadline := time.NewTimer(2 * time.Minute)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("native fixture deadline exceeded")
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(directory, "stop")); err != nil {
				continue
			}
			var report []map[string]any
			require.NoError(
				t,
				s.hosting.Store.WithHostingState(t.Context(), func(state *hosting.State) error {
					for _, reservation := range state.Requests {
						entry := map[string]any{"status": reservation.Status,
							"generation_started": reservation.GenerationStarted,
							"spent_tokens":       reservation.ActualTokens, "spent_microusd": reservation.ActualMicrousd}
						if reservation.Moderation != nil {
							entry["moderation"] = reservation.Moderation.Verdict
						}
						report = append(report, entry)
					}
					return nil
				}),
			)
			raw, err := json.Marshal(report)
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(filepath.Join(directory, "report.json"), raw, 0600))
			return
		}
	}
}
