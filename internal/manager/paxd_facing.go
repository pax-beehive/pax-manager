package manager

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/auth"
)

func (s *Service) handleAgentRegister(
	c context.Context,
	ctx *app.RequestContext,
	req RegisterAgentRequest,
) {
	status, data, err := s.paxd.RegisterAgent(c, requestMetadata(ctx), req)
	writeEndpointResult(ctx, status, data, err)
}

func requestMetadata(ctx *app.RequestContext) auth.RequestMetadata {
	headers := map[string]string{}
	ctx.Request.Header.VisitAll(func(k, v []byte) {
		headers[string(k)] = string(v)
	})
	return auth.NewRequestMetadata(headers)
}

func (s *Service) handleAgentStatus(
	c context.Context,
	ctx *app.RequestContext,
	agent Agent,
	report AgentStatusReport,
) {
	status, data, err := s.paxd.ReportStatus(c, agent, report)
	writeEndpointResult(ctx, status, data, err)
}

func (s *Service) handleAgentMailbox(
	c context.Context,
	ctx *app.RequestContext,
	agent Agent,
	offset int64,
	limit int,
) {
	status, data, err := s.paxd.PullMailbox(c, agent, offset, limit)
	writeEndpointResult(ctx, status, data, err)
}

func (s *Service) handleAgentSessionMailbox(
	c context.Context,
	ctx *app.RequestContext,
	agent Agent,
	sessionID string,
	offset int64,
	limit int,
) {
	status, data, err := s.paxd.PullSessionMailbox(c, agent, sessionID, offset, limit)
	writeEndpointResult(ctx, status, data, err)
}

func (s *Service) handleAgentOffset(
	c context.Context,
	ctx *app.RequestContext,
	agent Agent,
	offset int64,
) {
	status, data, err := s.paxd.UpdateOffset(c, agent, offset)
	writeEndpointResult(ctx, status, data, err)
}

func (s *Service) handleAgentMessageResult(
	c context.Context,
	ctx *app.RequestContext,
	agent Agent,
	req MessageResultRequest,
) {
	status, data, err := s.paxd.ReportMessageResult(c, agent, req)
	writeEndpointResult(ctx, status, data, err)
}
