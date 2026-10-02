//go:build integration

package integration_test

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNodeIdentityGivenWrongHintThenReturnsOwnerAndRejectsRevokedKey(t *testing.T) {
	fixture := newIntegrationFixture(t)
	fixture.waitForHealth(t)
	owner := getJSON[struct {
		User struct {
			UserID string `json:"user_id"`
		} `json:"user"`
	}](t, fixture, "/api/v1/user/self/me", fixture.userHeaders(), http.StatusOK)
	require.NotEmpty(t, owner.User.UserID)
	token := postJSON[registrationTokenResponse](
		t, fixture, "/api/v1/user/self/node-registration-tokens",
		map[string]any{"expires_in_seconds": 600}, fixture.userHeaders(), http.StatusOK,
	)
	node := postJSON[registerNodeResponse](
		t, fixture, "/api/v1/node/register",
		map[string]any{"hostname": "routing-recovery-test"},
		map[string]string{"X-Registration-Token": token.Token}, http.StatusOK,
	)
	require.NotEmpty(t, node.APIKey)
	header := map[string]string{
		"X-Pax-Key": node.APIKey, "X-Pax-User-ID": "usr_another_user",
		"X-User-Email": "not-the-owner@example.com",
	}
	identity := getJSON[map[string]string](
		t, fixture, "/api/v1/node/identity", header, http.StatusOK,
	)
	assert.Equal(t, node.NodeID, identity["node_id"])
	assert.Equal(t, owner.User.UserID, identity["user_id"])

	req := fixture.newRequest(t, http.MethodDelete, "/api/v1/user/self/nodes/"+node.NodeID, nil)
	addHeaders(req, fixture.userHeaders())
	fixture.do(t, req, http.StatusOK)
	fixture.getRaw(t, "/api/v1/node/identity", http.StatusNotFound, header)
}
