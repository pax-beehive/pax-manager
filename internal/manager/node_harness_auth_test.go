package manager

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestNodeHarnessAuthGivenSupportedNativeMethodsWhenStartingOverControlTunnelThenNeverJournalsSecrets(
	t *testing.T,
) {
	for _, tc := range []struct{ harness, method, secret string }{
		{"claude", "subscription", ""}, {"claude", "console", ""},
		{"codex", "device", ""}, {"codex", "api-key", "private-api-key"},
		{"codex", "access-token", "private-access-token"},
	} {
		t.Run(tc.harness+"/"+tc.method, func(t *testing.T) {
			srv, registered := testNodeControlServer(t, "todd@example.com")
			ws, closeServer := dialNodeControlTunnel(t, srv, registered)
			defer closeServer()
			defer func() { _ = ws.Close() }()
			waitNodeControlConnection(t, srv.nodeControls, registered.NodeID)
			writeNodeControlHeartbeat(t, ws, registered.NodeID, "identity")
			waitNodeControlRemoteID(t, srv.nodeControls, registered.NodeID, "remote_prod")
			path := "/api/v1/user/self/nodes/" + registered.NodeID + "/harnesses/" + tc.harness + "/auth/"
			body, err := json.Marshal(map[string]string{
				"command_id": "login", "operation": "start", "method": tc.method, "code": tc.secret,
			})
			require.NoError(t, err)
			rec := serveAsync(t, srv, http.MethodPost, path+"login", string(body), func() {
				query := readNodeControlFrame(t, ws)
				assert.Equal(t, "query", query.Kind)
				assert.JSONEq(
					t,
					`{"type":"harness_auth.status","harness_auth_status":{"harness":"`+tc.harness+`","session_id":""}}`,
					string(query.Query),
				)
				writeHarnessAuthQueryResult(
					t,
					ws,
					query.RequestID,
					domain.HarnessAuthView{Harness: tc.harness, State: "logged_out"},
				)
				command := readNodeControlFrame(t, ws)
				assert.Equal(t, "command", command.Kind)
				assert.Equal(t, "login", command.CommandID)
				var payload struct {
					Login domain.HarnessAuthLogin `json:"harness_auth_login"`
				}
				require.NoError(t, json.Unmarshal(command.Command, &payload))
				assert.Equal(
					t,
					domain.HarnessAuthLogin{
						Harness:   tc.harness,
						Operation: "start",
						Method:    tc.method,
						Code:      tc.secret,
					},
					payload.Login,
				)
				writeHarnessAuthAck(
					t,
					ws,
					command.CommandID,
					domain.HarnessAuthView{
						Harness:   tc.harness,
						SessionID: "attempt",
						State:     "starting",
						Method:    tc.method,
					},
				)
			})
			require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
			assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
			assert.Equal(
				t,
				"attempt",
				decodeData[domain.HarnessAuthData](t, rec.Body.Bytes()).HarnessAuth.SessionID,
			)
			if tc.secret != "" {
				assert.NotContains(t, rec.Body.String(), tc.secret)
			}
			state := "awaiting_browser"
			if tc.harness == "claude" {
				state = "awaiting_code"
			}
			rec = serveAsync(t, srv, http.MethodGet, path+"status?session_id=attempt", "", func() {
				query := readNodeControlFrame(t, ws)
				writeHarnessAuthQueryResult(t, ws, query.RequestID, domain.HarnessAuthView{
					Harness: tc.harness, SessionID: "attempt", State: state,
					AuthorizationURL: "https://claude.ai/oauth/authorize?state=private", UserCode: "USER-CODE",
				})
			})
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
			assert.Equal(
				t,
				state,
				decodeData[domain.HarnessAuthData](t, rec.Body.Bytes()).HarnessAuth.State,
			)
			for _, operation := range []string{"submit", "cancel"} {
				if operation == "submit" && tc.harness != "claude" {
					continue
				}
				secret := ""
				if operation == "submit" {
					secret = "one-time-code#state"
				}
				body, err = json.Marshal(
					map[string]string{
						"command_id": operation,
						"operation":  operation,
						"session_id": "attempt",
						"code":       secret,
					},
				)
				require.NoError(t, err)
				rec = serveAsync(t, srv, http.MethodPost, path+"login", string(body), func() {
					command := readNodeControlFrame(t, ws)
					writeHarnessAuthAck(
						t,
						ws,
						command.CommandID,
						domain.HarnessAuthView{
							Harness:   tc.harness,
							SessionID: "attempt",
							State:     "cancelled",
						},
					)
				})
				require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
				if secret != "" {
					assert.NotContains(t, rec.Body.String(), secret)
				}
			}
			rec = serve(
				srv,
				http.MethodGet,
				"/api/v1/user/self/nodes/"+registered.NodeID+"/daemon/commands",
				"",
			)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Empty(t, decodeData[struct {
				Commands []any `json:"commands"`
			}](t, rec.Body.Bytes()).Commands)
		})
	}
}

func TestNodeHarnessAuthGivenMalformedOrUnauthorizedRequestWhenBindingThenReturnsUncachedSafeErrors(
	t *testing.T,
) {
	srv, registered := testNodeControlServer(t, "todd@example.com")
	path := "/api/v1/user/self/nodes/" + registered.NodeID + "/harnesses/claude/auth/login"
	for _, body := range []string{
		"", `{"code":"private",`, `{"code":"private","unexpected":true}`,
		`{"operation":"start","command_id":"start"} {"code":"private"}`, strings.Repeat("x", 32*1024+1),
	} {
		rec := serve(srv, http.MethodPost, path, body)
		assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
		assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
		assert.NotContains(t, rec.Body.String(), "private")
	}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		suffix, body := "status", ""
		if method == http.MethodPost {
			suffix, body = "login", `{"command_id":"start","operation":"start"}`
		}
		req := httptest.NewRequest(
			method,
			strings.TrimSuffix(path, "login")+suffix,
			strings.NewReader(body),
		)
		req.Header.Set("X-User-Email", "different@example.com")
		if body != "" {
			setJSON(req)
		}
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, req)
		assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
		assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	}
}

