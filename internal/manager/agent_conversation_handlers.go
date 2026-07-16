package manager

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/logging"
)

const (
	conversationDeliveryQueuedStatus = "queued"
	conversationDeliveryRetryEvery   = 100 * time.Millisecond
	conversationDeliveryRetryFor     = 30 * time.Second
)

type agentConversationDelivery struct {
	Status string `json:"status"`
	Error  string `json:"error,omitempty"`
}

func StartAgentConversation(c context.Context, ctx *app.RequestContext) {
	var req domain.StartAgentConversationRequest
	decodeBody(ctx, &req)
	req.FromRuntimeAgentID = ctx.Param("agent_id")
	req.Input = strings.TrimSpace(req.Input)
	if req.FromRuntimeAgentID == "" || req.ToRepresentativeAgentID == "" || req.Input == "" {
		writeError(
			ctx,
			http.StatusBadRequest,
			"agent_id, to_representative_agent_id, and input are required",
		)
		return
	}
	service := serviceFromContext(ctx)
	start, err := service.store.StartAgentConversation(c, nodeFromContext(ctx), req)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	delivery := agentConversationDelivery{Status: "delivered"}
	status := http.StatusOK
	if err := service.deliverAgentConversation(c, start); err != nil {
		status = http.StatusAccepted
		delivery.Status = "pending"
		delivery.Error = err.Error()
	}
	writeData(ctx, status, map[string]any{
		"conversation": start.Conversation,
		"bindings": []domain.ConversationAgentBinding{
			start.SourceBinding,
			start.TargetBinding,
		},
		"invocation":     start.Invocation,
		"source_session": start.SourceSession,
		"target_session": start.TargetSession,
		"prompt_message": start.PromptMessage,
		"delivery":       delivery,
	})
}

func DeliverAgentConversation(c context.Context, ctx *app.RequestContext) {
	var req domain.DeliverConversationRequest
	if err := json.Unmarshal(ctx.Request.Body(), &req); err != nil {
		writeError(ctx, http.StatusBadRequest, "invalid JSON body")
		return
	}
	normalizeDeliverConversationRequest(&req)
	if err := validateDeliverConversationRequest(req); err != nil {
		writeError(ctx, http.StatusBadRequest, err.Error())
		return
	}
	node := nodeFromContext(ctx)
	delivery, err := serviceFromContext(ctx).store.DeliverAgentConversation(c, node, req)
	deliveryError := ""
	if err == nil {
		service := serviceFromContext(ctx)
		if deliveryErr := service.deliverConversationDelivery(c, delivery); deliveryErr != nil {
			if isAgentTunnelAlreadyInUse(deliveryErr) {
				delivery.DeliveryStatus = conversationDeliveryQueuedStatus
				service.enqueueConversationDelivery(c, delivery)
				logging.Info(
					c,
					"conversation delivery queued",
					slog.String("target_kind", req.Target.Kind),
					slog.String("conversation_id", delivery.Conversation.ConversationID),
					slog.String("invocation_id", delivery.Invocation.InvocationID),
					slog.String("target_agent_id", delivery.TargetSession.AgentID),
					slog.String("target_session_id", delivery.TargetSession.SessionID),
					slog.String("target_native_session_id", delivery.TargetSession.NativeID),
					logging.Err(deliveryErr),
				)
			} else {
				delivery.DeliveryStatus = "pending"
				deliveryError = deliveryErr.Error()
				logging.Warn(
					c,
					"conversation delivery prompt failed",
					slog.String("target_kind", req.Target.Kind),
					slog.String("conversation_id", delivery.Conversation.ConversationID),
					slog.String("invocation_id", delivery.Invocation.InvocationID),
					slog.String("target_agent_id", delivery.TargetSession.AgentID),
					slog.String("target_session_id", delivery.TargetSession.SessionID),
					slog.String("target_native_session_id", delivery.TargetSession.NativeID),
					logging.Err(deliveryErr),
				)
			}
			err = nil
		} else {
			delivery.DeliveryStatus = "delivered"
			logging.Info(
				c,
				"conversation delivery prompt delivered",
				slog.String("target_kind", req.Target.Kind),
				slog.String("conversation_id", delivery.Conversation.ConversationID),
				slog.String("invocation_id", delivery.Invocation.InvocationID),
				slog.String("target_agent_id", delivery.TargetSession.AgentID),
				slog.String("target_session_id", delivery.TargetSession.SessionID),
				slog.String("target_native_session_id", delivery.TargetSession.NativeID),
			)
		}
	}
	data := map[string]any{
		"contract_version":  "conversation_delivery.v1",
		"delivery_endpoint": routeDeliverAgentConversation,
		"delivery":          delivery,
	}
	if delivery.ReceiptToken != "" {
		data["receipt_token"] = delivery.ReceiptToken
	}
	if deliveryError != "" {
		data["delivery_error"] = deliveryError
	}
	writeEndpointResult(ctx, http.StatusAccepted, data, err)
}

