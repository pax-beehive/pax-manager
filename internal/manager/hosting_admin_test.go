package manager

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/hosting"
)

const hostingAdminPath = "/api/v1/user/self/hosting-admin"
const hostingAdminEmail = "operator@example.invalid"

func hostingAdminServer(t *testing.T) *Service {
	t.Helper()
	s := hostingTestServer(t, "https://gateway.example.invalid/v1")
	s.cfg.AdminEmails[hostingAdminEmail] = true
	return s
}

func hostingAdminPatch(t *testing.T, s *Service, body any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	require.NoError(t, err)
	return hostingRequest(t, s, "PATCH", hostingAdminPath, string(raw), hostingAdminEmail, "")
}

func TestHostingAdminRequiresServerResolvedAdministrator(t *testing.T) {
	s := hostingAdminServer(t)
	job := gatewayAccountingJob(t, s)
	for _, method := range []string{"GET", "PATCH"} {
		for _, token := range []string{"", job.ModelToken, s.cfg.HostingHostKey, s.cfg.HostingGatewayKey} {
			response := hostingRequest(t, s, method, hostingAdminPath, "{", "", token)
			require.Equal(t, 401, response.Code)
			require.Contains(t, response.Header().Get("Cache-Control"), "no-store")
		}
		response := hostingRequest(t, s, method, hostingAdminPath, "{", "user@example.invalid", "")
		require.Equal(t, 403, response.Code)
		require.NotContains(t, response.Body.String(), "monthly_limit_microusd")
	}
	_, err := s.store.EnsureUser(t.Context(), "fake-admin@example.invalid", "Fake", "admin")
	require.NoError(t, err)
	response := hostingRequest(t, s, "GET", hostingAdminPath, "", "fake-admin@example.invalid", "")
	require.Equal(t, 403, response.Code)
	response = hostingRequest(t, s, "GET", hostingAdminPath, "", hostingAdminEmail, "")
	require.Equal(t, 200, response.Code)
	require.Contains(t, response.Body.String(), "monthly_limit_microusd")
	for _, secret := range []string{s.cfg.HostingHostKey, s.cfg.HostingGatewayKey, s.cfg.HostingTokenSecret, job.ModelToken} {
		require.NotContains(t, response.Body.String(), secret)
	}
}

func TestHostingAdminStrictWritesRevisionAndFixedAuditActor(t *testing.T) {
	s := hostingAdminServer(t)
	for _, body := range []string{
		`{`, `null`, `{} {}`, `{"action":"pool","monthly_microusd":0}`,
		`{"revision":null,"action":"pool","monthly_microusd":0}`,
		`{"revision":0,"action":"pool","monthly_microusd":-1}`,
		`{"revision":0,"action":"pool","monthly_microusd":1.5}`,
		`{"revision":0,"action":"pool","monthly_microusd":9223372036854775808}`,
		`{"revision":0,"action":"pool","monthly_microusd":0,"actor_user_id":"fake"}`,
		`{"revision":0,"action":"pool","monthly_microusd":0,"enabled":false}`,
		`{"revision":0,"action":"model","target_id":"unknown","provider_key":"secret"}`,
	} {
		response := hostingRequest(t, s, "PATCH", hostingAdminPath, body, hostingAdminEmail, "")
		require.Equal(t, 400, response.Code, body)
	}
	response := hostingRequest(t, s, "PATCH", hostingAdminPath,
		strings.Repeat(" ", hostingAdminMaxBody+1), hostingAdminEmail, "")
	require.Equal(t, 413, response.Code)
	response = hostingAdminPatch(t, s, map[string]any{
		"revision": 0, "action": "pool", "monthly_microusd": 0,
	})
	require.Equal(t, 200, response.Code)
	snapshot := decodeData[hosting.AdminSnapshot](t, response.Body.Bytes())
	require.EqualValues(t, 1, snapshot.Revision)
	require.Zero(t, snapshot.Pool.MonthlyLimitMicrousd)
	require.False(t, snapshot.Pool.Available)
	operator, err := s.store.GetUserByEmail(t.Context(), hostingAdminEmail)
	require.NoError(t, err)
	require.Len(t, snapshot.Audit, 1)
	require.Equal(t, operator.UserID, snapshot.Audit[0].ActorUserID)
	response = hostingAdminPatch(t, s, map[string]any{
		"revision": 0, "action": "pool", "monthly_microusd": 1000000,
	})
	require.Equal(t, 409, response.Code)
	require.Contains(t, response.Body.String(), "hosting_admin_revision_conflict")
}

