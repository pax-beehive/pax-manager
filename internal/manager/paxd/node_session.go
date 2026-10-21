package paxd

import (
	"context"
	"net/http"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type NodeSessionRegistrar interface {
	RegisterNodeSession(
		context.Context,
		domain.Node,
		string,
		*domain.RegisterNodeSessionRequest,
	) (domain.AgentSession, error)
}

func (s *Service) RegisterNodeSession(
	ctx context.Context,
	node domain.Node,
	agentID string,
	req *domain.RegisterNodeSessionRequest,
) (int, any, error) {
	if req == nil || agentID == "" || req.Origin.Kind != "cronjob" || !validNodeSessionFields(req) {
		return 0, nil, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "native_id, absolute cwd and complete cronjob origin are required",
		}
	}
	store, ok := s.store.(NodeSessionRegistrar)
	if !ok {
		return 0, nil, apperr.Error{
			Status:  http.StatusServiceUnavailable,
			Message: "session registration is unavailable",
		}
	}
	session, err := store.RegisterNodeSession(ctx, node, agentID, req)
	if err != nil {
		return 0, nil, err
	}
	return http.StatusOK, session, nil
}

func validNodeSessionFields(req *domain.RegisterNodeSessionRequest) bool {
	for _, value := range []string{req.NativeID, req.Origin.RegistrationID, req.Origin.CronjobID, req.Origin.RunID, req.Origin.Name} {
		if strings.TrimSpace(value) == "" || len(value) > 512 ||
			strings.ContainsAny(value, "\x00\r\n") {
			return false
		}
	}
	// Paths belong to the node; never stat or resolve them on the Manager host.
	return strings.HasPrefix(req.CWD, "/") && len(req.CWD) <= 4096 &&
		!strings.ContainsRune(req.CWD, 0)
}