func (s *Service) enqueueConversationDelivery(
	ctx context.Context,
	delivery domain.ConversationDelivery,
) {
	if s.backgroundRunner == nil {
		s.backgroundRunner = func(ctx context.Context, task func(context.Context)) {
			go task(context.WithoutCancel(ctx))
		}
	}
	s.backgroundRunner(ctx, func(taskCtx context.Context) {
		s.retryQueuedConversationDelivery(taskCtx, delivery)
	})
}

func (s *Service) retryQueuedConversationDelivery(
	ctx context.Context,
	delivery domain.ConversationDelivery,
) {
	deadline := time.NewTimer(conversationDeliveryRetryFor)
	defer deadline.Stop()
	ticker := time.NewTicker(conversationDeliveryRetryEvery)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			logging.Warn(
				ctx,
				"conversation delivery queued retry canceled",
				conversationDeliveryAttrs(delivery)...,
			)
			return
		case <-deadline.C:
			logging.Warn(
				ctx,
				"conversation delivery queued retry expired",
				conversationDeliveryAttrs(delivery)...,
			)
			return
		case <-ticker.C:
			err := s.deliverConversationDelivery(ctx, delivery)
			if err == nil {
				logging.Info(
					ctx,
					"conversation delivery queued retry delivered",
					conversationDeliveryAttrs(delivery)...,
				)
				return
			}
			if isAgentTunnelAlreadyInUse(err) {
				continue
			}
			logging.Warn(
				ctx,
				"conversation delivery queued retry failed",
				append(conversationDeliveryAttrs(delivery), logging.Err(err))...,
			)
			return
		}
	}
}

func conversationDeliveryAttrs(delivery domain.ConversationDelivery) []slog.Attr {
	return []slog.Attr{
		slog.String("conversation_id", delivery.Conversation.ConversationID),
		slog.String("invocation_id", delivery.Invocation.InvocationID),
		slog.String("target_agent_id", delivery.TargetSession.AgentID),
		slog.String("target_session_id", delivery.TargetSession.SessionID),
		slog.String("target_native_session_id", delivery.TargetSession.NativeID),
		slog.String("source_agent_id", delivery.SourceSession.AgentID),
		slog.String("source_session_id", delivery.SourceSession.SessionID),
	}
}

func ListRepresentativeAgents(c context.Context, ctx *app.RequestContext) {
	service := serviceFromContext(ctx)
	principal, err := service.auth.Principal(c, requestMetadata(ctx))
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	agents, err := service.store.ListRepresentativeAgents(
		c,
		principal,
		strings.TrimSpace(string(ctx.Query("runtime_agent_id"))),
	)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	writeData(ctx, http.StatusOK, map[string]any{"representative_agents": agents})
}

func GetAgentOwnerInfo(c context.Context, ctx *app.RequestContext) {
	service := serviceFromContext(ctx)
	principal, err := service.auth.Principal(c, requestMetadata(ctx))
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	req := domain.AgentOwnerInfoRequest{
		AgentID:               strings.TrimSpace(string(ctx.Query("agent_id"))),
		RepresentativeAgentID: strings.TrimSpace(string(ctx.Query("representative_agent_id"))),
	}
	if req.AgentID == "" && req.RepresentativeAgentID == "" {
		writeError(ctx, http.StatusBadRequest, "agent_id or representative_agent_id is required")
		return
	}
	info, err := service.store.GetAgentOwnerInfo(c, principal, req)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	writeData(ctx, http.StatusOK, map[string]any{"owner_info": info})
}

