package manager

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestCanonicalSessionMetadataRouteChecksIdentityAndOwnership(t *testing.T) {
	srv, _ := testServer(t, "owner@example.com")
	fixture := testNodeAgent(t, srv, "owner@example.com")
	principal := testUserPrincipal(t, srv, fixture.userEmail)
	session, err := srv.store.CreateNodeAgentSession(
		t.Context(),
		principal,
		domain.CreateSessionRequest{
			NodeID: fixture.nodeID, AgentID: fixture.agentID, SessionID: "canonical-session", NativeID: "native-session",
		},
	)
	require.NoError(t, err)
	read := func(userPath, id, email string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/user/"+userPath+"/sessions/"+id, nil)
		if email != "" {
			req.Header.Set("X-User-Email", email)
		}
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, req)
		return rec
	}
	t.Run(
		"Given an authenticated owner then canonical metadata includes both routing IDs",
		func(t *testing.T) {
			rec := read("self", session.SessionID, fixture.userEmail)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			got := decodeData[AgentSession](t, rec.Body.Bytes())
			assert.Equal(t, session.SessionID, got.SessionID)
			assert.Equal(t, fixture.agentID, got.AgentID)
			assert.Equal(t, fixture.nodeID, got.NodeID)
		},
	)
	t.Run("Given no identity then the direct route requires authentication", func(t *testing.T) {
		anonymousServer := newServer(Config{}, srv.store)
		req := httptest.NewRequest(
			http.MethodGet,
			"/api/v1/user/self/sessions/"+session.SessionID,
			nil,
		)
		rec := httptest.NewRecorder()
		anonymousServer.routes().ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
	})
	t.Run(
		"Given an unrelated owner then neither self nor an explicit owner path grants access",
		func(t *testing.T) {
			other := testNodeAgent(t, srv, "other@example.com")
			assert.Equal(
				t,
				http.StatusNotFound,
				read("self", session.SessionID, other.userEmail).Code,
			)
			assert.Equal(
				t,
				http.StatusNotFound,
				read(principal.User.UserID, session.SessionID, other.userEmail).Code,
			)
		},
	)
	t.Run("Given an absent canonical session then return not found", func(t *testing.T) {
		assert.Equal(t, http.StatusNotFound, read("self", "missing", fixture.userEmail).Code)
	})
}
