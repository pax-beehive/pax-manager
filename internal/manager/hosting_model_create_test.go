package manager

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/hosting"
)

func TestHostingAdminCreatesDurableDisabledModelsWithoutRuntimeCredentials(t *testing.T) {
	s, store := newConfigTestServer(t, Config{
		HostingGrantMicrousd: 5000000, HostingGrantTokens: 200000,
	})
	body := map[string]any{
		"revision": 0, "action": "model_create", "target_id": "trial/frontier",
		"provider": "openrouter", "upstream_model": "vendor/frontier-model",
		"moderation": "openai", "max_output_tokens": 4096,
		"input_microusd_per_million": 1000000, "output_microusd_per_million": 5000000,
	}
	for _, email := range []string{"", "member@example.com"} {
		response := callAPI(t, s, "PATCH", hostingAdminPath, email, body)
		if email == "" {
			require.Equal(t, 401, response.Code)
		} else {
			require.Equal(t, 403, response.Code)
		}
	}
	response := callAPI(t, s, "PATCH", hostingAdminPath, "admin@example.com", body)
	require.Equal(t, 200, response.Code, response.Body.String())
	created := decodeData[hosting.AdminSnapshot](t, response.Body.Bytes())
	require.False(t, created.Enabled)
	require.Zero(t, created.Pool.MonthlyLimitMicrousd)
	require.Len(t, created.Models, 1)
	require.False(t, created.Models[0].Enabled)
	require.Equal(t, "admin-1", created.Models[0].PolicyVersion)
	require.Equal(t, "openrouter", created.Models[0].Provider)
	require.Equal(t, "vendor/frontier-model", created.Models[0].UpstreamModel)
	require.Len(t, created.ModelBases["trial/frontier"], 64)
	require.Equal(t, "model_create", created.Audit[0].Action)
	operator, err := s.store.GetUserByEmail(t.Context(), "admin@example.com")
	require.NoError(t, err)
	require.Equal(t, operator.UserID, created.Audit[0].ActorUserID)

	restarted := newServer(s.cfg, store)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, restarted.CloseTransportStore(ctx))
	})
	response = callAPI(t, restarted, "GET", hostingAdminPath, "admin@example.com", nil)
	require.Equal(t, 200, response.Code)
	require.Equal(t, created, decodeData[hosting.AdminSnapshot](t, response.Body.Bytes()))
	body["revision"] = 1
	response = callAPI(t, restarted, "PATCH", hostingAdminPath, "admin@example.com", body)
	require.Equal(t, 409, response.Code)
	require.Contains(t, response.Body.String(), "hosting_model_conflict")
	body["revision"], body["target_id"] = 0, "trial/stale"
	response = callAPI(t, restarted, "PATCH", hostingAdminPath, "admin@example.com", body)
	require.Equal(t, 409, response.Code)
	require.Contains(t, response.Body.String(), "hosting_admin_revision_conflict")
	body["revision"], body["target_id"] = 1, "trial/rejected"
	for _, field := range []string{"api_key", "base_url", "route", "policy_version"} {
		body[field] = "forbidden"
		response = callAPI(t, restarted, "PATCH", hostingAdminPath, "admin@example.com", body)
		require.Equal(t, 400, response.Code, field)
		delete(body, field)
	}
	raw, err := json.Marshal(created)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "forbidden")
}
