package manager

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func legacyModelConfig() domain.SessionACPConfig {
	return domain.SessionACPConfig{
		Source: domain.SessionConfigSourceLegacy,
		LegacyModels: &domain.SessionLegacyModels{
			CurrentModelID: "provider:old",
			Available: []domain.SessionModel{
				{ID: "provider:old", Name: "Old model"},
				{ID: "provider:new", Name: "New model", Description: "Another model"},
			},
		},
	}
}

func TestLegacyModelConfigurationExposesSelectableModels(t *testing.T) {
	config := legacyModelConfig()
	response := sessionConfigAPIResponse(domain.AgentSession{ACPConfig: config})
	require.Len(t, response.Options, 1)
	option := response.Options[0]
	assert.Equal(t, "model", option.ID)
	assert.Equal(t, "model", option.Category)
	assert.Equal(t, "select", option.Type)
	assert.Equal(t, "provider:old", option.CurrentValue)
	require.Len(t, option.Options, 2)
	assert.Equal(t, "provider:new", option.Options[1].Value)
	assert.Equal(t, "Another model", option.Options[1].Description)
	assert.True(t, response.CanSet)
	assert.False(t, response.CanForceRefresh)
	assert.Empty(t, config.Options, "API projection must not invent native config options")

	config.Options = []domain.SessionConfigOption{{
		ID: "native-model", Category: "model", Type: "select", CurrentValue: "standard",
	}}
	response = sessionConfigAPIResponse(domain.AgentSession{ACPConfig: config})
	require.Len(t, response.Options, 1)
	assert.Equal(t, "native-model", response.Options[0].ID)
	assert.Equal(t, "standard", response.Options[0].CurrentValue)

	response = sessionConfigAPIResponse(domain.AgentSession{
		ACPConfig: legacyModelConfig(), Transport: domain.SessionTransportE2EE,
	})
	assert.False(t, response.CanSet)
	assert.False(t, response.CanForceRefresh)
}

func TestLegacyModelSwitchRoundTrip(t *testing.T) {
	for _, tc := range []struct {
		name       string
		standard   bool
		agentError string
	}{
		{name: "legacy acknowledgement persists model"},
		{name: "busy agent preserves model", agentError: "Session is busy"},
		{name: "unsupported method preserves model", agentError: "Method not found"},
		{name: "standard option takes precedence", standard: true},
		{name: "standard failure does not fall back", standard: true, agentError: "Method not found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			testLegacyModelSwitch(t, tc.standard, tc.agentError)
		})
	}
}

func testLegacyModelSwitch(t *testing.T, standard bool, agentError string) {
	t.Helper()
	srv, _ := testServer(t, "owner@example.com")
	fixture := testNodeAgent(t, srv, "owner@example.com")
	principal := testUserPrincipal(t, srv, fixture.userEmail)
	session, err := srv.store.CreateNodeAgentSession(
		t.Context(),
		principal,
		domain.CreateSessionRequest{
			NodeID: fixture.nodeID, AgentID: fixture.agentID,
			SessionID: "sess_model", NativeID: "native_model", Source: domain.MessageSourceACPTunnel,
		},
	)
	require.NoError(t, err)
	config := legacyModelConfig()
	config.Options = []domain.SessionConfigOption{{
		ID: "fast", Name: "Fast mode", Type: "boolean", CurrentValue: false,
	}}
	if standard {
		config.Options = append(config.Options, domain.SessionConfigOption{
			ID: "model", Category: "model", Type: "select", CurrentValue: "provider:old",
			Options: []domain.SessionConfigValue{{Value: "provider:new", Name: "New model"}},
		})
	}
	require.NoError(
		t,
		srv.store.UpdateSessionACPConfig(t.Context(), fixture.agentID, session.SessionID, config),
	)
	path := "/api/v1/user/self/nodes/" + fixture.nodeID + "/agents/" + fixture.agentID +
		"/sessions/" + session.SessionID + "/configuration"
	handler := srv.routes()
	request := func(method, suffix, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path+suffix, strings.NewReader(body))
		req.Header.Set("X-User-Email", fixture.userEmail)
		setJSON(req)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	// Reject unadvertised values before trying to contact a runtime.
	rejected := request(http.MethodPatch, "/options/model", `{"value":"unlisted"}`)
	require.Equal(t, http.StatusBadRequest, rejected.Code, rejected.Body.String())
	wrongType := request(http.MethodPatch, "/options/model", `{"value":true}`)
	require.Equal(t, http.StatusBadRequest, wrongType.Code, wrongType.Body.String())

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agent/tunnel", srv.handleAgentACPTunnel)
	httpServer := httptest.NewServer(mux)
	defer httpServer.Close()
	agentWS := dialMockAgentTunnel(t, "ws"+strings.TrimPrefix(httpServer.URL, "http"),
		fixture.nodeAPIKey, fixture.agentID)
	defer func() { assert.NoError(t, agentWS.Close()) }()
	waitACPTunnelAgentRegistered(t, srv, fixture.agentID, "")
	replies := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		replies <- request(http.MethodPatch, "/options/model", `{"value":"provider:new"}`)
	}()
	method := "session/set_model"
	if standard {
		method = "session/set_config_option"
	}
	env, rpc := readMockACPRequest(t, agentWS, method)
	var params map[string]any
	require.NoError(t, json.Unmarshal(rpc.Params, &params))
	if standard {
		assert.Equal(t, "model", params["configId"])
		assert.Equal(t, "provider:new", params["value"])
	} else {
		assert.Equal(t, "provider:new", params["modelId"])
		assert.NotContains(t, params, "configId")
	}
	// The durable model must not change before the agent acknowledges it.
	stored, err := srv.store.GetSession(t.Context(), principal, session.SessionID)
	require.NoError(t, err)
	assert.Equal(t, "provider:old", stored.Model)
	result := json.RawMessage(`{}`)
	if standard {
		result = json.RawMessage(
			`{"configOptions":[{"id":"model","category":"model","currentValue":"provider:new","options":[{"value":"provider:new","name":"New model"}]}]}`,
		)
	}
	if agentError != "" {
		writeMockACPError(t, agentWS, env.QueueID, 1, rpc.ID, -32603, agentError, "", false)
	} else {
		writeMockACPResponse(t, agentWS, env.QueueID, 1, rpc.ID, result)
	}
	select {
	case rec := <-replies:
		if agentError != "" {
			assert.GreaterOrEqual(t, rec.Code, 400, rec.Body.String())
			assert.Contains(t, rec.Body.String(), agentError)
		} else {
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("model update did not complete")
	}
	wantModel := "provider:new"
	if agentError != "" {
		wantModel = "provider:old"
	}
	stored, err = srv.store.GetSession(t.Context(), principal, session.SessionID)
	require.NoError(t, err)
	assert.Equal(t, wantModel, stored.Model)
	if !standard {
		assert.Equal(t, config.Options, stored.ACPConfig.Options)
		assert.Equal(t, config.LegacyModels.Available, stored.ACPConfig.LegacyModels.Available)
		assert.Equal(t, wantModel, stored.ACPConfig.LegacyModels.CurrentModelID)
	}
	reloaded := request(http.MethodGet, "", "")
	require.Equal(t, http.StatusOK, reloaded.Code)
	snapshot := decodeData[sessionConfigResponse](t, reloaded.Body.Bytes())
	model := sessionConfigOptionByID(domain.SessionACPConfig{
		Options: snapshot.Options,
	}, "model")
	require.NotNil(t, model)
	assert.Equal(t, wantModel, model.CurrentValue)
}