func normalizeDeliverConversationRequest(req *domain.DeliverConversationRequest) {
	req.Source.AgentID = strings.TrimSpace(req.Source.AgentID)
	req.Source.RepresentativeAgentID = strings.TrimSpace(req.Source.RepresentativeAgentID)
	req.Source.SessionID = strings.TrimSpace(req.Source.SessionID)
	req.Target.Kind = strings.TrimSpace(req.Target.Kind)
	req.Target.RepresentativeAgentID = strings.TrimSpace(req.Target.RepresentativeAgentID)
	req.Target.SessionID = strings.TrimSpace(req.Target.SessionID)
	req.Target.InvocationID = strings.TrimSpace(req.Target.InvocationID)
	req.Instruction = strings.TrimSpace(req.Instruction)
	req.Reason = strings.TrimSpace(req.Reason)
}

func validateDeliverConversationRequest(req domain.DeliverConversationRequest) error {
	switch req.Target.Kind {
	case domain.ConversationDeliveryTargetRepresentative:
		if req.Source.AgentID == "" || req.Source.SessionID == "" {
			return errBadConversationDelivery("source.agent_id and source.session_id are required for representative delivery")
		}
		if req.Target.RepresentativeAgentID == "" {
			return errBadConversationDelivery("target.representative_agent_id is required for representative delivery")
		}
		return nil
	case domain.ConversationDeliveryTargetActiveInvocation:
		if req.Source.AgentID == "" || req.Source.SessionID == "" {
			return errBadConversationDelivery("source.agent_id and source.session_id are required for active invocation delivery")
		}
		return nil
	default:
		return errBadConversationDelivery("target.kind must be representative or active_invocation")
	}
}

type errBadConversationDelivery string

func (e errBadConversationDelivery) Error() string {
	return string(e)
}

func UpsertRepresentativeAgent(c context.Context, ctx *app.RequestContext) {
	var req domain.UpsertRepresentativeAgentRequest
	decodeBody(ctx, &req)
	req.RuntimeAgentID = strings.TrimSpace(req.RuntimeAgentID)
	if req.RuntimeAgentID == "" {
		writeError(ctx, http.StatusBadRequest, "runtime_agent_id is required")
		return
	}
	service := serviceFromContext(ctx)
	principal, err := service.auth.Principal(c, requestMetadata(ctx))
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	rep, profile, err := service.store.UpsertRepresentativeAgent(c, principal, req)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	writeData(ctx, http.StatusOK, map[string]any{
		"representative_agent": rep,
		"profile":              profile,
	})
}

func StartUserAgentInquiry(c context.Context, ctx *app.RequestContext) {
	var req domain.StartAgentConversationRequest
	decodeBody(ctx, &req)
	req.FromRuntimeAgentID = ctx.Param("agent_id")
	req.Input = strings.TrimSpace(req.Input)
	if req.FromRuntimeAgentID == "" || req.ToRepresentativeAgentID == "" || req.Input == "" {
		writeError(
			ctx,
			http.StatusBadRequest,
			"agent_id, to_representative_agent_id, and input are required",
		)
		return
	}
	service := serviceFromContext(ctx)
	principal, err := service.auth.Principal(c, requestMetadata(ctx))
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	start, err := service.store.StartAgentConversationForUser(c, principal, req)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	delivery := agentConversationDelivery{Status: "delivered"}
	status := http.StatusOK
	if err := service.deliverAgentConversation(c, start); err != nil {
		status = http.StatusAccepted
		delivery.Status = "pending"
		delivery.Error = err.Error()
	}
	writeData(ctx, status, map[string]any{
		"conversation": start.Conversation,
		"bindings": []domain.ConversationAgentBinding{
			start.SourceBinding,
			start.TargetBinding,
		},
		"invocation":     start.Invocation,
		"source_session": start.SourceSession,
		"target_session": start.TargetSession,
		"prompt_message": start.PromptMessage,
		"delivery":       delivery,
	})
}

func ListAgentConversationMessages(c context.Context, ctx *app.RequestContext) {
	service := serviceFromContext(ctx)
	principal, err := service.auth.Principal(c, requestMetadata(ctx))
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	messages, err := service.store.ListConversationMessages(
		c,
		principal,
		ctx.Param("conversation_id"),
		queryInt(ctx, "limit"),
	)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	if strings.TrimSpace(string(ctx.Query("view"))) != "debug" {
		messages = domain.NormalTranscriptMessages(messages)
	}
	writeData(ctx, http.StatusOK, map[string]any{"messages": messages})
}