func TestHostingAdminUserLookupPauseAndHeldFunds(t *testing.T) {
	s := hostingAdminServer(t)
	job := gatewayAccountingJob(t, s)
	id := gatewayReserve(t, s, job)
	response := gatewayAccountingRequest(t, s, map[string]any{
		"action": "moderation", "reservation_id": id,
		"moderation": map[string]any{"verdict": "allowed", "model": hosting.ModerationModel},
	})
	require.Equal(t, 200, response.Code)
	response = hostingRequest(t, s, "GET", hostingAdminPath+"?user_query=gateway%40example.com",
		"", hostingAdminEmail, "")
	require.Equal(t, 200, response.Code)
	before := decodeData[hosting.AdminSnapshot](t, response.Body.Bytes())
	require.Len(t, before.Users, 1)
	require.Equal(t, "gateway@example.com", before.Users[0].Email)
	require.Positive(t, before.Pool.ReservedMicrousd)
	response = hostingAdminPatch(t, s, map[string]any{
		"revision": before.Revision, "action": "user", "target_id": job.OwnerUserID,
		"access_paused": true, "monthly_microusd": 500000,
	})
	require.Equal(t, 200, response.Code)
	after := decodeData[hosting.AdminSnapshot](t, response.Body.Bytes())
	require.Equal(t, before.Pool.ReservedMicrousd, after.Pool.ReservedMicrousd)
	require.Equal(t, before.Pool.SpentMicrousd, after.Pool.SpentMicrousd)
	require.True(t, after.Users[0].AccessPaused)
	require.EqualValues(t, 500000, after.Users[0].Quota.GrantedMicrousd)
	for _, action := range []string{"authorize", "models", "begin"} {
		response = gatewayAccountingRequest(t, s, map[string]any{
			"action": action, "token": job.ModelToken, "reservation_id": id,
		})
		require.Equal(t, 403, response.Code, action)
		require.Contains(t, response.Body.String(), "starter_access_paused")
	}
	response = hostingRequest(t, s, "POST", "/api/v1/starter/wake", "{}", "gateway@example.com", "")
	require.Equal(t, 403, response.Code)
	response = gatewayAccountingRequest(
		t,
		s,
		map[string]any{"action": "cancel", "reservation_id": id},
	)
	require.Equal(t, 200, response.Code)
	for _, query := range []string{"limit=0", "limit=101", "limit=oops", "request_status=oops"} {
		response = hostingRequest(
			t,
			s,
			"GET",
			hostingAdminPath+"?"+query,
			"",
			hostingAdminEmail,
			"",
		)
		require.Equal(t, 400, response.Code, query)
	}
	response = hostingRequest(t, s, "GET", hostingAdminPath+"?user_query=unknown%40example.com",
		"", hostingAdminEmail, "")
	require.Equal(t, 200, response.Code)
	empty := decodeData[hosting.AdminSnapshot](t, response.Body.Bytes())
	require.Empty(t, empty.Users)
	require.Empty(t, empty.Requests)
}

