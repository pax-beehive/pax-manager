package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSessionCommandsPersistNotificationAndReadConfiguration(t *testing.T) {
	srv, _ := testServer(t, "owner@example.com")
	fixture := testNodeAgent(t, srv, "owner@example.com")
	principal := testUserPrincipal(t, srv, fixture.userEmail)
	session, err := srv.store.CreateNodeAgentSession(
		t.Context(),
		principal,
		domain.CreateSessionRequest{
			NodeID: fixture.nodeID, AgentID: fixture.agentID, SessionID: "sess_commands", NativeID: "native_commands", Source: domain.MessageSourceACPTunnel,
		},
	)
	require.NoError(t, err)
	path := "/api/v1/user/self/nodes/" + fixture.nodeID + "/agents/" + fixture.agentID + "/sessions/" + session.SessionID + "/configuration"
	read := func(email string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-User-Email", email)
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, req)
		return rec
	}
	initial := read(fixture.userEmail)
	require.Equal(t, http.StatusOK, initial.Code)
	assert.Nil(t, decodeData[sessionConfigResponse](t, initial.Body.Bytes()).Commands)

	observe := func(commands string, direction acpFrameDirection) {
		raw := []byte(
			`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess_commands","update":{"sessionUpdate":"available_commands_update","availableCommands":` + commands + `}}}`,
		)
		frame := newACPFrameContext(
			&ACPTunnelAgent{agentID: fixture.agentID},
			direction,
			websocket.TextMessage,
			raw,
		)
		frame.managerSessionID = session.SessionID
		called := false
		err := (sessionCommandsObservationMiddleware{service: srv}).HandleACPFrame(
			t.Context(),
			frame,
			func(_ context.Context, _ *acpFrameContext) error { called = true; return nil },
		)
		require.NoError(t, err)
		require.True(t, called)
	}
	advertised := `[{"name":"review","description":"Review changes","input":{"hint":"instructions","type":"_custom"},"_meta":{"extension":true}}]`
	observe(advertised, acpAgentToUser)
	// A separate configuration update must not erase the command catalog.
	require.NoError(
		t,
		srv.store.UpdateSessionACPConfig(
			t.Context(),
			fixture.agentID,
			session.SessionID,
			domain.SessionACPConfig{
				Source:     domain.SessionConfigSourceUpdate,
				ObservedAt: time.Now(),
			},
		),
	)
	observe(`null`, acpAgentToUser)
	observe(`[{"name":"invalid"}]`, acpAgentToUser)
	observe(`[]`, acpUserToAgent)
	rec := read(fixture.userEmail)
	require.Equal(t, http.StatusOK, rec.Code)
	snapshot := decodeData[sessionConfigResponse](t, rec.Body.Bytes()).Commands
	require.NotNil(t, snapshot)
	require.Len(t, snapshot.AvailableCommands, 1)
	assert.JSONEq(t, advertised[1:len(advertised)-1], string(snapshot.AvailableCommands[0]))
	assert.False(t, snapshot.ObservedAt.IsZero())
	assert.Equal(t, http.StatusNotFound, read("other@example.com").Code)

	observe(`[{"name":"status","description":"Status"}]`, acpAgentToUser)
	snapshot = decodeData[sessionConfigResponse](t, read(fixture.userEmail).Body.Bytes()).Commands
	require.Len(t, snapshot.AvailableCommands, 1)
	assert.JSONEq(
		t,
		`{"name":"status","description":"Status"}`,
		string(snapshot.AvailableCommands[0]),
	)
	observe(`[]`, acpAgentToUser)
	snapshot = decodeData[sessionConfigResponse](t, read(fixture.userEmail).Body.Bytes()).Commands
	require.NotNil(t, snapshot)
	require.NotNil(t, snapshot.AvailableCommands)
	assert.Empty(t, snapshot.AvailableCommands)
}

func TestParseSessionACPCommandsRejectsMalformedUpdates(t *testing.T) {
	for _, raw := range []string{
		`{}`, `{"update":{"sessionUpdate":"config_option_update","availableCommands":[]}}`,
		`{"update":{"sessionUpdate":"available_commands_update","availableCommands":null}}`,
		`{"update":{"sessionUpdate":"available_commands_update","availableCommands":[{"name":" ","description":"empty"}]}}`,
		`{"update":{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"x","description":"a"},{"name":"x","description":"b"}]}}`,
	} {
		_, ok := parseSessionACPCommands(json.RawMessage(raw), time.Now())
		assert.False(t, ok, raw)
	}
}
