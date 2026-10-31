package manager

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/secretary"
)

func (s *Service) secretaryExecutor() secretary.Executor {
	return secretary.ExecutorFunc(s.executeSecretaryAgent)
}

func (s *Service) executeSecretaryAgent(
	ctx context.Context,
	request secretary.ExecutionRequest,
) (secretary.ExecutionResult, error) {
	if err := ctx.Err(); err != nil {
		return secretary.ExecutionResult{}, err
	}
	if request.OwnerID == "" || request.AgentID == "" || request.RunID == "" ||
		strings.TrimSpace(request.Prompt) == "" {
		return secretary.ExecutionResult{}, domain.ErrConflict
	}
	user, err := s.store.GetUser(ctx, request.OwnerID)
	if err != nil {
		return secretary.ExecutionResult{}, err
	}
	principal := domain.UserPrincipal{User: user}
	agent, err := s.store.GetAgent(ctx, principal, request.AgentID)
	if err != nil {
		return secretary.ExecutionResult{}, err
	}
	if agent.OwnerUserID != request.OwnerID {
		return secretary.ExecutionResult{}, domain.ErrNotFound
	}
	return s.executeOwnedSecretaryAgent(ctx, principal, agent, request)
}

func (s *Service) executeOwnedSecretaryAgent(
	ctx context.Context,
	principal domain.UserPrincipal,
	agent domain.Agent,
	request secretary.ExecutionRequest,
) (secretary.ExecutionResult, error) {
	sessionID := secretary.ExecutionSessionID(request.OwnerID, request.RunID)
	turnID := secretary.ExecutionTurnID(request.OwnerID, request.RunID)
	result := secretary.ExecutionResult{SessionID: sessionID}
	session, err := s.store.GetSession(ctx, principal, sessionID)
	if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return result, err
	}
	if err == nil {
		if session.AgentID != agent.AgentID || session.ArchivedAt != nil ||
			session.Transport == domain.SessionTransportE2EE {
			return result, domain.ErrNotFound
		}
		var started, completed bool
		result.Text, started, completed, err = s.readSecretaryExecution(
			ctx,
			agent.AgentID,
			sessionID,
			turnID,
		)
		if err != nil || completed {
			return result, err
		}
		if started {
			return result, secretary.ErrExecutionIncomplete
		}
	}
	if request.BeforePrompt != nil {
		if err = request.BeforePrompt(ctx, agent.AgentID, sessionID); err != nil {
			return result, err
		}
	}
	runner, release, err := s.claimAgentConversationRunner(
		ctx,
		agent.AgentID,
		sessionID,
		session.NativeID,
	)
	if err != nil {
		return result, err
	}
	defer release()
	conversation := conversationSession{managerID: sessionID, nativeID: session.NativeID}
	if session.NativeID == "" {
		conversation, err = s.createConversationSession(
			ctx,
			principal,
			&runner,
			conversationRequest{ApprovalMode: domain.SessionApprovalModeManual},
		)
		if err != nil {
			return result, err
		}
	}
	if request.BeforePrompt != nil {
		if err = request.BeforePrompt(ctx, agent.AgentID, sessionID); err != nil {
			return result, err
		}
	}
	sub := runner.agentConn.subscribeSSE(sessionID)
	defer runner.agentConn.unsubscribeSSE(sub)
	writer := &queuedTurnWriter{header: make(http.Header)}
	completed, err := s.promptConversationOnce(
		ctx,
		writer,
		writer,
		&runner,
		conversation,
		sub,
		turnID,
		[]map[string]any{{"type": "text", "text": request.Prompt}},
	)
	if ctx.Err() != nil {
		cancelSecretaryTurn(ctx, &runner, sessionID)
	}
	if err != nil {
		return result, err
	}
	if !completed {
		if writer.accepted {
			return result, secretary.ErrApprovalRequired
		}
		return result, secretary.ErrExecutionIncomplete
	}
	result.Text, _, _, err = s.readSecretaryExecution(ctx, agent.AgentID, sessionID, turnID)
	return result, err
}

func cancelSecretaryTurn(ctx context.Context, runner *conversationRunner, sessionID string) {
	cancelCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	payload, _ := json.Marshal(
		map[string]any{
			"jsonrpc": "2.0",
			"method":  "session/cancel",
			"params":  map[string]any{"sessionId": sessionID},
		},
	)
	_ = runner.sendCancelRaw(cancelCtx, payload)
}

func (s *Service) readSecretaryExecution(
	ctx context.Context,
	agentID, sessionID, turnID string,
) (string, bool, bool, error) {
	messages, err := s.store.ListMessages(ctx, agentID, sessionID, 1000)
	if err != nil {
		return "", false, false, err
	}
	var output strings.Builder
	started, completed := false, false
	for _, message := range messages {
		if message.TurnID != turnID {
			continue
		}
		started = true
		if message.MessageType == "turn_done" && message.Status == "complete" {
			completed = true
			continue
		}
		if message.Role != "assistant" {
			continue
		}
		parts, partErr := s.store.ListMessageParts(ctx, message.MessageID)
		if partErr != nil {
			return "", started, completed, partErr
		}
		document, canonicalErr := domain.CanonicalSearchMessage(
			domain.MessageWithParts{Message: message, Parts: parts},
		)
		if errors.Is(canonicalErr, domain.ErrNotFound) {
			continue
		}
		if canonicalErr != nil {
			return "", started, completed, canonicalErr
		}
		output.WriteString(document.Body)
		if output.Len() > 1024*1024 {
			return "", started, completed, errors.New("secretary response exceeds 1 MiB")
		}
	}
	if completed && strings.TrimSpace(output.String()) == "" {
		return "", started, completed, secretary.ErrNoOutput
	}
	return output.String(), started, completed, nil
}