func TestNodeHarnessAuthGivenLostStartAckWhenTunnelReconnectsThenStatusRecoversAttempt(
	t *testing.T,
) {
	srv, registered := testNodeControlServer(t, "todd@example.com")
	ws, closeServer := dialNodeControlTunnel(t, srv, registered)
	defer closeServer()
	waitNodeControlConnection(t, srv.nodeControls, registered.NodeID)
	writeNodeControlHeartbeat(t, ws, registered.NodeID, "identity")
	waitNodeControlRemoteID(t, srv.nodeControls, registered.NodeID, "remote_prod")
	path := "/api/v1/user/self/nodes/" + registered.NodeID + "/harnesses/claude/auth/"
	rec := serveAsync(
		t,
		srv,
		http.MethodPost,
		path+"login",
		`{"command_id":"lost","operation":"start"}`,
		func() {
			query := readNodeControlFrame(t, ws)
			writeHarnessAuthQueryResult(
				t,
				ws,
				query.RequestID,
				domain.HarnessAuthView{Harness: "claude", State: "logged_out"},
			)
			command := readNodeControlFrame(t, ws)
			assert.Equal(t, "lost", command.CommandID)
			require.NoError(t, ws.Close())
		},
	)
	require.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	ws, closeSecond := dialNodeControlTunnel(t, srv, registered)
	defer closeSecond()
	defer func() { _ = ws.Close() }()
	waitNodeControlConnection(t, srv.nodeControls, registered.NodeID)
	writeNodeControlHeartbeat(t, ws, registered.NodeID, "identity-reconnect")
	waitNodeControlRemoteID(t, srv.nodeControls, registered.NodeID, "remote_prod")
	rec = serveAsync(t, srv, http.MethodGet, path+"status", "", func() {
		query := readNodeControlFrame(t, ws)
		writeHarnessAuthQueryResult(t, ws, query.RequestID, domain.HarnessAuthView{
			Harness: "claude", SessionID: "accepted-before-disconnect", State: "awaiting_code",
			Method: "subscription", AuthorizationURL: "https://claude.ai/oauth/authorize?state=private",
		})
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(
		t,
		"accepted-before-disconnect",
		decodeData[domain.HarnessAuthData](t, rec.Body.Bytes()).HarnessAuth.SessionID,
	)
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
}

func TestNodeHarnessAuthGivenLoggedInAccountWhenReadingStatusThenReturnsUncachedPublicIdentity(
	t *testing.T,
) {
	srv, registered := testNodeControlServer(t, "todd@example.com")
	ws, closeServer := dialNodeControlTunnel(t, srv, registered)
	defer closeServer()
	defer func() { _ = ws.Close() }()
	waitNodeControlConnection(t, srv.nodeControls, registered.NodeID)
	writeNodeControlHeartbeat(t, ws, registered.NodeID, "account-identity")
	waitNodeControlRemoteID(t, srv.nodeControls, registered.NodeID, "remote_prod")
	path := "/api/v1/user/self/nodes/" + registered.NodeID + "/harnesses/claude/auth/status"
	rec := serveAsync(t, srv, http.MethodGet, path, "", func() {
		query := readNodeControlFrame(t, ws)
		require.NoError(
			t,
			ws.WriteMessage(
				websocket.TextMessage,
				[]byte(
					`{"kind":"response","request_id":`+strconv.Quote(
						query.RequestID,
					)+`,"query_result":{"type":"harness_auth.status","harness_auth":{"harness":"claude","state":"logged_in","logged_in":true,"account":{"email":"person@example.com","display_name":"Person","organization":"Workspace","plan":"Pro","access_token":"private-token"}}}}`,
				),
			),
		)
	})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
	assert.Equal(t, &domain.HarnessAuthAccount{
		Email: "person@example.com", DisplayName: "Person", Organization: "Workspace", Plan: "Pro",
	}, decodeData[domain.HarnessAuthData](t, rec.Body.Bytes()).HarnessAuth.Account)
	assert.NotContains(t, rec.Body.String(), "access_token")
	assert.NotContains(t, rec.Body.String(), "private-token")
}

func writeHarnessAuthQueryResult(
	t *testing.T,
	ws *websocket.Conn,
	requestID string,
	view domain.HarnessAuthView,
) {
	t.Helper()
	data, err := json.Marshal(
		map[string]any{
			"kind":         "response",
			"request_id":   requestID,
			"query_result": map[string]any{"type": "harness_auth.status", "harness_auth": view},
		},
	)
	require.NoError(t, err)
	require.NoError(t, ws.WriteMessage(websocket.TextMessage, data))
}

func writeHarnessAuthAck(
	t *testing.T,
	ws *websocket.Conn,
	commandID string,
	view domain.HarnessAuthView,
) {
	t.Helper()
	data, err := json.Marshal(view)
	require.NoError(t, err)
	require.NoError(
		t,
		ws.WriteMessage(
			websocket.TextMessage,
			[]byte(
				`{"kind":"ack","command_id":`+strconv.Quote(
					commandID,
				)+`,"command_ack":{"command_id":`+strconv.Quote(
					commandID,
				)+`,"ok":true,"result":{"harness_auth":`+string(
					data,
				)+`}}}`,
			),
		),
	)
}
