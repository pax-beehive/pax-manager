package userapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
)

// NodeMobileControl forwards only fixed operations after resolving node ownership.
// The daemon owns runtime credentials, session validation and local file access.
func (s *Service) NodeMobileControl(
	c context.Context,
	meta auth.RequestMetadata,
	nodeID, operation string,
	payload json.RawMessage,
) (int, any, error) {
	if len(payload) > 16384 {
		return 0, nil, apperr.Error{
			Status: http.StatusBadRequest, Message: "mobile payload is too large",
		}
	}
	switch operation {
	case "state", "templates", "create", "start", "stop", "destroy",
		"takeover", "release", "screenshot", "tap", "swipe", "text", "key",
		"recording_start", "recording_stop", "recording_list", "recording_read":
	default:
		return 0, nil, apperr.Error{
			Status: http.StatusBadRequest, Message: "unsupported mobile operation",
		}
	}
	if len(payload) == 0 {
		payload = json.RawMessage(`{}`)
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(payload, &object) != nil || object == nil {
		return 0, nil, apperr.Error{
			Status: http.StatusBadRequest, Message: "mobile payload must be a JSON object",
		}
	}
	return s.queryNodeDaemon(c, meta, nodeID, map[string]any{
		"type": "mobile.control",
		"mobile_control": map[string]any{
			"operation": operation, "payload": payload,
		},
	})
}