func (s *Service) deliverAgentConversation(
	ctx context.Context,
	start domain.AgentConversationStart,
) error {
	sourceRunner, releaseSource, err := s.claimAgentConversationRunner(
		ctx,
		start.SourceRuntimeAgent.AgentID,
		start.SourceSession.SessionID,
		start.SourceSession.NativeID,
	)
	if err != nil {
		return err
	}
	defer releaseSource()

	targetRunner := sourceRunner
	releaseTarget := func() {}
	if start.TargetRuntimeAgent.AgentID != start.SourceRuntimeAgent.AgentID {
		targetRunner, releaseTarget, err = s.claimAgentConversationRunner(
			ctx,
			start.TargetRuntimeAgent.AgentID,
			start.TargetSession.SessionID,
			start.TargetSession.NativeID,
		)
		if err != nil {
			return err
		}
	}
	defer releaseTarget()

	if err := s.initializeAgentConversationSession(
		ctx,
		&sourceRunner,
		start.SourceSession,
	); err != nil {
		return err
	}
	if err := s.initializeAgentConversationSession(
		ctx,
		&targetRunner,
		start.TargetSession,
	); err != nil {
		return err
	}

	turns := start.Invocation.MaxTurns
	if turns <= 0 {
		turns = 1
	}
	prompt := start.PromptMessage.Parts[0].Text
	current := agentConversationTurnRunner{
		runner:    &targetRunner,
		agentID:   start.TargetRuntimeAgent.AgentID,
		sessionID: start.TargetSession.SessionID,
	}
	next := agentConversationTurnRunner{
		runner:    &sourceRunner,
		agentID:   start.SourceRuntimeAgent.AgentID,
		sessionID: start.SourceSession.SessionID,
	}
	for i := 0; i < turns && strings.TrimSpace(prompt) != ""; i++ {
		meta := startAgentConversationTurnMetadata(start, current, next, i, prompt)
		response, err := s.promptAgentConversationTurn(ctx, current, prompt, meta)
		if err != nil {
			return err
		}
		prompt = response
		current, next = next, current
	}
	return s.store.CompleteAgentConversationInvocation(ctx, start.Invocation.InvocationID)
}

func (s *Service) deliverConversationDelivery(
	ctx context.Context,
	delivery domain.ConversationDelivery,
) error {
	targetAgentID := strings.TrimSpace(delivery.TargetSession.AgentID)
	targetSessionID := strings.TrimSpace(delivery.TargetSession.SessionID)
	if targetAgentID == "" || targetSessionID == "" {
		return ErrNotFound
	}
	displayText := ""
	prompt := ""
	if len(delivery.PromptMessage.Parts) > 0 {
		displayText = strings.TrimSpace(delivery.PromptMessage.Parts[0].Text)
		prompt = agentConversationDeliveryPrompt(delivery, displayText)
	}
	if strings.TrimSpace(prompt) == "" {
		return nil
	}
	logging.Info(
		ctx,
		"conversation delivery prompting target session",
		slog.String("conversation_id", delivery.Conversation.ConversationID),
		slog.String("invocation_id", delivery.Invocation.InvocationID),
		slog.String("target_agent_id", targetAgentID),
		slog.String("target_session_id", targetSessionID),
		slog.String("target_native_session_id", delivery.TargetSession.NativeID),
		slog.String("source_agent_id", delivery.SourceSession.AgentID),
		slog.String("source_session_id", delivery.SourceSession.SessionID),
		slog.Int("prompt_chars", len(prompt)),
	)

	runner, release, err := s.claimAgentConversationRunner(
		ctx,
		targetAgentID,
		targetSessionID,
		delivery.TargetSession.NativeID,
	)
	if err != nil {
		logging.Warn(
			ctx,
			"conversation delivery target tunnel claim failed",
			slog.String("conversation_id", delivery.Conversation.ConversationID),
			slog.String("invocation_id", delivery.Invocation.InvocationID),
			slog.String("target_agent_id", targetAgentID),
			slog.String("target_session_id", targetSessionID),
			slog.String("target_native_session_id", delivery.TargetSession.NativeID),
			logging.Err(err),
		)
		return err
	}
	defer release()

	if err := s.initializeAgentConversationSession(ctx, &runner, delivery.TargetSession); err != nil {
		logging.Warn(
			ctx,
			"conversation delivery target session initialize failed",
			slog.String("conversation_id", delivery.Conversation.ConversationID),
			slog.String("invocation_id", delivery.Invocation.InvocationID),
			slog.String("target_agent_id", targetAgentID),
			slog.String("target_session_id", targetSessionID),
			slog.String("target_native_session_id", delivery.TargetSession.NativeID),
			logging.Err(err),
		)
		return err
	}
	_, err = s.promptAgentConversationTurn(ctx, agentConversationTurnRunner{
		runner:    &runner,
		agentID:   targetAgentID,
		sessionID: targetSessionID,
	}, prompt, deliveryConversationTurnMetadata(delivery, displayText, prompt))
	if err != nil {
		logging.Warn(
			ctx,
			"conversation delivery target session prompt failed",
			slog.String("conversation_id", delivery.Conversation.ConversationID),
			slog.String("invocation_id", delivery.Invocation.InvocationID),
			slog.String("target_agent_id", targetAgentID),
			slog.String("target_session_id", targetSessionID),
			slog.String("target_native_session_id", delivery.TargetSession.NativeID),
			logging.Err(err),
		)
		return err
	}
	logging.Info(
		ctx,
		"conversation delivery target session prompt completed",
		slog.String("conversation_id", delivery.Conversation.ConversationID),
		slog.String("invocation_id", delivery.Invocation.InvocationID),
		slog.String("target_agent_id", targetAgentID),
		slog.String("target_session_id", targetSessionID),
		slog.String("target_native_session_id", delivery.TargetSession.NativeID),
	)
	return err
}

