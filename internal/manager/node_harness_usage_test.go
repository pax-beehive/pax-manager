package manager

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gorilla/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestNodeHarnessUsageGivenConnectedNodeWhenReadingUsageThenForwardsQueryAndReturnsUncachedView(
	t *testing.T,
) {
	for name, tc := range map[string]struct {
		queryResult string
		expected    domain.HarnessUsageView
	}{
		"current paxd": {
			`{"type":"harness_usage.status","harness_usage":{"harness":"claude","state":"available",` +
				`"observed_at":"2026-11-12T17:31:51Z","plan":"max","windows":[{"id":"seven_day",` +
				`"window_minutes":10080,"used_percent":3,"resets_at":"2026-11-16T09:00:00Z",` +
				`"access_token":"private-token"}],"extra_usage":{"enabled":false}}}`,
			domain.HarnessUsageView{
				Harness:    "claude",
				State:      "available",
				ObservedAt: "2026-11-12T17:31:51Z",
				Plan:       "max",
				Windows: []domain.HarnessUsageWindow{{
					ID:            "seven_day",
					WindowMinutes: 10080,
					UsedPercent:   3,
					ResetsAt:      "2026-11-16T09:00:00Z",
				}},
				ExtraUsage: &domain.HarnessUsageExtra{},
			},
		},
		"paxd without usage support": {
			`{"type":"harness_usage.status","error":{"code":"invalid_argument",` +
				`"message":"exactly one query payload is required"}}`,
			domain.HarnessUsageView{
				Harness:   "claude",
				State:     "unavailable",
				ErrorCode: "daemon_unsupported",
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv, registered := testNodeControlServer(t, "todd@example.com")
			ws, closeServer := dialNodeControlTunnel(t, srv, registered)
			defer closeServer()
			defer func() { _ = ws.Close() }()
			waitNodeControlConnection(t, srv.nodeControls, registered.NodeID)
			writeNodeControlHeartbeat(t, ws, registered.NodeID, "usage")
			waitNodeControlRemoteID(t, srv.nodeControls, registered.NodeID, "remote_prod")
			path := "/api/v1/user/self/nodes/" + registered.NodeID + "/harnesses/claude/usage"
			rec := serveAsync(t, srv, http.MethodGet, path, "", func() {
				query := readNodeControlFrame(t, ws)
				assert.Equal(t, "query", query.Kind)
				assert.JSONEq(
					t,
					`{"type":"harness_usage.status","harness_usage_status":{"harness":"claude"}}`,
					string(query.Query),
				)
				require.NoError(t, ws.WriteMessage(websocket.TextMessage, []byte(
					`{"kind":"response","request_id":`+strconv.Quote(query.RequestID)+
						`,"query_result":`+tc.queryResult+`}`,
				)))
			})
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
			assert.Equal(
				t,
				tc.expected,
				decodeData[domain.HarnessUsageData](t, rec.Body.Bytes()).HarnessUsage,
			)
			assert.NotContains(t, rec.Body.String(), "private")
		})
	}
}

func TestNodeHarnessUsageGivenInvalidOrForeignRequestWhenReadingThenReturnsUncachedErrors(
	t *testing.T,
) {
	srv, registered := testNodeControlServer(t, "todd@example.com")
	base := "/api/v1/user/self/nodes/" + registered.NodeID + "/harnesses/"

	rec := serve(srv, http.MethodGet, base+"pi/usage", "")
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))

	rec = serve(srv, http.MethodGet, base+"claude/usage", "")
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))

	req := httptest.NewRequest(http.MethodGet, base+"claude/usage", nil)
	req.Header.Set("X-User-Email", "different@example.com")
	rec = httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
}
