package manager

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSessionRuntimeResetRejectsInvalidHTTPRequests(t *testing.T) {
	tests := []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
	}{
		{
			name:   "Given a non-POST request then it is method not allowed",
			method: http.MethodGet, path: "/api/v1/user/self/agents/agent_1/sessions/sess_1/runtime/reset",
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name:   "Given missing route identities then it is rejected",
			method: http.MethodPost, path: "/api/v1/user/self/agents//sessions//runtime/reset", body: `{}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:   "Given malformed JSON then it is rejected",
			method: http.MethodPost, path: "/api/v1/user/self/agents/agent_1/sessions/sess_1/runtime/reset", body: `{`,
			wantStatus: http.StatusBadRequest,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))

			new(Service).handleSessionRuntimeReset(recorder, request)

			assert.Equal(t, test.wantStatus, recorder.Code)
		})
	}
}