func agentConversationDeliveryPrompt(delivery domain.ConversationDelivery, prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return ""
	}
	if delivery.Invocation.Status == domain.ConversationAgentInvocationStatusActive &&
		delivery.Invocation.TargetRuntimeAgentID == delivery.TargetSession.AgentID &&
		delivery.Invocation.TargetSessionID == delivery.TargetSession.SessionID {
		return strings.TrimSpace(`You are receiving a Pax conversation inquiry from another agent.

You must answer this inquiry by calling the pax-conversation reply tool after you have the answer. Do not finish by only writing a normal chat response; the sender will receive your answer only through the reply tool.

Use the MCP tool exposed by the pax-conversation MCP server. Depending on your runtime it may appear as reply, pax-conversation.reply, or an MCP-prefixed reply tool; do not run a shell command or CLI instead.

The reply tool does not automatically include your answer or this context. You must pass your final answer explicitly in the tool input, such as the text/input/content argument or an input file.

Inquiry:
` + prompt)
	}
	if delivery.Invocation.Status == domain.ConversationAgentInvocationStatusCompleted &&
		delivery.Invocation.SourceRuntimeAgentID == delivery.TargetSession.AgentID &&
		delivery.Invocation.SourceSessionID == delivery.TargetSession.SessionID {
		return strings.TrimSpace(`Your Pax conversation inquiry has received a reply from another agent.

This is the reply you asked for. Do not call the pax-conversation reply tool here, and do not try to fetch a reply through that tool; reply is only for the other agent to send an answer back to you.

Reply:
` + prompt)
	}
	return prompt
}

type agentConversationTurnRunner struct {
	runner    *conversationRunner
	agentID   string
	sessionID string
}

