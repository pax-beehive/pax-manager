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

var paxlTargetVersion = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+$`)

func (s *Service) UpgradeNodePaxl(
	ctx context.Context,
	meta auth.RequestMetadata,
	req domain.UpgradeNodePaxlRequest,
) (int, any, error) {
	principal, err := s.principal.Principal(ctx, meta)
	if err != nil {
		return 0, nil, err
	}
	req.NodeID = strings.TrimSpace(req.NodeID)
	req.CommandID = strings.TrimSpace(req.CommandID)
	req.Version = strings.TrimSpace(req.Version)
	if req.NodeID == "" || req.CommandID == "" || !paxlTargetVersion.MatchString(req.Version) {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "node_id, command_id and explicit semantic version are required",
		}
	}
	if req.Tag == "" {
		req.Tag = "stable"
	}
	if req.Tag != "stable" && req.Tag != "latest" {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "tag must be stable or latest",
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
		"command_id": req.CommandID, "type": "paxl.upgrade", "upgrade_paxl": map[string]any{"version": req.Version, "tag": req.Tag},
	}, map[string]any{"command_id": req.CommandID, "remote_id": remoteID, "dispatch_status": "unknown", "expected_version": req.Version})
}
