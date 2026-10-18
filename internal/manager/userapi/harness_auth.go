package userapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func knownAuthHarness(h string) bool {
	switch h {
	case "claude",
		"codex",
		"pi",
		"kimi",
		"gemini",
		"dsh",
		"dscode",
		"kiro",
		"hermes",
		"openclaw",
		"opencode",
		"antigravity":
		return true
	}
	return false
}
func validAuthField(value string) bool {
	return len(value) <= 128 && !strings.HasPrefix(value, "-") &&
		!strings.ContainsAny(value, "\r\n\x00") &&
		strings.TrimSpace(value) == value
}
func authBadRequest() error {
	return apperr.Error{
		Status:  http.StatusBadRequest,
		Message: "invalid harness authentication request",
	}
}

// Authentication commands bypass maintenance tracking and durable command storage.
// The verified principal supplies actor_id; it cannot be selected by the browser.
func (s *Service) NodeHarnessAuth(
	ctx context.Context,
	meta auth.RequestMetadata,
	req domain.NodeHarnessAuthRequest,
) (int, any, error) {
	principal, err := s.principal.Principal(ctx, meta)
	if err != nil {
		return 0, nil, err
	}
	if !knownAuthHarness(req.Harness) || req.CommandID == "" || !validAuthField(req.CommandID) ||
		!validAuthField(req.SessionID) ||
		!validAuthField(req.PromptID) ||
		len(req.Code) > 4096 ||
		strings.ContainsAny(req.Code, "\r\n\x00") {
		return 0, nil, authBadRequest()
	}
	switch req.Operation {
	case "start", "submit", "cancel":
	default:
		return 0, nil, authBadRequest()
	}
	node, err := s.store.GetNode(ctx, principal, req.NodeID)
	if err != nil {
		return 0, nil, err
	}
	if s.nodeControl == nil {
		return 0, nil, nodeControlUnavailableError()
	}
	if req.Operation == "start" {
		result, err := s.queryNodeControl(
			ctx,
			node.NodeID,
			map[string]any{
				"type": "harness_auth.status",
				"harness_auth_status": map[string]any{
					"harness":   req.Harness,
					"operation": "capabilities",
					"actor_id":  principal.User.UserID,
				},
			},
		)
		if err != nil {
			return 0, nil, err
		}
		var metadata struct {
			HarnessAuth *struct {
				ProtocolVersion int `json:"protocol_version"`
			} `json:"harness_auth"`
		}
		if json.Unmarshal(result, &metadata) != nil || metadata.HarnessAuth == nil ||
			metadata.HarnessAuth.ProtocolVersion != 1 {
			return 0, nil, apperr.Error{
				Status:  http.StatusConflict,
				Message: "Update this node's paxd and paxl before using Console harness authentication.",
			}
		}
	}
	command := map[string]any{
		"command_id": req.CommandID,
		"type":       "harness_auth.login",
		"harness_auth_login": map[string]any{
			"harness":      req.Harness,
			"operation":    req.Operation,
			"action":       req.Action,
			"provider":     req.Provider,
			"profile":      req.Profile,
			"method":       req.Method,
			"secret_stdin": req.SecretStdin,
			"session_id":   req.SessionID,
			"prompt_id":    req.PromptID,
			"code":         req.Code,
			"actor_id":     principal.User.UserID,
		},
	}
	ack, err := s.nodeControl.Command(ctx, node.NodeID, req.CommandID, command)
	if err != nil {
		return 0, nil, apperr.Error{
			Status:  http.StatusServiceUnavailable,
			Message: "authentication acknowledgement unavailable; recover by the start command id",
		}
	}
	return http.StatusAccepted, map[string]any{"command_ack": ack}, nil
}

func (s *Service) GetNodeHarnessAuth(
	ctx context.Context,
	meta auth.RequestMetadata,
	req domain.GetNodeHarnessAuthRequest,
) (int, any, error) {
	principal, err := s.principal.Principal(ctx, meta)
	if err != nil {
		return 0, nil, err
	}
	if !knownAuthHarness(req.Harness) || !validAuthField(req.SessionID) ||
		!validAuthField(req.CommandID) ||
		!validAuthField(req.Provider) {
		return 0, nil, authBadRequest()
	}
	if req.Operation != "" && req.Operation != "status" && req.Operation != "methods" {
		return 0, nil, authBadRequest()
	}
	node, err := s.store.GetNode(ctx, principal, req.NodeID)
	if err != nil {
		return 0, nil, err
	}
	result, err := s.queryNodeControl(
		ctx,
		node.NodeID,
		map[string]any{
			"type": "harness_auth.status",
			"harness_auth_status": map[string]any{
				"harness":    req.Harness,
				"session_id": req.SessionID,
				"command_id": req.CommandID,
				"provider":   req.Provider,
				"operation":  req.Operation,
				"actor_id":   principal.User.UserID,
			},
		},
	)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, result, nil
}