func TestHostingAdminLiveModelPoliciesFenceAdmission(t *testing.T) {
	s := hostingAdminServer(t)
	policy := hosting.ModelPolicy{
		ID:              "trial-model",
		Provider:        "openrouter",
		UpstreamModel:   "vendor/model",
		Moderation:      "off",
		PolicyVersion:   "fixture-v1",
		MaxOutputTokens: 2048,
		InputPrice:      1000000,
		OutputPrice:     5000000,
		Enabled:         true,
	}
	s.hosting.Config.Models = []hosting.ModelPolicy{policy}
	job := gatewayAccountingJob(t, s)
	response := gatewayAccountingRequest(
		t,
		s,
		map[string]any{"action": "models", "token": job.ModelToken},
	)
	require.Equal(t, 200, response.Code)
	catalog := decodeData[struct {
		Models []hosting.ModelPolicy `json:"models"`
	}](t, response.Body.Bytes())
	require.Equal(t, []hosting.ModelPolicy{policy}, catalog.Models)
	response = hostingRequest(t, s, "GET", hostingAdminPath, "", hostingAdminEmail, "")
	require.Equal(t, 200, response.Code)
	initial := decodeData[hosting.AdminSnapshot](t, response.Body.Bytes())
	response = hostingAdminPatch(t, s, map[string]any{
		"revision": 0, "action": "model", "target_id": policy.ID, "moderation": "openai",
		"expected_model_base": initial.ModelBases[policy.ID],
	})
	require.Equal(t, 200, response.Code)
	updated := decodeData[hosting.AdminSnapshot](t, response.Body.Bytes()).Models[0]
	require.Equal(t, "admin-1", updated.PolicyVersion)
	require.Equal(t, policy.Provider, updated.Provider)
	require.Equal(t, policy.UpstreamModel, updated.UpstreamModel)
	response = gatewayAccountingRequest(t, s, map[string]any{
		"action": "reserve", "token": job.ModelToken, "request_id": "old-policy", "tokens": 1000,
		"model": policy.ID, "max_output_tokens": 2048, "policy": policy,
	})
	require.Equal(t, 409, response.Code)
	response = gatewayAccountingRequest(t, s, map[string]any{
		"action": "reserve", "token": job.ModelToken, "request_id": "new-policy", "tokens": 1000,
		"model": updated.ID, "max_output_tokens": 2048, "policy": updated,
	})
	require.Equal(t, 200, response.Code)
}

func TestHostingAdminRequestAuditOnlyContainsSanitizedFailuresAndUsage(t *testing.T) {
	s := hostingAdminServer(t)
	job := gatewayAccountingJob(t, s)
	id := gatewayReserve(t, s, job)
	for _, action := range []string{"moderation", "begin"} {
		body := map[string]any{"action": action, "reservation_id": id}
		if action == "moderation" {
			body["moderation"] = map[string]any{
				"verdict": "allowed",
				"model":   hosting.ModerationModel,
			}
		}
		require.Equal(t, 200, gatewayAccountingRequest(t, s, body).Code)
	}
	for _, code := range []string{"provider supplied secret", "model_upstream_failed"} {
		response := gatewayAccountingRequest(t, s, map[string]any{
			"action": "settle", "reservation_id": id, "known": true,
			"usage": map[string]any{"input_tokens": 25, "output_tokens": 5, "error_code": code,
				"raw_usage": map[string]any{"input_tokens": 25, "prompt": "private prompt"}},
		})
		if code == "model_upstream_failed" {
			require.Equal(t, 200, response.Code)
		} else {
			require.Equal(t, 409, response.Code)
		}
	}
	response := hostingRequest(
		t,
		s,
		"GET",
		hostingAdminPath+"?request_status=settled",
		"",
		hostingAdminEmail,
		"",
	)
	require.Equal(t, 200, response.Code)
	snapshot := decodeData[hosting.AdminSnapshot](t, response.Body.Bytes())
	require.Len(t, snapshot.Requests, 1)
	require.Equal(t, "model_upstream_failed", snapshot.Requests[0].ErrorCode)
	require.EqualValues(t, 25, snapshot.Requests[0].InputTokens)
	require.NotContains(t, response.Body.String(), "private prompt")
	require.NotContains(t, response.Body.String(), "raw_usage")
	require.Contains(t, response.Body.String(), fmt.Sprintf(`"user_id":%q`, job.OwnerUserID))
}