func (s *Service) claimAgentConversationRunner(
	ctx context.Context,
	agentID string,
	sessionID string,
	nativeSessionID string,
) (conversationRunner, func(), error) {
	logging.Info(
		ctx,
		"conversation delivery claiming agent tunnel",
		slog.String("agent_id", agentID),
		slog.String("session_id", sessionID),
		slog.String("native_session_id", nativeSessionID),
		slog.String("available_tunnels", s.acpTunnels.debugSnapshot(agentID)),
	)
	agentConn, release, err := s.acpTunnels.borrowAnyWait(
		ctx,
		conversationTunnelClaimTimeout,
		conversationTunnelClaimTick,
		agentID,
		sessionID,
		nativeSessionID,
		"",
	)
	if err != nil {
		logging.Warn(
			ctx,
			"conversation delivery agent tunnel claim failed",
			slog.String("agent_id", agentID),
			slog.String("session_id", sessionID),
			slog.String("native_session_id", nativeSessionID),
			slog.String("available_tunnels", s.acpTunnels.debugSnapshot(agentID)),
			logging.Err(err),
		)
		return conversationRunner{}, func() {}, err
	}
	logging.Info(
		ctx,
		"conversation delivery agent tunnel claimed",
		slog.String("agent_id", agentID),
		slog.String("session_id", sessionID),
		slog.String("native_session_id", nativeSessionID),
		slog.String("claimed_connection_id", agentConn.queueID()),
		slog.String("claimed_session_id", agentConn.currentSessionID()),
	)
	return conversationRunner{
			service:          s,
			agentConn:        agentConn,
			managerSessionID: sessionID,
		}, func() {
			release()
		}, nil
}

func (s *Service) initializeAgentConversationSession(
	ctx context.Context,
	runner *conversationRunner,
	session domain.AgentSession,
) error {
	sessionID := session.SessionID
	runner.managerSessionID = sessionID
	if strings.TrimSpace(session.NativeID) != "" {
		return nil
	}
	if _, err := runner.request(
		ctx,
		"session/new",
		map[string]any{
			"cwd":        "/tmp",
			"mcpServers": agentConversationMCPServers(session),
		},
		nil,
	); err != nil {
		return err
	}
	_, err := runner.request(
		ctx,
		"session/set_mode",
		map[string]any{
			"sessionId": sessionID,
			"modeId":    "full-access",
		},
		nil,
	)
	return err
}

func agentConversationMCPServers(session domain.AgentSession) []any {
	env := []map[string]string{
		{"name": "PAX_AGENT_ID", "value": session.AgentID},
		{"name": "PAX_SESSION_ID", "value": session.SessionID},
	}
	if strings.TrimSpace(session.RepresentativeAgentID) != "" {
		env = append(env, map[string]string{
			"name":  "PAX_REPRESENTATIVE_AGENT_ID",
			"value": session.RepresentativeAgentID,
		})
	}
	return []any{
		map[string]any{
			"name":    "pax-conversation",
			"command": "/Users/gengcongkai/.local/bin/paxd",
			"args":    []string{"mcp", "conversation", "serve"},
			"env":     env,
		},
	}
}

func startAgentConversationTurnMetadata(
	start domain.AgentConversationStart,
	current agentConversationTurnRunner,
	next agentConversationTurnRunner,
	turnIndex int,
	prompt string,
) paxInvocationPromptMetadata {
	phase := "reply"
	if turnIndex == 0 {
		phase = "inquiry"
	}
	sender := paxInvocationPromptEndpoint{
		RepresentativeAgentID: representativeIDForTurn(start, next),
		AgentID:               next.agentID,
		SessionID:             next.sessionID,
	}
	receiver := paxInvocationPromptEndpoint{
		RepresentativeAgentID: representativeIDForTurn(start, current),
		AgentID:               current.agentID,
		SessionID:             current.sessionID,
	}
	return conversationTurnInvocationMetadata(
		start.Invocation,
		phase,
		"target",
		sender,
		receiver,
		strings.TrimSpace(prompt),
		prompt,
	)
}

func deliveryConversationTurnMetadata(
	delivery domain.ConversationDelivery,
	displayText string,
	originalText string,
) paxInvocationPromptMetadata {
	phase := "inquiry"
	if delivery.Invocation.Status == domain.ConversationAgentInvocationStatusCompleted {
		phase = "reply"
	}
	sender := paxInvocationPromptEndpoint{
		RepresentativeAgentID: delivery.Invocation.SourceRepresentativeAgentID,
		AgentID:               delivery.SourceSession.AgentID,
		SessionID:             delivery.SourceSession.SessionID,
	}
	receiver := paxInvocationPromptEndpoint{
		RepresentativeAgentID: delivery.Invocation.TargetRepresentativeAgentID,
		AgentID:               delivery.TargetSession.AgentID,
		SessionID:             delivery.TargetSession.SessionID,
	}
	if phase == "reply" {
		sender.RepresentativeAgentID = delivery.Invocation.TargetRepresentativeAgentID
		receiver.RepresentativeAgentID = delivery.Invocation.SourceRepresentativeAgentID
	}
	return conversationTurnInvocationMetadata(
		delivery.Invocation,
		phase,
		"target",
		sender,
		receiver,
		strings.TrimSpace(displayText),
		originalText,
	)
}

