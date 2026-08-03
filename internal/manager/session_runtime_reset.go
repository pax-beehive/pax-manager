package manager

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/logging"
)

type sessionRuntimeResetRequest struct {
	ExpectedTurnInstanceID string `json:"expected_turn_instance_id"`
}

func (s *Service) handleSessionRuntimeReset(w http.ResponseWriter, r *http.Request) {
	ctx, logID := httpRequestLogContext(r.Context(), r)
	w.Header().Set(logging.HeaderLogID, logID)
	r = r.WithContext(ctx)
	if r.Method != http.MethodPost {
		writeHTTPError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	_, agentID, sessionID := conversationTurnControlRouteIDs(r.URL.Path)
	if agentID == "" || sessionID == "" {
		writeHTTPEndpointError(w, apperr.Error{
			Status: http.StatusBadRequest, Message: "agent_id and session_id are required",
		})
		return
	}
	var input sessionRuntimeResetRequest
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeHTTPError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	input.ExpectedTurnInstanceID = strings.TrimSpace(input.ExpectedTurnInstanceID)
	status, data, err := s.userapi.ResetSessionRuntime(
		r.Context(),
		httpRequestMetadata(r),
		agentID,
		sessionID,
		input.ExpectedTurnInstanceID,
	)
	if err != nil {
		writeHTTPEndpointError(w, err)
		return
	}
	writeHTTPData(w, status, data)
}
