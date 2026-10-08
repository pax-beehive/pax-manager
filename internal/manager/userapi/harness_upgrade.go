package userapi

import (
	"context"
	"net/http"
	"regexp"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

var harnessTargetVersion = regexp.MustCompile(
	`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$`,
)

func (s *Service) UpgradeNodeHarness(
	ctx context.Context,
	meta auth.RequestMetadata,
	req domain.UpgradeNodeHarnessRequest,
) (int, any, error) {
	principal, err := s.principal.Principal(ctx, meta)
	if err != nil {
		return 0, nil, err
	}
	req.NodeID = strings.TrimSpace(req.NodeID)
	req.CommandID = strings.TrimSpace(req.CommandID)
	req.Version = strings.TrimSpace(req.Version)
	if req.NodeID == "" || req.CommandID == "" || !harnessTargetVersion.MatchString(req.Version) {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "node_id, command_id and explicit semantic version are required",
		}
	}
	if (req.Harness != "claude-code" && req.Harness != "codex" && req.Harness != "pi") ||
		(req.Component != "acp" && req.Component != "cli") ||
		((req.Component == "acp") != (req.ConnectionID != "")) {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "select Claude, Codex or Pi, cli or acp, and a connection_id only for acp",
		}
	}
	node, err := s.store.GetNode(ctx, principal, req.NodeID)
	if err != nil {
		return 0, nil, err
	}
	if s.nodeControl == nil {
		return 0, nil, nodeControlUnavailableError()
	}
	remoteID, err := s.nodeControl.RemoteID(node.NodeID)
	if err != nil {
		return 0, nil, nodeControlUnavailableError()
	}
	return s.dispatchNodeDaemonCommand(ctx, node.NodeID, req.CommandID, map[string]any{
		"command_id": req.CommandID, "type": "harness.upgrade", "upgrade_harness": map[string]any{"harness": req.Harness, "component": req.Component, "version": req.Version, "connection_id": req.ConnectionID},
	}, map[string]any{"command_id": req.CommandID, "remote_id": remoteID, "dispatch_status": "unknown", "expected_version": req.Version})
}
