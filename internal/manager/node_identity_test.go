package manager

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestNodeIdentityGivenWrongRoutingHintThenReturnsKeyOwner(t *testing.T) {
	store := NewMemoryStore(time.Now)
	srv := newServer(Config{Region: "hk"}, store)
	t.Cleanup(func() { require.NoError(t, srv.CloseTransportStore(t.Context())) })
	owner, err := store.EnsureUser(t.Context(), "owner@example.com", "Owner", "user")
	require.NoError(t, err)
	node, err := store.RegisterNode(
		t.Context(),
		owner,
		domain.RegisterNodeRequest{Hostname: "device"},
		srv.secrets.Hash("node-secret"),
	)
	require.NoError(t, err)
	for _, hint := range []string{"", "usr_someone_else", "corrupt"} {
		t.Run(hint, func(t *testing.T) {
			req := httptest.NewRequest(
				http.MethodGet,
				"/api/v1/node/identity?user_id=usr_forged&region=us",
				nil,
			)
			req.Header.Set("X-Pax-Key", "node-secret")
			req.Header.Set("X-Pax-User-ID", hint)
			req.Header.Set("X-User-Email", "attacker@example.com")
			rec := httptest.NewRecorder()
			srv.routes().ServeHTTP(rec, req)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.Equal(
				t,
				map[string]string{"node_id": node.NodeID, "user_id": owner.UserID, "region": "hk"},
				decodeData[map[string]string](t, rec.Body.Bytes()),
			)
			assert.Equal(t, owner.UserID, rec.Header().Get("X-Pax-User-ID"))
			assert.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
			assert.NotContains(t, rec.Body.String(), "node-secret")
		})
	}
	for _, key := range []string{"", "invalid-key"} {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/node/identity", nil)
		req.Header.Set("X-Pax-Key", key)
		req.Header.Set("X-Pax-User-ID", owner.UserID)
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, req)
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.Empty(t, rec.Header().Get("X-Pax-User-ID"))
	}
}
