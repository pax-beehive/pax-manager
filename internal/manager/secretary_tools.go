package manager

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
)

type secretaryToolRequest struct {
	SessionID     string `json:"session_id"`
	Capability    string `json:"capability"`
	Tool          string `json:"tool"`
	ArgumentsJSON string `json:"arguments_json"`
}

type secretaryToolGrant struct {
	ctx       context.Context
	job       domain.SecretaryJob
	agent     domain.Agent
	sessionID string
	mu        sync.Mutex
	reads     map[string]userapi.WorkspaceMessageRead
	summaries map[domain.SummaryRef]bool
	readBytes int
	closed    bool
}

type secretaryToolPrincipal struct{ owner string }

func (p secretaryToolPrincipal) Principal(
	context.Context,
	auth.RequestMetadata,
) (domain.UserPrincipal, error) {
	return domain.UserPrincipal{User: domain.User{UserID: p.owner}}, nil
}

func (s *Service) registerSecretaryTools(
	ctx context.Context, job domain.SecretaryJob, agent domain.Agent, sessionID string,
) (string, func(), error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	if job.OwnerUserID == "" || job.AgentID != agent.AgentID ||
		agent.OwnerUserID != job.OwnerUserID ||
		agent.NodeID == "" ||
		!strings.HasPrefix(sessionID, domain.SecretaryInternalSessionPrefix) ||
		s.workspaceSearch == nil {
		return "", nil, domain.ErrSecretaryFenced
	}
	grant := &secretaryToolGrant{ctx: ctx, job: job, agent: agent, sessionID: sessionID,
		reads: make(
			map[string]userapi.WorkspaceMessageRead,
		), summaries: make(map[domain.SummaryRef]bool)}
	if err := s.secretaryToolActive(ctx, grant); err != nil {
		return "", nil, err
	}
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", nil, err
	}
	capability := base64.RawURLEncoding.EncodeToString(secret)
	grantContext, revoke := context.WithCancel(ctx)
	grant.ctx = grantContext
	s.secretaryTools.Store(capability, grant)
	cleanup := func() {
		grant.mu.Lock()
		grant.closed = true
		grant.mu.Unlock()
		revoke()
		s.secretaryTools.Delete(capability)
	}
	return capability, cleanup, nil
}

func (s *Service) secretaryToolActive(ctx context.Context, grant *secretaryToolGrant) error {
	if err := grant.ctx.Err(); err != nil {
		return domain.ErrSecretaryFenced
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	grant.mu.Lock()
	closed := grant.closed
	grant.mu.Unlock()
	if closed || s.secretaryStore == nil {
		return domain.ErrSecretaryFenced
	}
	active, err := s.secretaryStore.SecretaryJobActive(ctx, grant.job)
	if err != nil {
		return err
	}
	if !active {
		return domain.ErrSecretaryFenced
	}
	return nil
}

func CallSecretaryTool(ctx context.Context, c *app.RequestContext) {
	var request secretaryToolRequest
	if err := decodeSecretaryToolJSON(string(c.Request.Body()), &request); err != nil {
		writeEndpointError(c, err)
		return
	}
	result, err := serviceFromContext(
		c,
	).callSecretaryTool(ctx, nodeFromContext(c), c.Param("agent_id"), request)
	writeEndpointResult(c, http.StatusOK, result, err)
}

func (s *Service) callSecretaryTool(
	ctx context.Context, node domain.Node, agentID string, request secretaryToolRequest,
) (any, error) {
	value, ok := s.secretaryTools.Load(request.Capability)
	if !ok {
		return nil, domain.ErrUnauthorized
	}
	grant := value.(*secretaryToolGrant)
	if grant.sessionID != request.SessionID || grant.agent.AgentID != agentID ||
		grant.agent.NodeID != node.NodeID {
		return nil, domain.ErrUnauthorized
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(grant.ctx, cancel)
	defer stop()
	defer cancel()
	agent, err := s.store.GetNodeAgent(ctx, node.NodeID, agentID)
	if grant.ctx.Err() != nil {
		return nil, domain.ErrSecretaryFenced
	}
	if err != nil || agent.OwnerUserID != grant.job.OwnerUserID {
		return nil, domain.ErrUnauthorized
	}
	if err = s.secretaryToolActive(ctx, grant); err != nil {
		return nil, err
	}
	result, err := s.runSecretaryTool(ctx, grant, request)
	if err != nil {
		return nil, err
	}
	if err = s.secretaryToolActive(ctx, grant); err != nil {
		return nil, err
	}
	if err = grant.record(result); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *Service) runSecretaryTool(
	ctx context.Context,
	grant *secretaryToolGrant,
	request secretaryToolRequest,
) (any, error) {
	search := s.workspaceSearch.WithPrincipal(secretaryToolPrincipal{owner: grant.job.OwnerUserID})
	switch request.Tool {
	case "search":
		var query domain.SearchQuery
		if err := decodeSecretaryToolJSON(request.ArgumentsJSON, &query); err != nil {
			return nil, err
		}
		return search.Search(ctx, auth.RequestMetadata{}, query)
	case "read":
		var read userapi.WorkspaceReadRequest
		if err := decodeSecretaryToolJSON(request.ArgumentsJSON, &read); err != nil {
			return nil, err
		}
		return search.Read(ctx, auth.RequestMetadata{}, read)
	case "history":
		return s.secretaryToolHistory(ctx, grant, search, request.ArgumentsJSON)
	default:
		return nil, apperr.Error{Status: http.StatusBadRequest, Message: "unknown secretary tool"}
	}
}

func decodeSecretaryToolJSON(text string, target any) error {
	if len(text) > 64*1024 {
		return apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "secretary tool arguments exceed limit",
		}
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "invalid secretary tool arguments",
		}
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "invalid secretary tool arguments",
		}
	}
	return nil
}
