package manager

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSecretaryRunHTTPGivenIdempotentRequestThenReturnsStableAcceptedReceipt(t *testing.T) {
	srv, _ := testServer(t, "secretary-run@example.test")
	fixture := testNodeAgent(t, srv, "secretary-run@example.test")
	handler := srv.routes()
	request := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("X-User-Email", fixture.userEmail)
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response
	}
	configured := request(
		http.MethodPatch,
		"/api/v1/user/self/secretary",
		`{"agent_id":"`+fixture.agentID+`","enabled":true,"expected_revision":0}`,
	)
	require.Equal(t, http.StatusOK, configured.Code, configured.Body.String())
	first := request(http.MethodPost, "/api/v1/user/self/secretary/run", `{"request_id":"retry-1"}`)
	require.Equal(t, http.StatusAccepted, first.Code, first.Body.String())
	receipt := decodeData[domain.SecretaryRunReceipt](t, first.Body.Bytes())
	assert.Equal(t, "retry-1", receipt.RequestID)
	assert.Equal(t, "accepted", receipt.State)
	assert.False(t, receipt.AcceptedAt.IsZero())
	assert.NotContains(t, first.Body.String(), "pending_count")
	disabled := request(
		http.MethodPatch,
		"/api/v1/user/self/secretary",
		`{"agent_id":"","enabled":false,"expected_revision":1}`,
	)
	require.Equal(t, http.StatusOK, disabled.Code, disabled.Body.String())
	replayed := request(
		http.MethodPost,
		"/api/v1/user/self/secretary/run",
		`{"request_id":"retry-1"}`,
	)
	require.Equal(t, http.StatusAccepted, replayed.Code, replayed.Body.String())
	assert.Equal(t, receipt, decodeData[domain.SecretaryRunReceipt](t, replayed.Body.Bytes()))
	newRequest := request(
		http.MethodPost,
		"/api/v1/user/self/secretary/run",
		`{"request_id":"retry-2"}`,
	)
	assert.Equal(t, http.StatusConflict, newRequest.Code, newRequest.Body.String())
}

func TestSecretaryRunHTTPGivenMalformedRequestThenRejectsBeforeEnqueue(t *testing.T) {
	srv, _ := testServer(t, "secretary-run-invalid@example.test")
	for _, body := range []string{`{`, `null`, `[]`, `{"owner_user_id":"victim"}`, `{"request_id":null}`, `{"request_id":123}`, `{"request_id":""}`, `{"request_id":"has space"}`, `{} {}`, `{"request_id":"` + strings.Repeat("x", 129) + `"}`} {
		t.Run(body, func(t *testing.T) {
			req := httptest.NewRequest(
				http.MethodPost,
				"/api/v1/user/self/secretary/run",
				strings.NewReader(body),
			)
			req.Header.Set("X-User-Email", "secretary-run-invalid@example.test")
			response := httptest.NewRecorder()
			srv.routes().ServeHTTP(response, req)
			assert.Equal(t, http.StatusBadRequest, response.Code, response.Body.String())
		})
	}
	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/user/self/secretary/run",
		strings.NewReader(strings.Repeat(" ", 1025)+`{}`),
	)
	req.Header.Set("X-User-Email", "secretary-run-invalid@example.test")
	response := httptest.NewRecorder()
	srv.routes().ServeHTTP(response, req)
	// Whitespace is ignored, but the bounded ID decoder still cannot queue a
	// request for an owner whose Secretary is disabled.
	assert.Equal(t, http.StatusConflict, response.Code)
	req = httptest.NewRequest(
		http.MethodPost,
		"/api/v1/user/self/secretary/run",
		strings.NewReader(`{"request_id":"`+strings.Repeat("x", 1025)+`"}`),
	)
	req.Header.Set("X-User-Email", "secretary-run-invalid@example.test")
	response = httptest.NewRecorder()
	srv.routes().ServeHTTP(response, req)
	assert.Equal(t, http.StatusRequestEntityTooLarge, response.Code)
}
