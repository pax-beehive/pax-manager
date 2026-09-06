package manager

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestParseSessionACPConfig(t *testing.T) {
	observedAt := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	t.Run("normalizes select boolean and grouped options", func(t *testing.T) {
		config, found := parseSessionACPConfig(json.RawMessage(`{
			"configOptions":[
				{"id":"model","name":"Model","category":"model","type":"select","currentValue":"gpt-6","options":[
					{"group":"Current","options":[{"value":"gpt-6","name":"GPT-6"}]},
					{"value":"gpt-5.6-sol","name":"GPT-5.6 Sol"}
				]},
				{"id":"fast","name":"Fast","type":"boolean","currentValue":false}
			]
		}`), domain.SessionConfigSourceNew, observedAt)

		require.True(t, found)
		require.Len(t, config.Options, 2)
		assert.Equal(t, "gpt-6", config.Options[0].CurrentValue)
		assert.Equal(t, "Current", config.Options[0].Options[0].Group)
		assert.Equal(t, false, config.Options[1].CurrentValue)
		assert.Equal(t, observedAt, config.ObservedAt)
	})

	t.Run("accepts v2 configId and legacy models", func(t *testing.T) {
		config, found := parseSessionACPConfig(json.RawMessage(`{
			"configOptions":[{"configId":"reasoning_effort","name":"Thinking","currentValue":"high","options":[{"value":"high","name":"High"}]}],
			"models":{"currentModelId":"deepseek:v4","availableModels":[{"modelId":"deepseek:v4","name":"DeepSeek V4"}]}
		}`), domain.SessionConfigSourceNew, observedAt)

		require.True(t, found)
		assert.Equal(t, "reasoning_effort", config.Options[0].ID)
		assert.Equal(t, "select", config.Options[0].Type)
		require.NotNil(t, config.LegacyModels)
		assert.Equal(t, "deepseek:v4", config.LegacyModels.CurrentModelID)
	})

	t.Run("does not invent an observation from unrelated payload", func(t *testing.T) {
		_, found := parseSessionACPConfig(
			json.RawMessage(`{"sessionId":"sess_1"}`),
			domain.SessionConfigSourceNew,
			observedAt,
		)
		assert.False(t, found)
	})

	t.Run("does not replace a snapshot with malformed or null options", func(t *testing.T) {
		for _, raw := range []json.RawMessage{
			json.RawMessage(`{"configOptions":null}`),
			json.RawMessage(`{"configOptions":"invalid"}`),
		} {
			_, found := parseSessionACPConfig(raw, domain.SessionConfigSourceResponse, observedAt)
			assert.False(t, found)
		}
	})
}

func TestValidateSessionConfigValue(t *testing.T) {
	t.Run("rejects permission mode", func(t *testing.T) {
		_, err := validateSessionConfigValue(domain.SessionConfigOption{
			ID:       "mode",
			Category: domain.SessionConfigCategoryMode,
			Type:     "select",
		}, json.RawMessage(`"agent"`))
		var endpointErr apperr.Error
		require.ErrorAs(t, err, &endpointErr)
		assert.Equal(t, http.StatusConflict, endpointErr.Status)
	})

	t.Run("requires an advertised select value", func(t *testing.T) {
		option := domain.SessionConfigOption{
			ID:   "model",
			Type: "select",
			Options: []domain.SessionConfigValue{{
				Value: "gpt-6",
			}},
		}
		value, err := validateSessionConfigValue(option, json.RawMessage(`"gpt-6"`))
		require.NoError(t, err)
		assert.Equal(t, "gpt-6", value)
		_, err = validateSessionConfigValue(option, json.RawMessage(`"unlisted"`))
		require.Error(t, err)
	})

	t.Run("preserves boolean type", func(t *testing.T) {
		value, err := validateSessionConfigValue(
			domain.SessionConfigOption{ID: "fast", Type: "boolean"},
			json.RawMessage(`true`),
		)
		require.NoError(t, err)
		assert.Equal(t, true, value)
	})
}

func TestForceSessionConfigRefreshOptionPrefersModel(t *testing.T) {
	config := domain.SessionACPConfig{Options: []domain.SessionConfigOption{
		{ID: "mode", Category: "mode", CurrentValue: "default"},
		{ID: "fast-mode", CurrentValue: "off"},
		{ID: "model", Category: "model", CurrentValue: "gpt-6"},
	}}
	selected := forceSessionConfigRefreshOption(config)
	require.NotNil(t, selected)
	assert.Equal(t, "model", selected.ID)
}

func TestSessionConfigAPIResponseAlwaysReturnsOptionsArray(t *testing.T) {
	response := sessionConfigAPIResponse(domain.AgentSession{SessionID: "sess_1"})
	require.NotNil(t, response.Options)
	assert.Empty(t, response.Options)
}

func TestSessionConfigurationEndpointReturnsStoredSnapshot(t *testing.T) {
	srv, _ := testServer(t, "owner@example.com")
	fixture := testNodeAgent(t, srv, "owner@example.com")
	principal := testUserPrincipal(t, srv, fixture.userEmail)
	session, err := srv.store.CreateNodeAgentSession(
		t.Context(),
		principal,
		domain.CreateSessionRequest{
			NodeID:    fixture.nodeID,
			AgentID:   fixture.agentID,
			SessionID: "sess_config_1",
			NativeID:  "native_config_1",
			Source:    domain.MessageSourceACPTunnel,
		},
	)
	require.NoError(t, err)
	config := domain.SessionACPConfig{
		Source:     domain.SessionConfigSourceUpdate,
		ObservedAt: time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC),
		Options: []domain.SessionConfigOption{{
			ID:           "model",
			Name:         "Model",
			Category:     domain.SessionConfigCategoryModel,
			Type:         "select",
			CurrentValue: "gpt-6",
			Options: []domain.SessionConfigValue{{
				Value: "gpt-6",
				Name:  "GPT-6",
			}},
		}},
	}
	require.NoError(t, srv.store.UpdateSessionACPConfig(
		t.Context(),
		fixture.agentID,
		session.SessionID,
		config,
	))

	req := httptest.NewRequest(
		http.MethodGet,
		"/api/v1/user/self/nodes/"+fixture.nodeID+"/agents/"+fixture.agentID+
			"/sessions/"+session.SessionID+"/configuration",
		nil,
	)
	req.Header.Set("X-User-Email", fixture.userEmail)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	response := decodeData[sessionConfigResponse](t, rec.Body.Bytes())
	assert.Equal(t, session.SessionID, response.SessionID)
	require.Len(t, response.Options, 1)
	assert.Equal(t, "gpt-6", response.Options[0].CurrentValue)
	assert.Equal(t, domain.SessionConfigSourceUpdate, response.Source)
	assert.True(t, response.CanSet)
	assert.True(t, response.CanForceRefresh)
}

func TestSessionConfigurationGivenOpenAPIDocumentWhenGeneratedThenEndpointsAreDocumented(
	t *testing.T,
) {
	raw, err := openAPIDocument("https://manager.example")
	require.NoError(t, err)
	var document struct {
		Paths map[string]json.RawMessage `json:"paths"`
	}
	require.NoError(t, json.Unmarshal(raw, &document))
	assert.Contains(t, document.Paths, openAPIUserSessionConfig)
	assert.Contains(t, document.Paths, openAPIUserSessionConfigRefresh)
	assert.Contains(t, document.Paths, openAPIUserSessionConfigOption)
}