func representativeIDForTurn(
	start domain.AgentConversationStart,
	turn agentConversationTurnRunner,
) string {
	switch {
	case turn.agentID == start.SourceRuntimeAgent.AgentID &&
		turn.sessionID == start.SourceSession.SessionID:
		return start.SourceRepresentative.RepresentativeAgentID
	case turn.agentID == start.TargetRuntimeAgent.AgentID &&
		turn.sessionID == start.TargetSession.SessionID:
		return start.TargetRepresentative.RepresentativeAgentID
	default:
		return ""
	}
}

func (s *Service) promptAgentConversationTurn(
	ctx context.Context,
	turn agentConversationTurnRunner,
	prompt string,
	meta paxInvocationPromptMetadata,
) (string, error) {
	logging.Info(
		ctx,
		"conversation delivery prompt turn started",
		slog.String("agent_id", turn.agentID),
		slog.String("session_id", turn.sessionID),
		slog.Int("prompt_chars", len(strings.TrimSpace(prompt))),
	)
	turn.runner.managerSessionID = turn.sessionID
	sub := turn.runner.agentConn.subscribeSSE(turn.sessionID)
	defer turn.runner.agentConn.unsubscribeSSE(sub)
	activity := drainAgentConversationSSE(ctx, sub)
	before, err := s.store.ListMessages(ctx, turn.agentID, turn.sessionID, 100)
	if err != nil {
		return "", err
	}
	beforeSeen := make(map[string]struct{}, len(before))
	for _, msg := range before {
		beforeSeen[msg.MessageID] = struct{}{}
	}
	params := map[string]any{
		"sessionId": turn.sessionID,
		"prompt": []map[string]string{{
			"type": "text",
			"text": prompt,
		}},
	}
	if strings.TrimSpace(meta.InvocationID) != "" {
		params[paxInvocationPromptParam] = meta
	}
	if _, err := turn.runner.request(ctx, "session/prompt", params, activity); err != nil {
		logging.Warn(
			ctx,
			"conversation delivery prompt turn request failed",
			slog.String("agent_id", turn.agentID),
			slog.String("session_id", turn.sessionID),
			logging.Err(err),
		)
		return "", err
	}
	after, err := s.store.ListMessages(ctx, turn.agentID, turn.sessionID, 100)
	if err != nil {
		return "", err
	}
	for i := len(after) - 1; i >= 0; i-- {
		msg := after[i]
		if _, ok := beforeSeen[msg.MessageID]; ok {
			continue
		}
		if msg.Direction != domain.MessageDirectionAgentToUser {
			continue
		}
		text, err := s.messageText(ctx, msg.MessageID)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(text) != "" {
			logging.Info(
				ctx,
				"conversation delivery prompt turn completed",
				slog.String("agent_id", turn.agentID),
				slog.String("session_id", turn.sessionID),
				slog.String("response_message_id", msg.MessageID),
				slog.Int("response_chars", len(strings.TrimSpace(text))),
			)
			return text, nil
		}
	}
	logging.Info(
		ctx,
		"conversation delivery prompt turn completed without response text",
		slog.String("agent_id", turn.agentID),
		slog.String("session_id", turn.sessionID),
	)
	return "", nil
}

func drainAgentConversationSSE(
	ctx context.Context,
	sub *acpSSESubscriber,
) <-chan struct{} {
	activity := make(chan struct{}, 1)
	go func() {
		defer close(activity)
		for {
			select {
			case _, ok := <-sub.ch:
				if !ok {
					return
				}
				select {
				case activity <- struct{}{}:
				default:
				}
			case <-ctx.Done():
				return
			}
		}
	}()
	return activity
}

func (s *Service) messageText(ctx context.Context, messageID string) (string, error) {
	parts, err := s.store.ListMessageParts(ctx, messageID)
	if err != nil {
		return "", err
	}
	var builder strings.Builder
	for _, part := range parts {
		if part.Text == "" {
			continue
		}
		builder.WriteString(part.Text)
	}
	return builder.String(), nil
}
