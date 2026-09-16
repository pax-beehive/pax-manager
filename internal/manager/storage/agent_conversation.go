package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/logging"
)

const (
	defaultAgentConversationMaxTurns = 1
	maxAgentConversationMaxTurns     = 10
)

type paxInvocationEndpoint struct {
	RepresentativeAgentID string `json:"representative_agent_id,omitempty"`
	AgentID               string `json:"agent_id"`
	SessionID             string `json:"session_id"`
}

type paxInvocationDisplayContent struct {
	DisplayText  string `json:"display_text"`
	OriginalText string `json:"original_text"`
}

type paxInvocationDisplayRaw struct {
	InvocationID      string                      `json:"invocation_id"`
	InvocationType    string                      `json:"invocation_type"`
	Phase             string                      `json:"phase"`
	Side              string                      `json:"side"`
	ReplacesMessageID []string                    `json:"replaces_message_ids"`
	Sender            paxInvocationEndpoint       `json:"sender"`
	Receiver          paxInvocationEndpoint       `json:"receiver"`
	Content           paxInvocationDisplayContent `json:"content"`
}

type paxInvocationPendingRaw struct {
	InvocationID      string                      `json:"invocation_id"`
	InvocationType    string                      `json:"invocation_type"`
	Phase             string                      `json:"phase"`
	Side              string                      `json:"side"`
	ToolCallID        string                      `json:"tool_call_id,omitempty"`
	PromptMessageID   string                      `json:"prompt_message_id,omitempty"`
	ReplacesMessageID []string                    `json:"replaces_message_ids"`
	Sender            paxInvocationEndpoint       `json:"sender"`
	Receiver          paxInvocationEndpoint       `json:"receiver"`
	Content           paxInvocationDisplayContent `json:"content"`
}

type paxInvocationPromptDisplay struct {
	InvocationID    string
	Phase           string
	Side            string
	Sender          paxInvocationEndpoint
	Receiver        paxInvocationEndpoint
	DisplayText     string
	OriginalText    string
	LogicalKeyScope string
}

func (s *PostgresStore) ListRepresentativeAgents(
	ctx context.Context,
	principal UserPrincipal,
	runtimeAgentID string,
) ([]domain.RepresentativeAgent, error) {
	query := representativeAgentSelectSQL + `
		JOIN agents a ON a.agent_id = representative_agents.runtime_agent_id
		WHERE (
			a.owner_user_id = $1
			OR ` + teamAgentAccessSQL("a.agent_id", "$1") + `
		)`
	args := []any{principal.User.UserID}
	if strings.TrimSpace(runtimeAgentID) != "" {
		args = append(args, strings.TrimSpace(runtimeAgentID))
		query += ` AND representative_agents.runtime_agent_id = $2`
	}
	query += ` ORDER BY representative_agents.created_at ASC`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]domain.RepresentativeAgent, 0)
	for rows.Next() {
		rep, err := scanRepresentativeAgent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, rep)
	}
	return out, rows.Err()
}

func (s *PostgresStore) GetAgentOwnerInfo(
	ctx context.Context,
	principal UserPrincipal,
	req domain.AgentOwnerInfoRequest,
) (domain.AgentOwnerInfo, error) {
	agentID := strings.TrimSpace(req.AgentID)
	representativeAgentID := strings.TrimSpace(req.RepresentativeAgentID)
	if representativeAgentID != "" {
		rep, err := s.getRepresentativeAgent(ctx, representativeAgentID)
		if err != nil {
			return domain.AgentOwnerInfo{}, err
		}
		if agentID != "" && agentID != rep.RuntimeAgentID {
			return domain.AgentOwnerInfo{}, ErrNotFound
		}
		agent, err := s.GetAgent(ctx, principal, rep.RuntimeAgentID)
		if err != nil {
			return domain.AgentOwnerInfo{}, err
		}
		profile, err := s.getAgentProfile(ctx, rep.ProfileID)
		if err != nil {
			return domain.AgentOwnerInfo{}, err
		}
		owner, err := s.agentOwnerSubject(
			ctx,
			principal,
			rep.RepresentsType,
			rep.RepresentsID,
			agent.OwnerUserID,
		)
		if err != nil {
			return domain.AgentOwnerInfo{}, err
		}
		return domain.AgentOwnerInfo{
			Agent:               agent,
			RepresentativeAgent: &rep,
			Profile:             &profile,
			Owner:               owner,
		}, nil
	}
	if agentID == "" {
		return domain.AgentOwnerInfo{}, ErrNotFound
	}
	agent, err := s.GetAgent(ctx, principal, agentID)
	if err != nil {
		return domain.AgentOwnerInfo{}, err
	}
	owner, err := s.agentOwnerSubject(ctx, principal, "user", agent.OwnerUserID, agent.OwnerUserID)
	if err != nil {
		return domain.AgentOwnerInfo{}, err
	}
	return domain.AgentOwnerInfo{Agent: agent, Owner: owner}, nil
}

func (s *PostgresStore) UpsertRepresentativeAgent(
	ctx context.Context,
	principal UserPrincipal,
	req domain.UpsertRepresentativeAgentRequest,
) (domain.RepresentativeAgent, domain.AgentProfile, error) {
	agent, err := s.GetAgent(ctx, principal, strings.TrimSpace(req.RuntimeAgentID))
	if err != nil {
		return domain.RepresentativeAgent{}, domain.AgentProfile{}, err
	}
	if !canAccessOwner(principal, agent.OwnerUserID) {
		return domain.RepresentativeAgent{}, domain.AgentProfile{}, ErrUnauthorized
	}
	now := s.now().UTC()
	profileID := strings.TrimSpace(req.ProfileID)
	if profileID == "" {
		profileID = deterministicAgentProfileID(agent.AgentID, agent.OwnerUserID)
	}
	representsType := firstNonEmpty(strings.TrimSpace(req.RepresentsType), "user")
	representsID, err := s.validateRepresentsSubject(
		ctx,
		representsType,
		strings.TrimSpace(req.RepresentsID),
		agent.OwnerUserID,
	)
	if err != nil {
		return domain.RepresentativeAgent{}, domain.AgentProfile{}, err
	}
	displayName := firstNonEmpty(strings.TrimSpace(req.DisplayName), agent.Name, agent.AgentID)
	profile, err := s.upsertAgentProfile(
		ctx,
		profileID,
		agent.OwnerUserID,
		principal.User.UserID,
		displayName,
		req.Description,
		req.Card,
		req.Metadata,
		now,
	)
	if err != nil {
		return domain.RepresentativeAgent{}, domain.AgentProfile{}, err
	}
	repID := deterministicRepresentativeAgentID(
		agent.AgentID,
		profileID,
		representsType,
		representsID,
	)
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO representative_agents (
			representative_agent_id, profile_id, runtime_agent_id, represents_type,
			represents_id, approval_policy_id, status, created_by_user_id, created_at, updated_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$9)
		ON CONFLICT (representative_agent_id) DO UPDATE SET
			profile_id = EXCLUDED.profile_id,
			runtime_agent_id = EXCLUDED.runtime_agent_id,
			represents_type = EXCLUDED.represents_type,
			represents_id = EXCLUDED.represents_id,
			approval_policy_id = EXCLUDED.approval_policy_id,
			status = EXCLUDED.status,
			updated_at = EXCLUDED.updated_at,
			archived_at = NULL
		RETURNING representative_agent_id, profile_id, runtime_agent_id, represents_type,
			represents_id, approval_policy_id, status, created_by_user_id, created_at,
			updated_at, archived_at
	`, repID, profileID, agent.AgentID, representsType, representsID,
		strings.TrimSpace(req.ApprovalPolicyID), domain.ConversationStatusActive,
		principal.User.UserID, now)
	rep, err := scanRepresentativeAgent(row)
	return rep, profile, err
}

func (s *PostgresStore) validateRepresentsSubject(
	ctx context.Context,
	representsType string,
	representsID string,
	ownerUserID string,
) (string, error) {
	switch representsType {
	case "user":
		if representsID == "" {
			return ownerUserID, nil
		}
		if representsID != ownerUserID {
			return "", ErrUnauthorized
		}
		return representsID, nil
	case "team":
		if representsID == "" {
			return "", ErrConflict
		}
		var ok bool
		if err := s.db.QueryRowContext(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM team_members
				JOIN teams ON teams.team_id = team_members.team_id
				WHERE team_members.team_id = $1
					AND team_members.user_id = $2
					AND team_members.status = $3
					AND teams.status = $4
			)
		`, representsID, ownerUserID, domain.TeamMemberStatusActive, domain.TeamStatusActive).Scan(&ok); err != nil {
			return "", err
		}
		if !ok {
			return "", ErrUnauthorized
		}
		return representsID, nil
	default:
		return "", ErrConflict
	}
}

func (s *PostgresStore) upsertAgentProfile(
	ctx context.Context,
	profileID string,
	ownerUserID string,
	createdByUserID string,
	displayName string,
	description string,
	card json.RawMessage,
	metadata json.RawMessage,
	now time.Time,
) (domain.AgentProfile, error) {
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO agent_profiles (
			profile_id, owner_type, owner_id, display_name, description,
			card_json, metadata_json, status, created_by_user_id, created_at, updated_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$10)
		ON CONFLICT (profile_id) DO UPDATE SET
			display_name = EXCLUDED.display_name,
			description = EXCLUDED.description,
			card_json = EXCLUDED.card_json,
			metadata_json = EXCLUDED.metadata_json,
			status = EXCLUDED.status,
			updated_at = EXCLUDED.updated_at,
			archived_at = NULL
		WHERE agent_profiles.owner_id = EXCLUDED.owner_id
		RETURNING profile_id, owner_type, owner_id, display_name, description,
			card_json, instructions_md, default_model, tool_policy_json, metadata_json,
			status, created_by_user_id, created_at, updated_at, archived_at
	`, profileID, "user", ownerUserID, displayName, description, jsonDefault(card, "{}"),
		jsonDefault(metadata, "{}"), domain.ConversationStatusActive, createdByUserID, now)
	profile, err := scanAgentProfile(row)
	if errors.Is(err, sql.ErrNoRows) {
		// The profile exists but belongs to a different owner.
		return domain.AgentProfile{}, ErrConflict
	}
	return profile, err
}

func (s *PostgresStore) StartAgentConversation(
	ctx context.Context,
	node Node,
	req domain.StartAgentConversationRequest,
) (domain.AgentConversationStart, error) {
	sourceAgent, err := s.GetNodeAgent(ctx, node.NodeID, req.FromRuntimeAgentID)
	if err != nil {
		return domain.AgentConversationStart{}, err
	}
	sourceRep, err := s.findSourceRepresentativeAgent(
		ctx,
		sourceAgent.AgentID,
		req.FromRepresentativeAgentID,
	)
	if err != nil {
		return domain.AgentConversationStart{}, err
	}
	targetRep, targetAgent, err := s.getTargetRepresentativeAgent(ctx, req.ToRepresentativeAgentID)
	if err != nil {
		return domain.AgentConversationStart{}, err
	}
	if !s.agentConversationUsersCanInteract(ctx, sourceAgent.OwnerUserID, targetAgent.OwnerUserID) {
		return domain.AgentConversationStart{}, ErrUnauthorized
	}

	now := s.now().UTC()
	conversationID := strings.TrimSpace(req.ConversationID)
	if conversationID != "" {
		member, err := s.hasActiveConversationMembership(
			ctx,
			conversationID,
			sourceAgent.OwnerUserID,
		)
		if err != nil {
			return domain.AgentConversationStart{}, err
		}
		if !member {
			return domain.AgentConversationStart{}, ErrNotFound
		}
	}
	if conversationID == "" {
		conversationID, err = newSecret("conv")
		if err != nil {
			return domain.AgentConversationStart{}, err
		}
	}
	conversation, err := s.upsertAgentConversation(
		ctx,
		conversationID,
		sourceAgent.OwnerUserID,
		now,
	)
	if err != nil {
		return domain.AgentConversationStart{}, err
	}
	if err := s.upsertConversationMember(ctx, conversationID, sourceAgent.OwnerUserID, domain.ConversationMemberRoleOwner, now); err != nil {
		return domain.AgentConversationStart{}, err
	}
	if targetAgent.OwnerUserID != sourceAgent.OwnerUserID {
		if err := s.upsertConversationMember(ctx, conversationID, targetAgent.OwnerUserID, domain.ConversationMemberRoleMember, now); err != nil {
			return domain.AgentConversationStart{}, err
		}
	}
	sourceBinding, err := s.upsertConversationAgentBinding(
		ctx,
		conversationID,
		sourceRep.RepresentativeAgentID,
		sourceAgent.OwnerUserID,
		domain.ConversationAgentRelationshipParticipant,
		now,
	)
	if err != nil {
		return domain.AgentConversationStart{}, err
	}
	targetBinding, err := s.upsertConversationAgentBinding(
		ctx,
		conversationID,
		targetRep.RepresentativeAgentID,
		sourceAgent.OwnerUserID,
		domain.ConversationAgentRelationshipAssistant,
		now,
	)
	if err != nil {
		return domain.AgentConversationStart{}, err
	}

	sourceSession, err := s.createConversationSession(
		ctx,
		sourceAgent,
		sourceRep,
		conversationID,
		sourceAgent.OwnerUserID,
		now,
	)
	if err != nil {
		return domain.AgentConversationStart{}, err
	}
	targetSession, err := s.createConversationSession(
		ctx,
		targetAgent,
		targetRep,
		conversationID,
		sourceAgent.OwnerUserID,
		now,
	)
	if err != nil {
		return domain.AgentConversationStart{}, err
	}
	invocation, err := s.createConversationInvocation(
		ctx,
		conversationID,
		"",
		sourceRep.RepresentativeAgentID,
		sourceAgent.AgentID,
		sourceSession.SessionID,
		targetRep.RepresentativeAgentID,
		targetAgent.AgentID,
		targetSession.SessionID,
		sourceAgent.OwnerUserID,
		req.MaxTurns,
		"",
		now,
	)
	if err != nil {
		return domain.AgentConversationStart{}, err
	}
	prompt, err := s.createConversationPromptMessage(
		ctx,
		conversationID,
		sourceAgent,
		sourceSession.SessionID,
		req.Input,
		sourceInvocationPromptDisplay(
			invocation,
			sourceRep.RepresentativeAgentID,
			sourceAgent.AgentID,
			sourceSession.SessionID,
			targetRep.RepresentativeAgentID,
			targetAgent.AgentID,
			targetSession.SessionID,
			"inquiry",
			"source",
			"Asked "+conversationAgentLabel(targetAgent)+" for input.",
			req.Input,
		),
		now,
	)
	if err != nil {
		return domain.AgentConversationStart{}, err
	}

	return domain.AgentConversationStart{
		Conversation:         conversation,
		SourceBinding:        sourceBinding,
		TargetBinding:        targetBinding,
		Invocation:           invocation,
		SourceRepresentative: sourceRep,
		TargetRepresentative: targetRep,
		SourceRuntimeAgent:   sourceAgent,
		TargetRuntimeAgent:   targetAgent,
		SourceSession:        sourceSession,
		TargetSession:        targetSession,
		PromptMessage:        prompt,
	}, nil
}

func (s *PostgresStore) StartAgentConversationForUser(
	ctx context.Context,
	principal UserPrincipal,
	req domain.StartAgentConversationRequest,
) (domain.AgentConversationStart, error) {
	sourceAgent, err := s.GetAgent(ctx, principal, strings.TrimSpace(req.FromRuntimeAgentID))
	if err != nil {
		return domain.AgentConversationStart{}, err
	}
	if !canAccessOwner(principal, sourceAgent.OwnerUserID) {
		return domain.AgentConversationStart{}, ErrUnauthorized
	}
	return s.StartAgentConversation(ctx, Node{NodeID: sourceAgent.NodeID}, req)
}

func (s *PostgresStore) DeliverAgentConversation(
	ctx context.Context,
	node Node,
	req domain.DeliverConversationRequest,
) (domain.ConversationDelivery, error) {
	ctx = logging.With(ctx, conversationDeliveryRequestAttrs(node, req)...)
	logging.Info(ctx, "conversation delivery received")
	switch req.Target.Kind {
	case domain.ConversationDeliveryTargetRepresentative:
		return s.deliverAgentConversationToRepresentative(ctx, node, req)
	case domain.ConversationDeliveryTargetAgent:
		return s.deliverAgentConversationToAgent(ctx, node, req)
	case domain.ConversationDeliveryTargetActiveInvocation:
		return s.deliverAgentConversationActiveInvocationReply(ctx, node, req)
	default:
		return domain.ConversationDelivery{}, ErrNotFound
	}
}

// deliverAgentConversationToAgent handles a direct agent_id target. It resolves
// the target agent, requires source and target to be able to interact (same
// owner or shared team), ensures both agents' canonical representatives exist
// (idempotently, without clobbering an explicitly configured profile or
// approval policy), and then runs the same delivery core as the representative
// path.
func (s *PostgresStore) deliverAgentConversationToAgent(
	ctx context.Context,
	node Node,
	req domain.DeliverConversationRequest,
) (domain.ConversationDelivery, error) {
	sourceAgent, err := s.GetNodeAgent(ctx, node.NodeID, req.Source.AgentID)
	if err != nil {
		logging.Warn(ctx, "conversation delivery source agent lookup failed", logging.Err(err))
		return domain.ConversationDelivery{}, err
	}
	ctx = logging.With(ctx, slog.String("source_owner_user_id", sourceAgent.OwnerUserID))
	targetAgent, err := s.getAgentByID(ctx, req.Target.AgentID)
	if err != nil {
		logging.Warn(ctx, "conversation delivery target agent lookup failed", logging.Err(err))
		return domain.ConversationDelivery{}, err
	}
	ctx = logging.With(ctx,
		slog.String("target_runtime_agent_id", targetAgent.AgentID),
		slog.String("target_owner_user_id", targetAgent.OwnerUserID),
	)
	if !s.agentConversationUsersCanInteract(ctx, sourceAgent.OwnerUserID, targetAgent.OwnerUserID) {
		logging.Warn(ctx, "conversation delivery rejected because users cannot interact")
		return domain.ConversationDelivery{}, ErrUnauthorized
	}

	now := s.now().UTC()
	sourceRep, err := s.ensureCanonicalRepresentativeAgent(ctx, sourceAgent, now)
	if err != nil {
		logging.Warn(
			ctx,
			"conversation delivery source representative ensure failed",
			logging.Err(err),
		)
		return domain.ConversationDelivery{}, err
	}
	ctx = logging.With(
		ctx,
		slog.String("resolved_source_representative_agent_id", sourceRep.RepresentativeAgentID),
	)
	targetRep, err := s.ensureCanonicalRepresentativeAgent(ctx, targetAgent, now)
	if err != nil {
		logging.Warn(
			ctx,
			"conversation delivery target representative ensure failed",
			logging.Err(err),
		)
		return domain.ConversationDelivery{}, err
	}

	conversationID, err := newSecret("conv")
	if err != nil {
		logging.Warn(
			ctx,
			"conversation delivery conversation id generation failed",
			logging.Err(err),
		)
		return domain.ConversationDelivery{}, err
	}
	receiptToken, err := newSecret("rcpt")
	if err != nil {
		logging.Warn(ctx, "conversation delivery receipt token generation failed", logging.Err(err))
		return domain.ConversationDelivery{}, err
	}
	delivery, err := s.createConversationDelivery(
		ctx,
		conversationDeliverySpec{
			conversationID:   conversationID,
			parentInvocation: nil,
			sourceAgent:      sourceAgent,
			sourceRep:        sourceRep,
			sourceSessionID:  req.Source.SessionID,
			targetAgent:      targetAgent,
			targetRep:        targetRep,
			targetSessionID:  req.Target.SessionID,
			context:          req.Context.Effective(),
			instruction:      req.Instruction,
			reason:           req.Reason,
			receiptToken:     receiptToken,
			receiptTokenHash: hashConversationReceiptToken(receiptToken),
			now:              now,
		},
	)
	if err != nil {
		logging.Warn(ctx, "conversation delivery store failed", logging.Err(err))
		return domain.ConversationDelivery{}, err
	}
	logging.Info(
		ctx,
		"conversation delivery stored",
		slog.String("conversation_id", delivery.Conversation.ConversationID),
		slog.String("invocation_id", delivery.Invocation.InvocationID),
		slog.String("source_session_id", delivery.SourceSession.SessionID),
		slog.String("target_session_id", delivery.TargetSession.SessionID),
		slog.String("delivery_status", delivery.DeliveryStatus),
	)
	return delivery, nil
}

// getAgentByID looks up an agent by id with no owner scoping. Callers MUST
// enforce authorization (agentConversationUsersCanInteract) before acting on
// the result; this is only the resolution step.
func (s *PostgresStore) getAgentByID(ctx context.Context, agentID string) (Agent, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return Agent{}, ErrNotFound
	}
	return scanAgent(s.db.QueryRowContext(ctx, `
		SELECT `+agentSelectColumns+`
		FROM agents
		WHERE agent_id = $1 AND deleted_at IS NULL
	`, agentID))
}

// ensureCanonicalRepresentativeAgent returns the deterministic self-represents
// representative for an agent, creating it and its profile if absent. It never
// clobbers an existing explicitly configured profile or approval policy: the
// profile insert is ON CONFLICT DO NOTHING and the representative insert is a
// no-op update on conflict purely to return the existing row.
func (s *PostgresStore) ensureCanonicalRepresentativeAgent(
	ctx context.Context,
	agent Agent,
	now time.Time,
) (domain.RepresentativeAgent, error) {
	profileID := deterministicAgentProfileID(agent.AgentID, agent.OwnerUserID)
	representsType := "user"
	representsID := agent.OwnerUserID
	repID := deterministicRepresentativeAgentID(
		agent.AgentID,
		profileID,
		representsType,
		representsID,
	)

	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO agent_profiles (
			profile_id, owner_type, owner_id, display_name, description,
			status, created_by_user_id, created_at, updated_at
		)
		VALUES ($1,'user',$2,$3,$4,$5,$2,$6,$6)
		ON CONFLICT (profile_id) DO NOTHING
	`, profileID, agent.OwnerUserID, firstNonEmpty(agent.Name, agent.AgentID),
		agent.Description, domain.ConversationStatusActive, now); err != nil {
		return domain.RepresentativeAgent{}, err
	}

	row := s.db.QueryRowContext(ctx, `
		INSERT INTO representative_agents (
			representative_agent_id, profile_id, runtime_agent_id, represents_type,
			represents_id, approval_policy_id, status, created_by_user_id, created_at, updated_at
		)
		VALUES ($1,$2,$3,$4,$5,'',$6,$7,$8,$8)
		ON CONFLICT (representative_agent_id) DO UPDATE SET
			updated_at = representative_agents.updated_at
		RETURNING representative_agent_id, profile_id, runtime_agent_id, represents_type,
			represents_id, approval_policy_id, status, created_by_user_id, created_at,
			updated_at, archived_at
	`, repID, profileID, agent.AgentID, representsType, representsID,
		domain.ConversationStatusActive, agent.OwnerUserID, now)
	return scanRepresentativeAgent(row)
}

func (s *PostgresStore) deliverAgentConversationToRepresentative(
	ctx context.Context,
	node Node,
	req domain.DeliverConversationRequest,
) (domain.ConversationDelivery, error) {
	sourceAgent, err := s.GetNodeAgent(ctx, node.NodeID, req.Source.AgentID)
	if err != nil {
		logging.Warn(ctx, "conversation delivery source agent lookup failed", logging.Err(err))
		return domain.ConversationDelivery{}, err
	}
	ctx = logging.With(ctx, slog.String("source_owner_user_id", sourceAgent.OwnerUserID))
	sourceRep, err := s.findSourceRepresentativeAgent(
		ctx,
		sourceAgent.AgentID,
		req.Source.RepresentativeAgentID,
	)
	if err != nil {
		logging.Warn(
			ctx,
			"conversation delivery source representative lookup failed",
			logging.Err(err),
		)
		return domain.ConversationDelivery{}, err
	}
	ctx = logging.With(
		ctx,
		slog.String("resolved_source_representative_agent_id", sourceRep.RepresentativeAgentID),
	)
	targetRep, targetAgent, err := s.getTargetRepresentativeAgent(
		ctx,
		req.Target.RepresentativeAgentID,
	)
	if err != nil {
		logging.Warn(
			ctx,
			"conversation delivery target representative lookup failed",
			logging.Err(err),
		)
		return domain.ConversationDelivery{}, err
	}
	ctx = logging.With(ctx,
		slog.String("target_runtime_agent_id", targetAgent.AgentID),
		slog.String("target_owner_user_id", targetAgent.OwnerUserID),
	)
	if !s.agentConversationUsersCanInteract(ctx, sourceAgent.OwnerUserID, targetAgent.OwnerUserID) {
		logging.Warn(ctx, "conversation delivery rejected because users cannot interact")
		return domain.ConversationDelivery{}, ErrUnauthorized
	}

	now := s.now().UTC()
	conversationID, err := newSecret("conv")
	if err != nil {
		logging.Warn(
			ctx,
			"conversation delivery conversation id generation failed",
			logging.Err(err),
		)
		return domain.ConversationDelivery{}, err
	}
	receiptToken, err := newSecret("rcpt")
	if err != nil {
		logging.Warn(ctx, "conversation delivery receipt token generation failed", logging.Err(err))
		return domain.ConversationDelivery{}, err
	}
	delivery, err := s.createConversationDelivery(
		ctx,
		conversationDeliverySpec{
			conversationID:   conversationID,
			parentInvocation: nil,
			sourceAgent:      sourceAgent,
			sourceRep:        sourceRep,
			sourceSessionID:  req.Source.SessionID,
			targetAgent:      targetAgent,
			targetRep:        targetRep,
			targetSessionID:  req.Target.SessionID,
			context:          req.Context.Effective(),
			instruction:      req.Instruction,
			reason:           req.Reason,
			receiptToken:     receiptToken,
			receiptTokenHash: hashConversationReceiptToken(receiptToken),
			now:              now,
		},
	)
	if err != nil {
		logging.Warn(ctx, "conversation delivery store failed", logging.Err(err))
		return domain.ConversationDelivery{}, err
	}
	logging.Info(
		ctx,
		"conversation delivery stored",
		slog.String("conversation_id", delivery.Conversation.ConversationID),
		slog.String("invocation_id", delivery.Invocation.InvocationID),
		slog.String("source_session_id", delivery.SourceSession.SessionID),
		slog.String("target_session_id", delivery.TargetSession.SessionID),
		slog.String("delivery_status", delivery.DeliveryStatus),
	)
	return delivery, nil
}

func (s *PostgresStore) deliverAgentConversationActiveInvocationReply(
	ctx context.Context,
	node Node,
	req domain.DeliverConversationRequest,
) (domain.ConversationDelivery, error) {
	sourceAgent, err := s.GetNodeAgent(ctx, node.NodeID, req.Source.AgentID)
	if err != nil {
		logging.Warn(ctx, "conversation reply source agent lookup failed", logging.Err(err))
		return domain.ConversationDelivery{}, err
	}
	ctx = logging.With(ctx, slog.String("source_owner_user_id", sourceAgent.OwnerUserID))
	parent, err := s.getActiveConversationInvocationForReply(
		ctx,
		sourceAgent.AgentID,
		req.Source.RepresentativeAgentID,
		req.Source.SessionID,
		req.Target.InvocationID,
	)
	if err != nil {
		logging.Warn(ctx, "conversation reply active invocation lookup failed", logging.Err(err))
		return domain.ConversationDelivery{}, err
	}
	ctx = logging.With(ctx,
		slog.String("parent_invocation_id", parent.InvocationID),
		slog.String("conversation_id", parent.ConversationID),
		slog.String("parent_source_representative_agent_id", parent.SourceRepresentativeAgentID),
		slog.String("parent_target_representative_agent_id", parent.TargetRepresentativeAgentID),
		slog.String("parent_source_agent_id", parent.SourceRuntimeAgentID),
		slog.String("parent_source_session_id", parent.SourceSessionID),
		slog.String("parent_target_agent_id", parent.TargetRuntimeAgentID),
		slog.String("parent_target_session_id", parent.TargetSessionID),
	)
	logging.Info(ctx, "conversation reply active invocation found")
	if strings.TrimSpace(req.Source.RepresentativeAgentID) != "" &&
		req.Source.RepresentativeAgentID != parent.TargetRepresentativeAgentID {
		logging.Warn(
			ctx,
			"conversation reply rejected because source representative does not match active invocation",
		)
		return domain.ConversationDelivery{}, ErrUnauthorized
	}
	sourceRep, err := s.getRepresentativeAgent(ctx, parent.TargetRepresentativeAgentID)
	if err != nil {
		logging.Warn(
			ctx,
			"conversation reply source representative lookup failed",
			logging.Err(err),
		)
		return domain.ConversationDelivery{}, err
	}
	targetRep, targetAgent, err := s.getTargetRepresentativeAgent(
		ctx,
		parent.SourceRepresentativeAgentID,
	)
	if err != nil {
		logging.Warn(
			ctx,
			"conversation reply target representative lookup failed",
			logging.Err(err),
		)
		return domain.ConversationDelivery{}, err
	}
	now := s.now().UTC()
	delivery, err := s.completeConversationInvocationReply(
		ctx,
		conversationDeliverySpec{
			conversationID:   parent.ConversationID,
			parentInvocation: &parent,
			sourceAgent:      sourceAgent,
			sourceRep:        sourceRep,
			sourceSessionID:  parent.TargetSessionID,
			targetAgent:      targetAgent,
			targetRep:        targetRep,
			targetSessionID:  parent.SourceSessionID,
			context:          req.Context.Effective(),
			instruction:      req.Instruction,
			reason:           req.Reason,
			now:              now,
		},
	)
	if err != nil {
		logging.Warn(ctx, "conversation reply store failed", logging.Err(err))
		return domain.ConversationDelivery{}, err
	}
	logging.Info(
		ctx,
		"conversation reply stored",
		slog.String("invocation_id", delivery.Invocation.InvocationID),
		slog.String("reply_source_agent_id", delivery.SourceSession.AgentID),
		slog.String("source_session_id", delivery.SourceSession.SessionID),
		slog.String("reply_target_agent_id", delivery.TargetSession.AgentID),
		slog.String("target_session_id", delivery.TargetSession.SessionID),
		slog.String("delivery_status", delivery.DeliveryStatus),
	)
	return delivery, nil
}

func conversationDeliveryRequestAttrs(
	node Node,
	req domain.DeliverConversationRequest,
) []slog.Attr {
	attrs := []slog.Attr{
		slog.String("node_id", node.NodeID),
		slog.String("target_kind", req.Target.Kind),
	}
	if req.Source.AgentID != "" {
		attrs = append(attrs, slog.String("source_agent_id", req.Source.AgentID))
	}
	if req.Source.RepresentativeAgentID != "" {
		attrs = append(
			attrs,
			slog.String("source_representative_agent_id", req.Source.RepresentativeAgentID),
		)
	}
	if req.Source.SessionID != "" {
		attrs = append(attrs, slog.String("source_session_id", req.Source.SessionID))
	}
	if req.Target.RepresentativeAgentID != "" {
		attrs = append(
			attrs,
			slog.String("target_representative_agent_id", req.Target.RepresentativeAgentID),
		)
	}
	if req.Target.SessionID != "" {
		attrs = append(attrs, slog.String("target_session_id", req.Target.SessionID))
	}
	if req.Target.InvocationID != "" {
		attrs = append(attrs, slog.String("target_invocation_id", req.Target.InvocationID))
	}
	return attrs
}

type conversationDeliverySpec struct {
	conversationID   string
	parentInvocation *domain.ConversationAgentInvocation
	sourceAgent      Agent
	sourceRep        domain.RepresentativeAgent
	sourceSessionID  string
	targetAgent      Agent
	targetRep        domain.RepresentativeAgent
	targetSessionID  string
	context          domain.ConversationDeliveryContext
	instruction      string
	reason           string
	receiptToken     string
	receiptTokenHash string
	now              time.Time
}

func (s *PostgresStore) createConversationDelivery(
	ctx context.Context,
	spec conversationDeliverySpec,
) (domain.ConversationDelivery, error) {
	conversation, err := s.upsertAgentConversation(
		ctx,
		spec.conversationID,
		spec.sourceAgent.OwnerUserID,
		spec.now,
	)
	if err != nil {
		return domain.ConversationDelivery{}, err
	}
	if err := s.upsertConversationMember(ctx, spec.conversationID, spec.sourceAgent.OwnerUserID, domain.ConversationMemberRoleOwner, spec.now); err != nil {
		return domain.ConversationDelivery{}, err
	}
	if spec.targetAgent.OwnerUserID != spec.sourceAgent.OwnerUserID {
		if err := s.upsertConversationMember(ctx, spec.conversationID, spec.targetAgent.OwnerUserID, domain.ConversationMemberRoleMember, spec.now); err != nil {
			return domain.ConversationDelivery{}, err
		}
	}
	if _, err := s.upsertConversationAgentBinding(ctx, spec.conversationID, spec.sourceRep.RepresentativeAgentID, spec.sourceAgent.OwnerUserID, domain.ConversationAgentRelationshipParticipant, spec.now); err != nil {
		return domain.ConversationDelivery{}, err
	}
	if _, err := s.upsertConversationAgentBinding(ctx, spec.conversationID, spec.targetRep.RepresentativeAgentID, spec.sourceAgent.OwnerUserID, domain.ConversationAgentRelationshipAssistant, spec.now); err != nil {
		return domain.ConversationDelivery{}, err
	}
	sourceSession, err := s.ensureConversationSession(
		ctx,
		spec.sourceAgent,
		spec.sourceRep,
		spec.conversationID,
		spec.sourceAgent.OwnerUserID,
		spec.sourceSessionID,
		spec.now,
	)
	if err != nil {
		return domain.ConversationDelivery{}, err
	}
	targetSession, err := s.ensureConversationSession(
		ctx,
		spec.targetAgent,
		spec.targetRep,
		spec.conversationID,
		spec.sourceAgent.OwnerUserID,
		spec.targetSessionID,
		spec.now,
	)
	if err != nil {
		return domain.ConversationDelivery{}, err
	}
	parentInvocationID := ""
	if spec.parentInvocation != nil {
		parentInvocationID = spec.parentInvocation.InvocationID
	}
	invocation, err := s.createConversationInvocation(
		ctx,
		spec.conversationID,
		parentInvocationID,
		spec.sourceRep.RepresentativeAgentID,
		spec.sourceAgent.AgentID,
		sourceSession.SessionID,
		spec.targetRep.RepresentativeAgentID,
		spec.targetAgent.AgentID,
		targetSession.SessionID,
		spec.sourceAgent.OwnerUserID,
		defaultAgentConversationMaxTurns,
		spec.receiptTokenHash,
		spec.now,
	)
	if err != nil {
		return domain.ConversationDelivery{}, err
	}
	prompt, err := s.createConversationPromptMessage(
		ctx,
		spec.conversationID,
		spec.sourceAgent,
		sourceSession.SessionID,
		conversationDeliveryPromptText(spec),
		sourceInvocationPromptDisplay(
			invocation,
			spec.sourceRep.RepresentativeAgentID,
			spec.sourceAgent.AgentID,
			sourceSession.SessionID,
			spec.targetRep.RepresentativeAgentID,
			spec.targetAgent.AgentID,
			targetSession.SessionID,
			"inquiry",
			"source",
			"Asked "+conversationAgentLabel(spec.targetAgent)+" for input.",
			conversationDeliveryPromptText(spec),
		),
		spec.now,
	)
	if err != nil {
		return domain.ConversationDelivery{}, err
	}
	return domain.ConversationDelivery{
		Conversation:     conversation,
		Invocation:       invocation,
		ParentInvocation: spec.parentInvocation,
		SourceSession:    sourceSession,
		TargetSession:    targetSession,
		PromptMessage:    prompt,
		Context:          spec.context,
		Instruction:      spec.instruction,
		Reason:           spec.reason,
		DeliveryStatus:   "stored",
		ReceiptToken:     spec.receiptToken,
	}, nil
}

func (s *PostgresStore) completeConversationInvocationReply(
	ctx context.Context,
	spec conversationDeliverySpec,
) (domain.ConversationDelivery, error) {
	if spec.parentInvocation == nil {
		return domain.ConversationDelivery{}, ErrNotFound
	}
	conversation, err := s.upsertAgentConversation(
		ctx,
		spec.conversationID,
		spec.targetAgent.OwnerUserID,
		spec.now,
	)
	if err != nil {
		return domain.ConversationDelivery{}, err
	}
	sourceSession, err := s.ensureConversationSession(
		ctx,
		spec.sourceAgent,
		spec.sourceRep,
		spec.conversationID,
		spec.sourceAgent.OwnerUserID,
		spec.sourceSessionID,
		spec.now,
	)
	if err != nil {
		return domain.ConversationDelivery{}, err
	}
	targetSession, err := s.ensureConversationSession(
		ctx,
		spec.targetAgent,
		spec.targetRep,
		spec.conversationID,
		spec.targetAgent.OwnerUserID,
		spec.targetSessionID,
		spec.now,
	)
	if err != nil {
		return domain.ConversationDelivery{}, err
	}
	prompt, err := s.createConversationPromptMessage(
		ctx,
		spec.conversationID,
		spec.sourceAgent,
		sourceSession.SessionID,
		conversationDeliveryPromptText(spec),
		sourceInvocationPromptDisplay(
			*spec.parentInvocation,
			spec.sourceRep.RepresentativeAgentID,
			spec.sourceAgent.AgentID,
			sourceSession.SessionID,
			spec.targetRep.RepresentativeAgentID,
			spec.targetAgent.AgentID,
			targetSession.SessionID,
			"reply",
			"source",
			"Sent a reply to "+conversationAgentLabel(spec.targetAgent)+".",
			conversationDeliveryPromptText(spec),
		),
		spec.now,
	)
	if err != nil {
		return domain.ConversationDelivery{}, err
	}
	if err := s.completeConversationInvocation(ctx, spec.parentInvocation.InvocationID); err != nil {
		return domain.ConversationDelivery{}, err
	}
	invocation := *spec.parentInvocation
	invocation.Status = domain.ConversationAgentInvocationStatusCompleted
	invocation.RemainingTurns = 0
	return domain.ConversationDelivery{
		Conversation:   conversation,
		Invocation:     invocation,
		SourceSession:  sourceSession,
		TargetSession:  targetSession,
		PromptMessage:  prompt,
		Context:        spec.context,
		Instruction:    spec.instruction,
		Reason:         spec.reason,
		DeliveryStatus: "stored",
	}, nil
}

func (s *PostgresStore) CompleteAgentConversationInvocation(
	ctx context.Context,
	invocationID string,
) error {
	return s.completeConversationInvocation(ctx, invocationID)
}

func (s *PostgresStore) completeConversationInvocation(
	ctx context.Context,
	invocationID string,
) error {
	if strings.TrimSpace(invocationID) == "" {
		return ErrNotFound
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE conversation_agent_invocations
		SET status = $2, remaining_turns = 0
		WHERE invocation_id = $1
	`, invocationID, domain.ConversationAgentInvocationStatusCompleted)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err == nil && affected == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *PostgresStore) findSourceRepresentativeAgent(
	ctx context.Context,
	runtimeAgentID string,
	representativeAgentID string,
) (domain.RepresentativeAgent, error) {
	query := representativeAgentSelectSQL + `
		WHERE runtime_agent_id = $1 AND status = $2`
	args := []any{runtimeAgentID, domain.ConversationStatusActive}
	if representativeAgentID != "" {
		args = append(args, representativeAgentID)
		query += ` AND representative_agent_id = $3`
	}
	query += ` ORDER BY created_at ASC LIMIT 1`
	return scanRepresentativeAgent(s.db.QueryRowContext(ctx, query, args...))
}

func (s *PostgresStore) getTargetRepresentativeAgent(
	ctx context.Context,
	representativeAgentID string,
) (domain.RepresentativeAgent, Agent, error) {
	rep, err := s.getRepresentativeAgent(ctx, representativeAgentID)
	if err != nil {
		return domain.RepresentativeAgent{}, Agent{}, err
	}
	agent, err := scanAgent(s.db.QueryRowContext(ctx, `
		SELECT `+agentSelectColumns+`
		FROM agents
		WHERE agent_id = $1
	`, rep.RuntimeAgentID))
	if err != nil {
		return domain.RepresentativeAgent{}, Agent{}, err
	}
	return rep, agent, nil
}

func (s *PostgresStore) getRepresentativeAgent(
	ctx context.Context,
	representativeAgentID string,
) (domain.RepresentativeAgent, error) {
	return scanRepresentativeAgent(s.db.QueryRowContext(ctx, representativeAgentSelectSQL+`
		WHERE representative_agent_id = $1 AND status = $2
	`, representativeAgentID, domain.ConversationStatusActive))
}

func (s *PostgresStore) getAgentProfile(
	ctx context.Context,
	profileID string,
) (domain.AgentProfile, error) {
	return scanAgentProfile(s.db.QueryRowContext(ctx, `
		SELECT profile_id, owner_type, owner_id, display_name, description,
			card_json, instructions_md, default_model, tool_policy_json, metadata_json,
			status, created_by_user_id, created_at, updated_at, archived_at
		FROM agent_profiles
		WHERE profile_id = $1 AND status = $2
	`, strings.TrimSpace(profileID), domain.ConversationStatusActive))
}

func (s *PostgresStore) agentOwnerSubject(
	ctx context.Context,
	principal UserPrincipal,
	ownerType string,
	ownerID string,
	fallbackOwnerUserID string,
) (domain.AgentOwnerSubject, error) {
	switch strings.ToLower(strings.TrimSpace(ownerType)) {
	case "", "user":
		userID := firstNonEmpty(strings.TrimSpace(ownerID), fallbackOwnerUserID)
		user, err := s.GetUser(ctx, userID)
		if err != nil {
			return domain.AgentOwnerSubject{}, err
		}
		return domain.AgentOwnerSubject{Kind: "user", User: &user}, nil
	case "team":
		team, err := s.visibleTeamSummary(ctx, principal, strings.TrimSpace(ownerID))
		if err != nil {
			return domain.AgentOwnerSubject{}, err
		}
		return domain.AgentOwnerSubject{Kind: "team", Team: &team}, nil
	default:
		return domain.AgentOwnerSubject{}, ErrNotFound
	}
}

func (s *PostgresStore) visibleTeamSummary(
	ctx context.Context,
	principal UserPrincipal,
	teamID string,
) (domain.TeamSummary, error) {
	teams, err := s.ListTeams(ctx, principal)
	if err != nil {
		return domain.TeamSummary{}, err
	}
	for _, team := range teams {
		if team.TeamID == teamID {
			return team, nil
		}
	}
	return domain.TeamSummary{}, ErrNotFound
}

func (s *PostgresStore) getConversationInvocation(
	ctx context.Context,
	invocationID string,
) (domain.ConversationAgentInvocation, error) {
	return scanConversationAgentInvocation(s.db.QueryRowContext(ctx, `
		SELECT invocation_id, conversation_id, COALESCE(parent_invocation_id, ''),
			source_representative_agent_id, source_runtime_agent_id, source_session_id,
			target_representative_agent_id, target_runtime_agent_id, target_session_id,
			receipt_token_hash,
			requested_by_user_id, access_mode, selected_message_ids_json, max_turns,
			remaining_turns, status, created_at, expires_at, revoked_at
		FROM conversation_agent_invocations
		WHERE invocation_id = $1
	`, invocationID))
}

func (s *PostgresStore) getActiveConversationInvocation(
	ctx context.Context,
	targetRuntimeAgentID string,
	targetSessionID string,
) (domain.ConversationAgentInvocation, error) {
	return scanConversationAgentInvocation(s.db.QueryRowContext(ctx, `
		SELECT invocation_id, conversation_id, COALESCE(parent_invocation_id, ''),
			source_representative_agent_id, source_runtime_agent_id, source_session_id,
			target_representative_agent_id, target_runtime_agent_id, target_session_id,
			receipt_token_hash,
			requested_by_user_id, access_mode, selected_message_ids_json, max_turns,
			remaining_turns, status, created_at, expires_at, revoked_at
		FROM conversation_agent_invocations
		WHERE target_runtime_agent_id = $1
			AND target_session_id = $2
			AND status = $3
		ORDER BY created_at DESC
		LIMIT 1
	`, targetRuntimeAgentID, targetSessionID, domain.ConversationAgentInvocationStatusActive))
}

func (s *PostgresStore) getActiveConversationInvocationForReply(
	ctx context.Context,
	targetRuntimeAgentID string,
	targetRepresentativeAgentID string,
	targetSessionID string,
	invocationID string,
) (domain.ConversationAgentInvocation, error) {
	invocationID = strings.TrimSpace(invocationID)
	targetRepresentativeAgentID = strings.TrimSpace(targetRepresentativeAgentID)
	targetSessionID = strings.TrimSpace(targetSessionID)
	if invocationID != "" {
		invocation, err := s.getConversationInvocation(ctx, invocationID)
		if err != nil {
			return domain.ConversationAgentInvocation{}, err
		}
		if !activeInvocationMatchesReply(
			invocation,
			targetRuntimeAgentID,
			targetRepresentativeAgentID,
			targetSessionID,
		) {
			return domain.ConversationAgentInvocation{}, ErrNotFound
		}
		return invocation, nil
	}
	if targetSessionID != "" {
		invocation, err := s.getActiveConversationInvocation(
			ctx,
			targetRuntimeAgentID,
			targetSessionID,
		)
		if err == nil {
			return invocation, nil
		}
		return domain.ConversationAgentInvocation{}, err
	}

	query := `
		SELECT invocation_id, conversation_id, COALESCE(parent_invocation_id, ''),
			source_representative_agent_id, source_runtime_agent_id, source_session_id,
			target_representative_agent_id, target_runtime_agent_id, target_session_id,
			receipt_token_hash,
			requested_by_user_id, access_mode, selected_message_ids_json, max_turns,
			remaining_turns, status, created_at, expires_at, revoked_at
		FROM conversation_agent_invocations
		WHERE target_runtime_agent_id = $1
			AND status = $2`
	args := []any{targetRuntimeAgentID, domain.ConversationAgentInvocationStatusActive}
	if targetRepresentativeAgentID != "" {
		args = append(args, targetRepresentativeAgentID)
		query += ` AND target_representative_agent_id = $3`
	}
	query += `
		ORDER BY created_at DESC
		LIMIT 2`
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return domain.ConversationAgentInvocation{}, err
	}
	defer func() { _ = rows.Close() }()

	invocations := make([]domain.ConversationAgentInvocation, 0, 2)
	for rows.Next() {
		invocation, err := scanConversationAgentInvocation(rows)
		if err != nil {
			return domain.ConversationAgentInvocation{}, err
		}
		invocations = append(invocations, invocation)
	}
	if err := rows.Err(); err != nil {
		return domain.ConversationAgentInvocation{}, err
	}
	switch len(invocations) {
	case 0:
		return domain.ConversationAgentInvocation{}, ErrNotFound
	case 1:
		return invocations[0], nil
	default:
		return domain.ConversationAgentInvocation{}, ErrConflict
	}
}

func (s *PostgresStore) agentConversationUsersCanInteract(
	ctx context.Context,
	sourceUserID string,
	targetUserID string,
) bool {
	if sourceUserID == "" || targetUserID == "" {
		return false
	}
	if sourceUserID == targetUserID {
		return true
	}
	var ok bool
	_ = s.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM team_members source_member
			JOIN team_members target_member ON target_member.team_id = source_member.team_id
			JOIN teams t ON t.team_id = source_member.team_id
			WHERE source_member.user_id = $1
				AND target_member.user_id = $2
				AND source_member.status = $3
				AND target_member.status = $3
				AND t.status = $4
		)
	`, sourceUserID, targetUserID, domain.TeamMemberStatusActive, domain.TeamStatusActive).Scan(&ok)
	return ok
}

func (s *PostgresStore) upsertAgentConversation(
	ctx context.Context,
	conversationID string,
	ownerUserID string,
	now time.Time,
) (domain.Conversation, error) {
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO conversations (
			conversation_id, conversation_type, boundary_type, boundary_id,
			history_policy, status, created_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT (conversation_id) DO UPDATE SET
			status = COALESCE(conversations.status, EXCLUDED.status)
		RETURNING conversation_id, conversation_type, boundary_type, boundary_id,
			history_policy, status, created_at, archived_at
	`, conversationID, domain.ConversationTypeAgentThread, "personal", ownerUserID,
		domain.ConversationHistoryFullHistory, domain.ConversationStatusActive, now)
	return scanConversation(row)
}

func (s *PostgresStore) hasActiveConversationMembership(
	ctx context.Context,
	conversationID string,
	userID string,
) (bool, error) {
	var ok bool
	if err := s.db.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM conversation_members
			WHERE conversation_id = $1 AND user_id = $2 AND left_at IS NULL
		)
	`, conversationID, userID).Scan(&ok); err != nil {
		return false, err
	}
	return ok, nil
}

func (s *PostgresStore) upsertConversationMember(
	ctx context.Context,
	conversationID string,
	userID string,
	role string,
	now time.Time,
) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO conversation_members (conversation_id, user_id, role, joined_at)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (conversation_id, user_id) DO UPDATE SET
			role = COALESCE(conversation_members.role, EXCLUDED.role),
			left_at = NULL
	`, conversationID, userID, role, now)
	return err
}

func (s *PostgresStore) upsertConversationAgentBinding(
	ctx context.Context,
	conversationID string,
	representativeAgentID string,
	addedByUserID string,
	relationshipType string,
	now time.Time,
) (domain.ConversationAgentBinding, error) {
	bindingID := deterministicConversationBindingID(conversationID, representativeAgentID)
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO conversation_agent_bindings (
			binding_id, conversation_id, representative_agent_id, relationship_type,
			added_by_user_id, access_mode, status, created_at
		)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (binding_id) DO UPDATE SET
			relationship_type = EXCLUDED.relationship_type,
			status = $7,
			archived_at = NULL
		RETURNING binding_id, conversation_id, representative_agent_id, relationship_type,
			added_by_user_id, access_mode, status, created_at, archived_at
	`, bindingID, conversationID, representativeAgentID, relationshipType, addedByUserID,
		domain.ConversationAgentAccessFromBinding, domain.ConversationAgentBindingStatusActive, now)
	return scanConversationAgentBinding(row)
}

func (s *PostgresStore) createConversationSession(
	ctx context.Context,
	agent Agent,
	rep domain.RepresentativeAgent,
	conversationID string,
	createdByUserID string,
	now time.Time,
) (AgentSession, error) {
	sessionID, err := newSecret("sess")
	if err != nil {
		return AgentSession{}, err
	}
	req := CreateSessionRequest{
		NodeID:                agent.NodeID,
		AgentID:               agent.AgentID,
		SessionID:             sessionID,
		ConversationID:        conversationID,
		ProfileID:             rep.ProfileID,
		RepresentativeAgentID: rep.RepresentativeAgentID,
		CreatedByUserID:       createdByUserID,
		AgentType:             agent.AgentType,
		Source:                domain.MessageSourceACPTunnel,
	}
	principal := UserPrincipal{User: User{UserID: agent.OwnerUserID}}
	session, err := s.CreateNodeAgentSession(ctx, principal, req)
	if err != nil {
		return AgentSession{}, err
	}
	session.CreatedAt = now
	return session, nil
}

func (s *PostgresStore) ensureConversationSession(
	ctx context.Context,
	agent Agent,
	rep domain.RepresentativeAgent,
	conversationID string,
	createdByUserID string,
	sessionID string,
	now time.Time,
) (AgentSession, error) {
	if strings.TrimSpace(sessionID) == "" {
		return s.createConversationSession(ctx, agent, rep, conversationID, createdByUserID, now)
	}
	req := CreateSessionRequest{
		NodeID:                agent.NodeID,
		AgentID:               agent.AgentID,
		SessionID:             strings.TrimSpace(sessionID),
		ConversationID:        conversationID,
		ProfileID:             rep.ProfileID,
		RepresentativeAgentID: rep.RepresentativeAgentID,
		CreatedByUserID:       createdByUserID,
		AgentType:             agent.AgentType,
		Source:                domain.MessageSourceACPTunnel,
	}
	principal := UserPrincipal{User: User{UserID: agent.OwnerUserID}}
	session, err := s.CreateNodeAgentSession(ctx, principal, req)
	if err != nil {
		return AgentSession{}, err
	}
	session.CreatedAt = now
	return session, nil
}

func (s *PostgresStore) createConversationInvocation(
	ctx context.Context,
	conversationID string,
	parentInvocationID string,
	sourceRepresentativeAgentID string,
	sourceRuntimeAgentID string,
	sourceSessionID string,
	targetRepresentativeAgentID string,
	targetRuntimeAgentID string,
	targetSessionID string,
	requestedByUserID string,
	maxTurns int,
	receiptTokenHash string,
	now time.Time,
) (domain.ConversationAgentInvocation, error) {
	invocationID, err := newSecret("inv")
	if err != nil {
		return domain.ConversationAgentInvocation{}, err
	}
	maxTurns = normalizedMaxTurns(maxTurns)
	row := s.db.QueryRowContext(ctx, `
		INSERT INTO conversation_agent_invocations (
			invocation_id, conversation_id, parent_invocation_id,
			source_representative_agent_id, source_runtime_agent_id, source_session_id,
			target_representative_agent_id, target_runtime_agent_id, target_session_id,
			receipt_token_hash,
			representative_agent_id, session_id, requested_by_user_id, access_mode,
			selected_message_ids_json, max_turns, remaining_turns, status, created_at
		)
		VALUES ($1,$2,NULLIF($3,''),
			$4,$5,$6,
			$7,$8,$9,
			$10,
			$7,$9,$11,$12,
			'[]'::jsonb,$13,$13,$14,$15)
		RETURNING invocation_id, conversation_id, COALESCE(parent_invocation_id, ''),
			source_representative_agent_id, source_runtime_agent_id, source_session_id,
			target_representative_agent_id, target_runtime_agent_id, target_session_id,
			receipt_token_hash,
			requested_by_user_id, access_mode, selected_message_ids_json, max_turns,
			remaining_turns, status, created_at, expires_at, revoked_at
	`, invocationID, conversationID, parentInvocationID,
		sourceRepresentativeAgentID, sourceRuntimeAgentID, sourceSessionID,
		targetRepresentativeAgentID, targetRuntimeAgentID, targetSessionID,
		receiptTokenHash, requestedByUserID, domain.ConversationAgentAccessCurrentTurn, maxTurns,
		domain.ConversationAgentInvocationStatusActive, now)
	invocation, err := scanConversationAgentInvocation(row)
	if err != nil {
		if isUniqueViolation(err) {
			return domain.ConversationAgentInvocation{}, ErrConflict
		}
		return domain.ConversationAgentInvocation{}, err
	}
	return invocation, nil
}

func (s *PostgresStore) createConversationPromptMessage(
	ctx context.Context,
	conversationID string,
	agent Agent,
	sessionID string,
	input string,
	display paxInvocationPromptDisplay,
	now time.Time,
) (domain.MessageWithParts, error) {
	messageID, err := newSecret("msg")
	if err != nil {
		return domain.MessageWithParts{}, err
	}
	raw, _ := json.Marshal(map[string]string{"input": input})
	msg := Message{
		MessageID:      messageID,
		ConversationID: conversationID,
		OwnerUserID:    agent.OwnerUserID,
		NodeID:         agent.NodeID,
		AgentID:        agent.AgentID,
		SessionID:      sessionID,
		Source:         domain.MessageSourceACPTunnel,
		Direction:      domain.MessageDirectionUserToAgent,
		Role:           "user",
		Status:         "sent",
		MessageType:    domain.MessageTypePaxUser,
		LogicalKey:     "agent_conversation:" + messageID,
		RawJSON:        raw,
		CreatedAt:      now,
	}
	if err := s.UpsertMessage(ctx, &msg); err != nil {
		return domain.MessageWithParts{}, err
	}
	part := MessagePart{
		MessageID:   msg.MessageID,
		PartIndex:   0,
		PartType:    domain.MessagePartText,
		Text:        input,
		PayloadJSON: raw,
		CreatedAt:   now,
	}
	if err := s.UpsertMessagePart(ctx, &part); err != nil {
		return domain.MessageWithParts{}, err
	}
	if err := s.createPaxInvocationDisplayMessage(ctx, msg, display, now); err != nil {
		return domain.MessageWithParts{}, err
	}
	return domain.MessageWithParts{Message: msg, Parts: []MessagePart{part}}, nil
}

func (s *PostgresStore) createPaxInvocationDisplayMessage(
	ctx context.Context,
	parent Message,
	display paxInvocationPromptDisplay,
	now time.Time,
) error {
	if display.InvocationID == "" {
		return nil
	}
	if display.Side == "source" {
		if toolParent, toolCallID, ok := s.latestPaxInvocationToolCall(ctx, parent); ok {
			return s.createPaxInvocationPendingMessage(
				ctx,
				parent,
				toolParent,
				toolCallID,
				display,
				now,
			)
		}
	}
	messageID, err := newSecret("msg")
	if err != nil {
		return err
	}
	raw, text := paxInvocationDisplayPayload(parent.MessageID, []string{parent.MessageID}, display)
	msg := Message{
		MessageID:       messageID,
		ConversationID:  parent.ConversationID,
		OwnerUserID:     parent.OwnerUserID,
		NodeID:          parent.NodeID,
		AgentID:         parent.AgentID,
		SessionID:       parent.SessionID,
		Source:          parent.Source,
		Direction:       parent.Direction,
		Role:            parent.Role,
		Status:          parent.Status,
		MessageType:     domain.MessageTypePaxInvocation,
		ParentMessageID: parent.MessageID,
		LogicalKey:      paxInvocationDisplayLogicalKey(display),
		RawJSON:         raw,
		CreatedAt:       now,
	}
	if err := s.UpsertMessage(ctx, &msg); err != nil {
		return err
	}
	return s.UpsertMessagePart(ctx, &MessagePart{
		MessageID:   msg.MessageID,
		PartIndex:   0,
		PartType:    domain.MessagePartText,
		Text:        text,
		PayloadJSON: raw,
		CreatedAt:   now,
	})
}

func (s *PostgresStore) createPaxInvocationPendingMessage(
	ctx context.Context,
	prompt Message,
	toolParent Message,
	toolCallID string,
	display paxInvocationPromptDisplay,
	now time.Time,
) error {
	messageID, err := newSecret("msg")
	if err != nil {
		return err
	}
	raw, text := paxInvocationPendingPayload(
		prompt.MessageID,
		toolParent.MessageID,
		toolCallID,
		display,
	)
	msg := Message{
		MessageID:       messageID,
		ConversationID:  prompt.ConversationID,
		OwnerUserID:     prompt.OwnerUserID,
		NodeID:          prompt.NodeID,
		AgentID:         prompt.AgentID,
		SessionID:       prompt.SessionID,
		Source:          prompt.Source,
		Direction:       prompt.Direction,
		Role:            prompt.Role,
		Status:          "pending",
		MessageType:     domain.MessageTypePaxInvocationPending,
		ParentMessageID: toolParent.MessageID,
		LogicalKey:      paxInvocationPendingLogicalKey(display),
		RawJSON:         raw,
		CreatedAt:       now,
	}
	if err := s.UpsertMessage(ctx, &msg); err != nil {
		return err
	}
	return s.UpsertMessagePart(ctx, &MessagePart{
		MessageID:   msg.MessageID,
		PartIndex:   0,
		PartType:    domain.MessagePartText,
		Text:        text,
		PayloadJSON: raw,
		CreatedAt:   now,
	})
}

func (s *PostgresStore) latestPaxInvocationToolCall(
	ctx context.Context,
	parent Message,
) (Message, string, bool) {
	if parent.AgentID == "" || parent.SessionID == "" {
		return Message{}, "", false
	}
	messages, err := s.paxInvocationSessionMessages(ctx, parent.AgentID, parent.SessionID)
	if err != nil {
		return Message{}, "", false
	}
	return latestPaxInvocationToolCall(messages)
}

func (s *PostgresStore) paxInvocationSessionMessages(
	ctx context.Context,
	agentID string,
	sessionID string,
) ([]Message, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+messageReturningSQL+`
		FROM messages
		WHERE agent_id = $1 AND session_id = $2
		ORDER BY id ASC
		LIMIT 100
	`, agentID, sessionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	messages := make([]Message, 0)
	for rows.Next() {
		message, err := scanMessage(rows)
		if err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, rows.Err()
}

const representativeAgentSelectSQL = `
	SELECT representative_agents.representative_agent_id, representative_agents.profile_id, representative_agents.runtime_agent_id, representative_agents.represents_type,
		representative_agents.represents_id, representative_agents.approval_policy_id,
		representative_agents.status, representative_agents.created_by_user_id,
		representative_agents.created_at, representative_agents.updated_at,
		representative_agents.archived_at
	FROM representative_agents`

func scanAgentProfile(row interface{ Scan(dest ...any) error }) (domain.AgentProfile, error) {
	var profile domain.AgentProfile
	if err := row.Scan(
		&profile.ProfileID,
		&profile.OwnerType,
		&profile.OwnerID,
		&profile.DisplayName,
		&profile.Description,
		&profile.Card,
		&profile.InstructionsMD,
		&profile.DefaultModel,
		&profile.ToolPolicy,
		&profile.Metadata,
		&profile.Status,
		&profile.CreatedByUserID,
		&profile.CreatedAt,
		&profile.UpdatedAt,
		&profile.ArchivedAt,
	); err != nil {
		return domain.AgentProfile{}, mapSQLError(err)
	}
	return profile, nil
}

func scanRepresentativeAgent(
	row interface{ Scan(dest ...any) error },
) (domain.RepresentativeAgent, error) {
	var rep domain.RepresentativeAgent
	if err := row.Scan(
		&rep.RepresentativeAgentID,
		&rep.ProfileID,
		&rep.RuntimeAgentID,
		&rep.RepresentsType,
		&rep.RepresentsID,
		&rep.ApprovalPolicyID,
		&rep.Status,
		&rep.CreatedByUserID,
		&rep.CreatedAt,
		&rep.UpdatedAt,
		&rep.ArchivedAt,
	); err != nil {
		return domain.RepresentativeAgent{}, mapSQLError(err)
	}
	return rep, nil
}

func scanConversation(row interface{ Scan(dest ...any) error }) (domain.Conversation, error) {
	var conv domain.Conversation
	if err := row.Scan(
		&conv.ConversationID,
		&conv.ConversationType,
		&conv.BoundaryType,
		&conv.BoundaryID,
		&conv.HistoryPolicy,
		&conv.Status,
		&conv.CreatedAt,
		&conv.ArchivedAt,
	); err != nil {
		return domain.Conversation{}, mapSQLError(err)
	}
	return conv, nil
}

func scanConversationAgentBinding(
	row interface{ Scan(dest ...any) error },
) (domain.ConversationAgentBinding, error) {
	var binding domain.ConversationAgentBinding
	if err := row.Scan(
		&binding.BindingID,
		&binding.ConversationID,
		&binding.RepresentativeAgentID,
		&binding.RelationshipType,
		&binding.AddedByUserID,
		&binding.AccessMode,
		&binding.Status,
		&binding.CreatedAt,
		&binding.ArchivedAt,
	); err != nil {
		return domain.ConversationAgentBinding{}, mapSQLError(err)
	}
	return binding, nil
}

func scanConversationAgentInvocation(
	row interface{ Scan(dest ...any) error },
) (domain.ConversationAgentInvocation, error) {
	var invocation domain.ConversationAgentInvocation
	if err := row.Scan(
		&invocation.InvocationID,
		&invocation.ConversationID,
		&invocation.ParentInvocationID,
		&invocation.SourceRepresentativeAgentID,
		&invocation.SourceRuntimeAgentID,
		&invocation.SourceSessionID,
		&invocation.TargetRepresentativeAgentID,
		&invocation.TargetRuntimeAgentID,
		&invocation.TargetSessionID,
		&invocation.ReceiptTokenHash,
		&invocation.RequestedByUserID,
		&invocation.AccessMode,
		&invocation.SelectedMessageIDsJSON,
		&invocation.MaxTurns,
		&invocation.RemainingTurns,
		&invocation.Status,
		&invocation.CreatedAt,
		&invocation.ExpiresAt,
		&invocation.RevokedAt,
	); err != nil {
		return domain.ConversationAgentInvocation{}, mapSQLError(err)
	}
	return invocation, nil
}

func deterministicConversationBindingID(
	conversationID string,
	representativeAgentID string,
) string {
	sum := sha256.Sum256([]byte(conversationID + ":" + representativeAgentID))
	return "bind_" + hex.EncodeToString(sum[:])[:24]
}

func hashConversationReceiptToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func deterministicAgentProfileID(runtimeAgentID string, ownerUserID string) string {
	sum := sha256.Sum256([]byte(runtimeAgentID + ":" + ownerUserID))
	return "prof_" + hex.EncodeToString(sum[:])[:24]
}

func deterministicRepresentativeAgentID(
	runtimeAgentID string,
	profileID string,
	representsType string,
	representsID string,
) string {
	sum := sha256.Sum256(
		[]byte(runtimeAgentID + ":" + profileID + ":" + representsType + ":" + representsID),
	)
	return "rep_" + hex.EncodeToString(sum[:])[:24]
}

func normalizedMaxTurns(maxTurns int) int {
	if maxTurns <= 0 {
		return defaultAgentConversationMaxTurns
	}
	if maxTurns > maxAgentConversationMaxTurns {
		return maxAgentConversationMaxTurns
	}
	return maxTurns
}

func sourceInvocationPromptDisplay(
	invocation domain.ConversationAgentInvocation,
	sourceRepresentativeAgentID string,
	sourceAgentID string,
	sourceSessionID string,
	targetRepresentativeAgentID string,
	targetAgentID string,
	targetSessionID string,
	phase string,
	side string,
	displayText string,
	originalText string,
) paxInvocationPromptDisplay {
	return paxInvocationPromptDisplay{
		InvocationID: invocation.InvocationID,
		Phase:        phase,
		Side:         side,
		Sender: paxInvocationEndpoint{
			RepresentativeAgentID: sourceRepresentativeAgentID,
			AgentID:               sourceAgentID,
			SessionID:             sourceSessionID,
		},
		Receiver: paxInvocationEndpoint{
			RepresentativeAgentID: targetRepresentativeAgentID,
			AgentID:               targetAgentID,
			SessionID:             targetSessionID,
		},
		DisplayText:     strings.TrimSpace(displayText),
		OriginalText:    strings.TrimSpace(originalText),
		LogicalKeyScope: phase + ":" + side,
	}
}

func paxInvocationDisplayPayload(
	parentMessageID string,
	replacesMessageIDs []string,
	display paxInvocationPromptDisplay,
) (json.RawMessage, string) {
	text := firstNonEmpty(
		strings.TrimSpace(display.DisplayText),
		strings.TrimSpace(display.OriginalText),
		"Pax agent conversation update.",
	)
	if len(replacesMessageIDs) == 0 {
		replacesMessageIDs = []string{parentMessageID}
	}
	raw, _ := json.Marshal(paxInvocationDisplayRaw{
		InvocationID:      display.InvocationID,
		InvocationType:    "agent_conversation",
		Phase:             display.Phase,
		Side:              display.Side,
		ReplacesMessageID: replacesMessageIDs,
		Sender:            display.Sender,
		Receiver:          display.Receiver,
		Content: paxInvocationDisplayContent{
			DisplayText:  text,
			OriginalText: strings.TrimSpace(display.OriginalText),
		},
	})
	return raw, text
}

func paxInvocationPendingPayload(
	promptMessageID string,
	toolCallMessageID string,
	toolCallID string,
	display paxInvocationPromptDisplay,
) (json.RawMessage, string) {
	text := firstNonEmpty(
		strings.TrimSpace(display.DisplayText),
		strings.TrimSpace(display.OriginalText),
		"Pax agent conversation update.",
	)
	raw, _ := json.Marshal(paxInvocationPendingRaw{
		InvocationID:      display.InvocationID,
		InvocationType:    "agent_conversation",
		Phase:             display.Phase,
		Side:              display.Side,
		ToolCallID:        strings.TrimSpace(toolCallID),
		PromptMessageID:   promptMessageID,
		ReplacesMessageID: uniqueNonEmptyStrings(toolCallMessageID, promptMessageID),
		Sender:            display.Sender,
		Receiver:          display.Receiver,
		Content: paxInvocationDisplayContent{
			DisplayText:  text,
			OriginalText: strings.TrimSpace(display.OriginalText),
		},
	})
	return raw, text
}

func paxInvocationPendingLogicalKey(display paxInvocationPromptDisplay) string {
	return strings.TrimSuffix(paxInvocationDisplayLogicalKey(display), ":display") + ":pending"
}

func latestPaxInvocationToolCall(messages []Message) (Message, string, bool) {
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if message.MessageType != "tool_call" {
			continue
		}
		id := toolCallIDFromRaw(message.RawJSON)
		if id == "" {
			continue
		}
		return message, id, true
	}
	return Message{}, "", false
}

func toolCallIDFromRaw(raw json.RawMessage) string {
	var rpc struct {
		Params struct {
			Update map[string]any `json:"update"`
		} `json:"params"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &rpc) != nil || rpc.Params.Update == nil {
		return ""
	}
	return firstNonEmpty(
		stringMapField(rpc.Params.Update, "toolCallId"),
		stringMapField(rpc.Params.Update, "tool_call_id"),
	)
}

func stringMapField(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}

func paxInvocationDisplayLogicalKey(display paxInvocationPromptDisplay) string {
	scope := strings.TrimSpace(display.LogicalKeyScope)
	if scope == "" {
		scope = display.Phase + ":" + display.Side
	}
	return "agent_conversation:" + display.InvocationID + ":" + scope + ":display"
}

func conversationAgentLabel(agent Agent) string {
	return firstNonEmpty(
		strings.TrimSpace(agent.Name),
		strings.TrimSpace(agent.AgentID),
		"the agent",
	)
}

func conversationDeliveryPromptText(spec conversationDeliverySpec) string {
	if text := strings.TrimSpace(spec.instruction); text != "" {
		return text
	}
	if text := strings.TrimSpace(spec.reason); text != "" {
		return text
	}
	return "Conversation delivery requested."
}

func (s *MemoryStore) ListRepresentativeAgents(
	ctx context.Context,
	principal UserPrincipal,
	runtimeAgentID string,
) ([]domain.RepresentativeAgent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]domain.RepresentativeAgent, 0)
	for _, rep := range s.representativeAgents {
		if runtimeAgentID != "" && rep.RuntimeAgentID != runtimeAgentID {
			continue
		}
		agent, ok := s.agents[rep.RuntimeAgentID]
		if !ok || !s.canAccessAgentLocked(principal, agent) {
			continue
		}
		out = append(out, rep)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

func (s *MemoryStore) GetAgentOwnerInfo(
	ctx context.Context,
	principal UserPrincipal,
	req domain.AgentOwnerInfoRequest,
) (domain.AgentOwnerInfo, error) {
	agentID := strings.TrimSpace(req.AgentID)
	representativeAgentID := strings.TrimSpace(req.RepresentativeAgentID)
	if representativeAgentID != "" {
		s.mu.Lock()
		rep, ok := s.representativeAgents[representativeAgentID]
		if !ok || rep.Status != domain.ConversationStatusActive {
			s.mu.Unlock()
			return domain.AgentOwnerInfo{}, ErrNotFound
		}
		profile, ok := s.agentProfiles[rep.ProfileID]
		if !ok || profile.Status != domain.ConversationStatusActive {
			s.mu.Unlock()
			return domain.AgentOwnerInfo{}, ErrNotFound
		}
		s.mu.Unlock()
		if agentID != "" && agentID != rep.RuntimeAgentID {
			return domain.AgentOwnerInfo{}, ErrNotFound
		}
		agent, err := s.GetAgent(ctx, principal, rep.RuntimeAgentID)
		if err != nil {
			return domain.AgentOwnerInfo{}, err
		}
		owner, err := s.memoryAgentOwnerSubject(
			ctx,
			principal,
			rep.RepresentsType,
			rep.RepresentsID,
			agent.OwnerUserID,
		)
		if err != nil {
			return domain.AgentOwnerInfo{}, err
		}
		return domain.AgentOwnerInfo{
			Agent:               agent,
			RepresentativeAgent: &rep,
			Profile:             &profile,
			Owner:               owner,
		}, nil
	}
	if agentID == "" {
		return domain.AgentOwnerInfo{}, ErrNotFound
	}
	agent, err := s.GetAgent(ctx, principal, agentID)
	if err != nil {
		return domain.AgentOwnerInfo{}, err
	}
	owner, err := s.memoryAgentOwnerSubject(
		ctx,
		principal,
		"user",
		agent.OwnerUserID,
		agent.OwnerUserID,
	)
	if err != nil {
		return domain.AgentOwnerInfo{}, err
	}
	return domain.AgentOwnerInfo{Agent: agent, Owner: owner}, nil
}

func (s *MemoryStore) UpsertRepresentativeAgent(
	ctx context.Context,
	principal UserPrincipal,
	req domain.UpsertRepresentativeAgentRequest,
) (domain.RepresentativeAgent, domain.AgentProfile, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	agent, ok := s.agents[strings.TrimSpace(req.RuntimeAgentID)]
	if !ok || !s.canAccessAgentLocked(principal, agent) {
		return domain.RepresentativeAgent{}, domain.AgentProfile{}, ErrNotFound
	}
	if !canAccessOwner(principal, agent.OwnerUserID) {
		return domain.RepresentativeAgent{}, domain.AgentProfile{}, ErrUnauthorized
	}
	now := s.now().UTC()
	profileID := strings.TrimSpace(req.ProfileID)
	if profileID == "" {
		profileID = deterministicAgentProfileID(agent.AgentID, agent.OwnerUserID)
	}
	representsType := firstNonEmpty(strings.TrimSpace(req.RepresentsType), "user")
	representsID, err := s.validateRepresentsSubjectLocked(
		representsType,
		strings.TrimSpace(req.RepresentsID),
		agent.OwnerUserID,
	)
	if err != nil {
		return domain.RepresentativeAgent{}, domain.AgentProfile{}, err
	}
	displayName := firstNonEmpty(strings.TrimSpace(req.DisplayName), agent.Name, agent.AgentID)
	profile := s.agentProfiles[profileID]
	if profile.ProfileID != "" && profile.OwnerID != agent.OwnerUserID {
		return domain.RepresentativeAgent{}, domain.AgentProfile{}, ErrConflict
	}
	if profile.ProfileID == "" {
		profile = domain.AgentProfile{
			ProfileID:       profileID,
			OwnerType:       "user",
			OwnerID:         agent.OwnerUserID,
			CreatedByUserID: principal.User.UserID,
			CreatedAt:       now,
		}
	}
	profile.DisplayName = displayName
	profile.Description = strings.TrimSpace(req.Description)
	profile.Card = jsonDefault(req.Card, "{}")
	profile.Metadata = jsonDefault(req.Metadata, "{}")
	profile.ToolPolicy = json.RawMessage(`{}`)
	profile.Status = domain.ConversationStatusActive
	profile.UpdatedAt = now
	profile.ArchivedAt = nil
	s.agentProfiles[profileID] = profile

	repID := deterministicRepresentativeAgentID(
		agent.AgentID,
		profileID,
		representsType,
		representsID,
	)
	rep := s.representativeAgents[repID]
	if rep.RepresentativeAgentID == "" {
		rep = domain.RepresentativeAgent{
			RepresentativeAgentID: repID,
			CreatedAt:             now,
		}
	}
	rep.ProfileID = profileID
	rep.RuntimeAgentID = agent.AgentID
	rep.RepresentsType = representsType
	rep.RepresentsID = representsID
	rep.ApprovalPolicyID = strings.TrimSpace(req.ApprovalPolicyID)
	rep.Status = domain.ConversationStatusActive
	rep.CreatedByUserID = principal.User.UserID
	rep.UpdatedAt = now
	rep.ArchivedAt = nil
	s.representativeAgents[repID] = rep
	return rep, profile, nil
}

func (s *MemoryStore) validateRepresentsSubjectLocked(
	representsType string,
	representsID string,
	ownerUserID string,
) (string, error) {
	switch representsType {
	case "user":
		if representsID == "" {
			return ownerUserID, nil
		}
		if representsID != ownerUserID {
			return "", ErrUnauthorized
		}
		return representsID, nil
	case "team":
		if representsID == "" {
			return "", ErrConflict
		}
		team, ok := s.teams[representsID]
		if !ok || team.Status != domain.TeamStatusActive {
			return "", ErrUnauthorized
		}
		member, ok := s.teamMembers[teamMemberKey{TeamID: representsID, UserID: ownerUserID}]
		if !ok || member.Status != domain.TeamMemberStatusActive {
			return "", ErrUnauthorized
		}
		return representsID, nil
	default:
		return "", ErrConflict
	}
}

func (s *MemoryStore) memoryAgentOwnerSubject(
	ctx context.Context,
	principal UserPrincipal,
	ownerType string,
	ownerID string,
	fallbackOwnerUserID string,
) (domain.AgentOwnerSubject, error) {
	switch strings.ToLower(strings.TrimSpace(ownerType)) {
	case "", "user":
		userID := firstNonEmpty(strings.TrimSpace(ownerID), fallbackOwnerUserID)
		user, err := s.GetUser(ctx, userID)
		if err != nil {
			return domain.AgentOwnerSubject{}, err
		}
		return domain.AgentOwnerSubject{Kind: "user", User: &user}, nil
	case "team":
		team, err := s.memoryVisibleTeamSummary(ctx, principal, strings.TrimSpace(ownerID))
		if err != nil {
			return domain.AgentOwnerSubject{}, err
		}
		return domain.AgentOwnerSubject{Kind: "team", Team: &team}, nil
	default:
		return domain.AgentOwnerSubject{}, ErrNotFound
	}
}

func (s *MemoryStore) memoryVisibleTeamSummary(
	ctx context.Context,
	principal UserPrincipal,
	teamID string,
) (domain.TeamSummary, error) {
	teams, err := s.ListTeams(ctx, principal)
	if err != nil {
		return domain.TeamSummary{}, err
	}
	for _, team := range teams {
		if team.TeamID == teamID {
			return team, nil
		}
	}
	return domain.TeamSummary{}, ErrNotFound
}

func (s *MemoryStore) StartAgentConversation(
	ctx context.Context,
	node Node,
	req domain.StartAgentConversationRequest,
) (domain.AgentConversationStart, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sourceAgent, ok := s.agents[req.FromRuntimeAgentID]
	if !ok || sourceAgent.NodeID != node.NodeID {
		return domain.AgentConversationStart{}, ErrNotFound
	}
	sourceRep, ok := s.findSourceRepresentativeAgentLocked(
		sourceAgent.AgentID,
		req.FromRepresentativeAgentID,
	)
	if !ok {
		return domain.AgentConversationStart{}, ErrNotFound
	}
	targetRep, ok := s.representativeAgents[req.ToRepresentativeAgentID]
	if !ok || targetRep.Status != domain.ConversationStatusActive {
		return domain.AgentConversationStart{}, ErrNotFound
	}
	targetAgent, ok := s.agents[targetRep.RuntimeAgentID]
	if !ok {
		return domain.AgentConversationStart{}, ErrNotFound
	}
	if !s.agentConversationUsersCanInteractLocked(
		sourceAgent.OwnerUserID,
		targetAgent.OwnerUserID,
	) {
		return domain.AgentConversationStart{}, ErrUnauthorized
	}

	now := s.now().UTC()
	conversationID := strings.TrimSpace(req.ConversationID)
	if conversationID != "" &&
		!s.canReadConversationLocked(sourceAgent.OwnerUserID, conversationID) {
		return domain.AgentConversationStart{}, ErrNotFound
	}
	if conversationID == "" {
		generated, err := newSecret("conv")
		if err != nil {
			return domain.AgentConversationStart{}, err
		}
		conversationID = generated
	}
	conversation := s.upsertAgentConversationLocked(conversationID, sourceAgent.OwnerUserID, now)
	s.upsertConversationMemberLocked(
		conversationID,
		sourceAgent.OwnerUserID,
		domain.ConversationMemberRoleOwner,
		now,
	)
	if targetAgent.OwnerUserID != sourceAgent.OwnerUserID {
		s.upsertConversationMemberLocked(
			conversationID,
			targetAgent.OwnerUserID,
			domain.ConversationMemberRoleMember,
			now,
		)
	}
	sourceBinding := s.upsertConversationAgentBindingLocked(
		conversationID,
		sourceRep.RepresentativeAgentID,
		sourceAgent.OwnerUserID,
		domain.ConversationAgentRelationshipParticipant,
		now,
	)
	targetBinding := s.upsertConversationAgentBindingLocked(
		conversationID,
		targetRep.RepresentativeAgentID,
		sourceAgent.OwnerUserID,
		domain.ConversationAgentRelationshipAssistant,
		now,
	)
	sourceSession, err := s.createConversationSessionLocked(
		sourceAgent,
		sourceRep,
		conversationID,
		sourceAgent.OwnerUserID,
		now,
	)
	if err != nil {
		return domain.AgentConversationStart{}, err
	}
	targetSession, err := s.createConversationSessionLocked(
		targetAgent,
		targetRep,
		conversationID,
		sourceAgent.OwnerUserID,
		now,
	)
	if err != nil {
		return domain.AgentConversationStart{}, err
	}
	invocation, err := s.createConversationInvocationLocked(
		conversationID,
		"",
		sourceRep.RepresentativeAgentID,
		sourceAgent.AgentID,
		sourceSession.SessionID,
		targetRep.RepresentativeAgentID,
		targetAgent.AgentID,
		targetSession.SessionID,
		sourceAgent.OwnerUserID,
		req.MaxTurns,
		"",
		now,
	)
	if err != nil {
		return domain.AgentConversationStart{}, err
	}
	prompt, err := s.createConversationPromptMessageLocked(
		conversationID,
		sourceAgent,
		sourceSession.SessionID,
		req.Input,
		sourceInvocationPromptDisplay(
			invocation,
			sourceRep.RepresentativeAgentID,
			sourceAgent.AgentID,
			sourceSession.SessionID,
			targetRep.RepresentativeAgentID,
			targetAgent.AgentID,
			targetSession.SessionID,
			"inquiry",
			"source",
			"Asked "+conversationAgentLabel(targetAgent)+" for input.",
			req.Input,
		),
		now,
	)
	if err != nil {
		return domain.AgentConversationStart{}, err
	}

	return domain.AgentConversationStart{
		Conversation:         conversation,
		SourceBinding:        sourceBinding,
		TargetBinding:        targetBinding,
		Invocation:           invocation,
		SourceRepresentative: sourceRep,
		TargetRepresentative: targetRep,
		SourceRuntimeAgent:   sourceAgent,
		TargetRuntimeAgent:   targetAgent,
		SourceSession:        sourceSession,
		TargetSession:        targetSession,
		PromptMessage:        prompt,
	}, nil
}

func (s *MemoryStore) DeliverAgentConversation(
	ctx context.Context,
	node Node,
	req domain.DeliverConversationRequest,
) (domain.ConversationDelivery, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	switch req.Target.Kind {
	case domain.ConversationDeliveryTargetRepresentative:
		return s.deliverAgentConversationToRepresentativeLocked(node, req)
	case domain.ConversationDeliveryTargetAgent:
		return s.deliverAgentConversationToAgentLocked(node, req)
	case domain.ConversationDeliveryTargetActiveInvocation:
		return s.deliverAgentConversationActiveInvocationReplyLocked(node, req)
	default:
		return domain.ConversationDelivery{}, ErrNotFound
	}
}

func (s *MemoryStore) deliverAgentConversationToRepresentativeLocked(
	node Node,
	req domain.DeliverConversationRequest,
) (domain.ConversationDelivery, error) {
	sourceAgent, ok := s.agents[req.Source.AgentID]
	if !ok || sourceAgent.NodeID != node.NodeID {
		return domain.ConversationDelivery{}, ErrNotFound
	}
	sourceRep, ok := s.findSourceRepresentativeAgentLocked(
		sourceAgent.AgentID,
		req.Source.RepresentativeAgentID,
	)
	if !ok {
		return domain.ConversationDelivery{}, ErrNotFound
	}
	targetRep, ok := s.representativeAgents[req.Target.RepresentativeAgentID]
	if !ok || targetRep.Status != domain.ConversationStatusActive {
		return domain.ConversationDelivery{}, ErrNotFound
	}
	targetAgent, ok := s.agents[targetRep.RuntimeAgentID]
	if !ok {
		return domain.ConversationDelivery{}, ErrNotFound
	}
	if !s.agentConversationUsersCanInteractLocked(
		sourceAgent.OwnerUserID,
		targetAgent.OwnerUserID,
	) {
		return domain.ConversationDelivery{}, ErrUnauthorized
	}
	conversationID, err := newSecret("conv")
	if err != nil {
		return domain.ConversationDelivery{}, err
	}
	receiptToken, err := newSecret("rcpt")
	if err != nil {
		return domain.ConversationDelivery{}, err
	}
	return s.createConversationDeliveryLocked(conversationDeliverySpec{
		conversationID:   conversationID,
		sourceAgent:      sourceAgent,
		sourceRep:        sourceRep,
		sourceSessionID:  req.Source.SessionID,
		targetAgent:      targetAgent,
		targetRep:        targetRep,
		targetSessionID:  req.Target.SessionID,
		context:          req.Context.Effective(),
		instruction:      req.Instruction,
		reason:           req.Reason,
		receiptToken:     receiptToken,
		receiptTokenHash: hashConversationReceiptToken(receiptToken),
		now:              s.now().UTC(),
	})
}

func (s *MemoryStore) deliverAgentConversationToAgentLocked(
	node Node,
	req domain.DeliverConversationRequest,
) (domain.ConversationDelivery, error) {
	sourceAgent, ok := s.agents[req.Source.AgentID]
	if !ok || sourceAgent.NodeID != node.NodeID {
		return domain.ConversationDelivery{}, ErrNotFound
	}
	targetAgent, ok := s.agents[strings.TrimSpace(req.Target.AgentID)]
	if !ok {
		return domain.ConversationDelivery{}, ErrNotFound
	}
	if !s.agentConversationUsersCanInteractLocked(
		sourceAgent.OwnerUserID,
		targetAgent.OwnerUserID,
	) {
		return domain.ConversationDelivery{}, ErrUnauthorized
	}
	now := s.now().UTC()
	sourceRep := s.ensureCanonicalRepresentativeAgentLocked(sourceAgent, now)
	targetRep := s.ensureCanonicalRepresentativeAgentLocked(targetAgent, now)
	conversationID, err := newSecret("conv")
	if err != nil {
		return domain.ConversationDelivery{}, err
	}
	receiptToken, err := newSecret("rcpt")
	if err != nil {
		return domain.ConversationDelivery{}, err
	}
	return s.createConversationDeliveryLocked(conversationDeliverySpec{
		conversationID:   conversationID,
		sourceAgent:      sourceAgent,
		sourceRep:        sourceRep,
		sourceSessionID:  req.Source.SessionID,
		targetAgent:      targetAgent,
		targetRep:        targetRep,
		targetSessionID:  req.Target.SessionID,
		context:          req.Context.Effective(),
		instruction:      req.Instruction,
		reason:           req.Reason,
		receiptToken:     receiptToken,
		receiptTokenHash: hashConversationReceiptToken(receiptToken),
		now:              now,
	})
}

// ensureCanonicalRepresentativeAgentLocked mirrors the Postgres helper: it
// returns the deterministic self-represents representative for an agent,
// creating it and its profile if absent, and preserving an existing one.
func (s *MemoryStore) ensureCanonicalRepresentativeAgentLocked(
	agent Agent,
	now time.Time,
) domain.RepresentativeAgent {
	profileID := deterministicAgentProfileID(agent.AgentID, agent.OwnerUserID)
	representsType := "user"
	representsID := agent.OwnerUserID
	repID := deterministicRepresentativeAgentID(
		agent.AgentID,
		profileID,
		representsType,
		representsID,
	)

	if profile, ok := s.agentProfiles[profileID]; !ok || profile.ProfileID == "" {
		s.agentProfiles[profileID] = domain.AgentProfile{
			ProfileID:       profileID,
			OwnerType:       "user",
			OwnerID:         agent.OwnerUserID,
			DisplayName:     firstNonEmpty(agent.Name, agent.AgentID),
			Description:     strings.TrimSpace(agent.Description),
			Card:            json.RawMessage(`{}`),
			Metadata:        json.RawMessage(`{}`),
			ToolPolicy:      json.RawMessage(`{}`),
			Status:          domain.ConversationStatusActive,
			CreatedByUserID: agent.OwnerUserID,
			CreatedAt:       now,
			UpdatedAt:       now,
		}
	}

	rep, ok := s.representativeAgents[repID]
	if !ok || rep.RepresentativeAgentID == "" {
		rep = domain.RepresentativeAgent{
			RepresentativeAgentID: repID,
			ProfileID:             profileID,
			RuntimeAgentID:        agent.AgentID,
			RepresentsType:        representsType,
			RepresentsID:          representsID,
			Status:                domain.ConversationStatusActive,
			CreatedByUserID:       agent.OwnerUserID,
			CreatedAt:             now,
			UpdatedAt:             now,
		}
		s.representativeAgents[repID] = rep
	}
	return rep
}

func (s *MemoryStore) deliverAgentConversationActiveInvocationReplyLocked(
	node Node,
	req domain.DeliverConversationRequest,
) (domain.ConversationDelivery, error) {
	sourceAgent, ok := s.agents[req.Source.AgentID]
	if !ok || sourceAgent.NodeID != node.NodeID {
		return domain.ConversationDelivery{}, ErrNotFound
	}
	parent, err := s.activeConversationInvocationForReplyLocked(
		sourceAgent.AgentID,
		req.Source.RepresentativeAgentID,
		req.Source.SessionID,
		req.Target.InvocationID,
	)
	if err != nil {
		return domain.ConversationDelivery{}, err
	}
	if strings.TrimSpace(req.Source.RepresentativeAgentID) != "" &&
		req.Source.RepresentativeAgentID != parent.TargetRepresentativeAgentID {
		return domain.ConversationDelivery{}, ErrUnauthorized
	}
	sourceRep, ok := s.representativeAgents[parent.TargetRepresentativeAgentID]
	if !ok || sourceRep.Status != domain.ConversationStatusActive {
		return domain.ConversationDelivery{}, ErrNotFound
	}
	targetRep, ok := s.representativeAgents[parent.SourceRepresentativeAgentID]
	if !ok || targetRep.Status != domain.ConversationStatusActive {
		return domain.ConversationDelivery{}, ErrNotFound
	}
	targetAgent, ok := s.agents[parent.SourceRuntimeAgentID]
	if !ok {
		return domain.ConversationDelivery{}, ErrNotFound
	}
	now := s.now().UTC()
	return s.completeConversationInvocationReplyLocked(conversationDeliverySpec{
		conversationID:   parent.ConversationID,
		parentInvocation: &parent,
		sourceAgent:      sourceAgent,
		sourceRep:        sourceRep,
		sourceSessionID:  parent.TargetSessionID,
		targetAgent:      targetAgent,
		targetRep:        targetRep,
		targetSessionID:  parent.SourceSessionID,
		context:          req.Context.Effective(),
		instruction:      req.Instruction,
		reason:           req.Reason,
		now:              now,
	})
}

func (s *MemoryStore) createConversationDeliveryLocked(
	spec conversationDeliverySpec,
) (domain.ConversationDelivery, error) {
	conversation := s.upsertAgentConversationLocked(
		spec.conversationID,
		spec.sourceAgent.OwnerUserID,
		spec.now,
	)
	s.upsertConversationMemberLocked(
		spec.conversationID,
		spec.sourceAgent.OwnerUserID,
		domain.ConversationMemberRoleOwner,
		spec.now,
	)
	if spec.targetAgent.OwnerUserID != spec.sourceAgent.OwnerUserID {
		s.upsertConversationMemberLocked(
			spec.conversationID,
			spec.targetAgent.OwnerUserID,
			domain.ConversationMemberRoleMember,
			spec.now,
		)
	}
	s.upsertConversationAgentBindingLocked(
		spec.conversationID,
		spec.sourceRep.RepresentativeAgentID,
		spec.sourceAgent.OwnerUserID,
		domain.ConversationAgentRelationshipParticipant,
		spec.now,
	)
	s.upsertConversationAgentBindingLocked(
		spec.conversationID,
		spec.targetRep.RepresentativeAgentID,
		spec.sourceAgent.OwnerUserID,
		domain.ConversationAgentRelationshipAssistant,
		spec.now,
	)
	sourceSession, err := s.ensureConversationSessionLocked(
		spec.sourceAgent,
		spec.sourceRep,
		spec.conversationID,
		spec.sourceAgent.OwnerUserID,
		spec.sourceSessionID,
		spec.now,
	)
	if err != nil {
		return domain.ConversationDelivery{}, err
	}
	targetSession, err := s.ensureConversationSessionLocked(
		spec.targetAgent,
		spec.targetRep,
		spec.conversationID,
		spec.sourceAgent.OwnerUserID,
		spec.targetSessionID,
		spec.now,
	)
	if err != nil {
		return domain.ConversationDelivery{}, err
	}
	parentInvocationID := ""
	if spec.parentInvocation != nil {
		parentInvocationID = spec.parentInvocation.InvocationID
	}
	invocation, err := s.createConversationInvocationLocked(
		spec.conversationID,
		parentInvocationID,
		spec.sourceRep.RepresentativeAgentID,
		spec.sourceAgent.AgentID,
		sourceSession.SessionID,
		spec.targetRep.RepresentativeAgentID,
		spec.targetAgent.AgentID,
		targetSession.SessionID,
		spec.sourceAgent.OwnerUserID,
		defaultAgentConversationMaxTurns,
		spec.receiptTokenHash,
		spec.now,
	)
	if err != nil {
		return domain.ConversationDelivery{}, err
	}
	prompt, err := s.createConversationPromptMessageLocked(
		spec.conversationID,
		spec.sourceAgent,
		sourceSession.SessionID,
		conversationDeliveryPromptText(spec),
		sourceInvocationPromptDisplay(
			invocation,
			spec.sourceRep.RepresentativeAgentID,
			spec.sourceAgent.AgentID,
			sourceSession.SessionID,
			spec.targetRep.RepresentativeAgentID,
			spec.targetAgent.AgentID,
			targetSession.SessionID,
			"inquiry",
			"source",
			"Asked "+conversationAgentLabel(spec.targetAgent)+" for input.",
			conversationDeliveryPromptText(spec),
		),
		spec.now,
	)
	if err != nil {
		return domain.ConversationDelivery{}, err
	}
	return domain.ConversationDelivery{
		Conversation:     conversation,
		Invocation:       invocation,
		ParentInvocation: spec.parentInvocation,
		SourceSession:    sourceSession,
		TargetSession:    targetSession,
		PromptMessage:    prompt,
		Context:          spec.context,
		Instruction:      spec.instruction,
		Reason:           spec.reason,
		DeliveryStatus:   "stored",
		ReceiptToken:     spec.receiptToken,
	}, nil
}

func (s *MemoryStore) completeConversationInvocationReplyLocked(
	spec conversationDeliverySpec,
) (domain.ConversationDelivery, error) {
	if spec.parentInvocation == nil {
		return domain.ConversationDelivery{}, ErrNotFound
	}
	conversation := s.upsertAgentConversationLocked(
		spec.conversationID,
		spec.targetAgent.OwnerUserID,
		spec.now,
	)
	sourceSession, err := s.ensureConversationSessionLocked(
		spec.sourceAgent,
		spec.sourceRep,
		spec.conversationID,
		spec.sourceAgent.OwnerUserID,
		spec.sourceSessionID,
		spec.now,
	)
	if err != nil {
		return domain.ConversationDelivery{}, err
	}
	targetSession, err := s.ensureConversationSessionLocked(
		spec.targetAgent,
		spec.targetRep,
		spec.conversationID,
		spec.targetAgent.OwnerUserID,
		spec.targetSessionID,
		spec.now,
	)
	if err != nil {
		return domain.ConversationDelivery{}, err
	}
	prompt, err := s.createConversationPromptMessageLocked(
		spec.conversationID,
		spec.sourceAgent,
		sourceSession.SessionID,
		conversationDeliveryPromptText(spec),
		sourceInvocationPromptDisplay(
			*spec.parentInvocation,
			spec.sourceRep.RepresentativeAgentID,
			spec.sourceAgent.AgentID,
			sourceSession.SessionID,
			spec.targetRep.RepresentativeAgentID,
			spec.targetAgent.AgentID,
			targetSession.SessionID,
			"reply",
			"source",
			"Sent a reply to "+conversationAgentLabel(spec.targetAgent)+".",
			conversationDeliveryPromptText(spec),
		),
		spec.now,
	)
	if err != nil {
		return domain.ConversationDelivery{}, err
	}
	invocation := *spec.parentInvocation
	invocation.Status = domain.ConversationAgentInvocationStatusCompleted
	invocation.RemainingTurns = 0
	s.conversationAgentInvocations[invocation.InvocationID] = invocation
	return domain.ConversationDelivery{
		Conversation:   conversation,
		Invocation:     invocation,
		SourceSession:  sourceSession,
		TargetSession:  targetSession,
		PromptMessage:  prompt,
		Context:        spec.context,
		Instruction:    spec.instruction,
		Reason:         spec.reason,
		DeliveryStatus: "stored",
	}, nil
}

func (s *MemoryStore) StartAgentConversationForUser(
	ctx context.Context,
	principal UserPrincipal,
	req domain.StartAgentConversationRequest,
) (domain.AgentConversationStart, error) {
	s.mu.Lock()
	sourceAgent, ok := s.agents[strings.TrimSpace(req.FromRuntimeAgentID)]
	if !ok || !s.canAccessAgentLocked(principal, sourceAgent) {
		s.mu.Unlock()
		return domain.AgentConversationStart{}, ErrNotFound
	}
	if !canAccessOwner(principal, sourceAgent.OwnerUserID) {
		s.mu.Unlock()
		return domain.AgentConversationStart{}, ErrUnauthorized
	}
	nodeID := sourceAgent.NodeID
	s.mu.Unlock()
	return s.StartAgentConversation(ctx, Node{NodeID: nodeID}, req)
}

func (s *MemoryStore) CompleteAgentConversationInvocation(
	ctx context.Context,
	invocationID string,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	invocation, ok := s.conversationAgentInvocations[invocationID]
	if !ok {
		return ErrNotFound
	}
	invocation.Status = domain.ConversationAgentInvocationStatusCompleted
	invocation.RemainingTurns = 0
	s.conversationAgentInvocations[invocationID] = invocation
	return nil
}

func (s *MemoryStore) findSourceRepresentativeAgentLocked(
	runtimeAgentID string,
	representativeAgentID string,
) (domain.RepresentativeAgent, bool) {
	for _, rep := range s.representativeAgents {
		if rep.RuntimeAgentID != runtimeAgentID || rep.Status != domain.ConversationStatusActive {
			continue
		}
		if representativeAgentID != "" && rep.RepresentativeAgentID != representativeAgentID {
			continue
		}
		return rep, true
	}
	return domain.RepresentativeAgent{}, false
}

func (s *MemoryStore) agentConversationUsersCanInteractLocked(
	sourceUserID string,
	targetUserID string,
) bool {
	if sourceUserID == "" || targetUserID == "" {
		return false
	}
	if sourceUserID == targetUserID {
		return true
	}
	return s.envelopeUsersShareTeamLocked(sourceUserID, targetUserID)
}

func (s *MemoryStore) upsertAgentConversationLocked(
	conversationID string,
	ownerUserID string,
	now time.Time,
) domain.Conversation {
	conversation := s.conversations[conversationID]
	if conversation.ConversationID == "" {
		conversation = domain.Conversation{
			ConversationID:   conversationID,
			ConversationType: domain.ConversationTypeAgentThread,
			BoundaryType:     "personal",
			BoundaryID:       ownerUserID,
			HistoryPolicy:    domain.ConversationHistoryFullHistory,
			Status:           domain.ConversationStatusActive,
			CreatedAt:        now,
		}
	}
	s.conversations[conversationID] = conversation
	return conversation
}

func (s *MemoryStore) upsertConversationMemberLocked(
	conversationID string,
	userID string,
	role string,
	now time.Time,
) {
	key := conversationID + ":" + userID
	member := s.conversationMembers[key]
	if member.ConversationID == "" {
		member = domain.ConversationMember{
			ConversationID: conversationID,
			UserID:         userID,
			Role:           role,
			JoinedAt:       now,
		}
	}
	member.LeftAt = nil
	s.conversationMembers[key] = member
}

func (s *MemoryStore) upsertConversationAgentBindingLocked(
	conversationID string,
	representativeAgentID string,
	addedByUserID string,
	relationshipType string,
	now time.Time,
) domain.ConversationAgentBinding {
	bindingID := deterministicConversationBindingID(conversationID, representativeAgentID)
	binding := domain.ConversationAgentBinding{
		BindingID:             bindingID,
		ConversationID:        conversationID,
		RepresentativeAgentID: representativeAgentID,
		RelationshipType:      relationshipType,
		AddedByUserID:         addedByUserID,
		AccessMode:            domain.ConversationAgentAccessFromBinding,
		Status:                domain.ConversationAgentBindingStatusActive,
		CreatedAt:             now,
	}
	s.conversationAgentBindings[bindingID] = binding
	return binding
}

func (s *MemoryStore) createConversationSessionLocked(
	agent Agent,
	rep domain.RepresentativeAgent,
	conversationID string,
	createdByUserID string,
	now time.Time,
) (AgentSession, error) {
	sessionID, err := newSecret("sess")
	if err != nil {
		return AgentSession{}, err
	}
	session := s.upsertSessionLocked(agent.NodeID, agent.AgentID, SessionStatusInput{
		SessionID: sessionID,
		AgentType: agent.AgentType,
		Source:    domain.MessageSourceACPTunnel,
		Status:    "idle",
	}, "", now)
	session.ConversationID = conversationID
	session.ProfileID = rep.ProfileID
	session.RepresentativeAgentID = rep.RepresentativeAgentID
	session.CreatedByUserID = createdByUserID
	s.sessions[sessionKey(agent.AgentID, sessionID)] = session
	return session, nil
}

func (s *MemoryStore) ensureConversationSessionLocked(
	agent Agent,
	rep domain.RepresentativeAgent,
	conversationID string,
	createdByUserID string,
	sessionID string,
	now time.Time,
) (AgentSession, error) {
	if strings.TrimSpace(sessionID) == "" {
		return s.createConversationSessionLocked(agent, rep, conversationID, createdByUserID, now)
	}
	session := s.upsertSessionLocked(agent.NodeID, agent.AgentID, SessionStatusInput{
		SessionID: strings.TrimSpace(sessionID),
		AgentType: agent.AgentType,
		Source:    domain.MessageSourceACPTunnel,
		Status:    "idle",
	}, "", now)
	session.ConversationID = conversationID
	session.ProfileID = rep.ProfileID
	session.RepresentativeAgentID = rep.RepresentativeAgentID
	session.CreatedByUserID = createdByUserID
	s.sessions[sessionKey(agent.AgentID, session.SessionID)] = session
	return session, nil
}

func (s *MemoryStore) createConversationInvocationLocked(
	conversationID string,
	parentInvocationID string,
	sourceRepresentativeAgentID string,
	sourceRuntimeAgentID string,
	sourceSessionID string,
	targetRepresentativeAgentID string,
	targetRuntimeAgentID string,
	targetSessionID string,
	requestedByUserID string,
	maxTurns int,
	receiptTokenHash string,
	now time.Time,
) (domain.ConversationAgentInvocation, error) {
	invocationID, err := newSecret("inv")
	if err != nil {
		return domain.ConversationAgentInvocation{}, err
	}
	maxTurns = normalizedMaxTurns(maxTurns)
	if _, ok := s.activeConversationInvocationLocked(targetRuntimeAgentID, targetSessionID); ok {
		return domain.ConversationAgentInvocation{}, ErrConflict
	}
	invocation := domain.ConversationAgentInvocation{
		InvocationID:                invocationID,
		ConversationID:              conversationID,
		ParentInvocationID:          parentInvocationID,
		SourceRepresentativeAgentID: sourceRepresentativeAgentID,
		SourceRuntimeAgentID:        sourceRuntimeAgentID,
		SourceSessionID:             sourceSessionID,
		TargetRepresentativeAgentID: targetRepresentativeAgentID,
		TargetRuntimeAgentID:        targetRuntimeAgentID,
		TargetSessionID:             targetSessionID,
		ReceiptTokenHash:            receiptTokenHash,
		RequestedByUserID:           requestedByUserID,
		AccessMode:                  domain.ConversationAgentAccessCurrentTurn,
		SelectedMessageIDsJSON:      json.RawMessage(`[]`),
		MaxTurns:                    maxTurns,
		RemainingTurns:              maxTurns,
		Status:                      domain.ConversationAgentInvocationStatusActive,
		CreatedAt:                   now,
	}
	s.conversationAgentInvocations[invocationID] = invocation
	return invocation, nil
}

func (s *MemoryStore) activeConversationInvocationLocked(
	targetRuntimeAgentID string,
	targetSessionID string,
) (domain.ConversationAgentInvocation, bool) {
	for _, invocation := range s.conversationAgentInvocations {
		if invocation.TargetRuntimeAgentID == targetRuntimeAgentID &&
			invocation.TargetSessionID == targetSessionID &&
			invocation.Status == domain.ConversationAgentInvocationStatusActive {
			return invocation, true
		}
	}
	return domain.ConversationAgentInvocation{}, false
}

func (s *MemoryStore) activeConversationInvocationForReplyLocked(
	targetRuntimeAgentID string,
	targetRepresentativeAgentID string,
	targetSessionID string,
	invocationID string,
) (domain.ConversationAgentInvocation, error) {
	invocationID = strings.TrimSpace(invocationID)
	targetRepresentativeAgentID = strings.TrimSpace(targetRepresentativeAgentID)
	targetSessionID = strings.TrimSpace(targetSessionID)
	if invocationID != "" {
		invocation, ok := s.conversationAgentInvocations[invocationID]
		if !ok {
			return domain.ConversationAgentInvocation{}, ErrNotFound
		}
		if !activeInvocationMatchesReply(
			invocation,
			targetRuntimeAgentID,
			targetRepresentativeAgentID,
			targetSessionID,
		) {
			return domain.ConversationAgentInvocation{}, ErrNotFound
		}
		return invocation, nil
	}
	if targetSessionID != "" {
		if invocation, ok := s.activeConversationInvocationLocked(targetRuntimeAgentID, targetSessionID); ok {
			return invocation, nil
		}
		return domain.ConversationAgentInvocation{}, ErrNotFound
	}
	matches := make([]domain.ConversationAgentInvocation, 0, 2)
	for _, invocation := range s.conversationAgentInvocations {
		if invocation.TargetRuntimeAgentID != targetRuntimeAgentID ||
			invocation.Status != domain.ConversationAgentInvocationStatusActive {
			continue
		}
		if targetRepresentativeAgentID != "" &&
			invocation.TargetRepresentativeAgentID != targetRepresentativeAgentID {
			continue
		}
		matches = append(matches, invocation)
		if len(matches) > 1 {
			return domain.ConversationAgentInvocation{}, ErrConflict
		}
	}
	if len(matches) == 0 {
		return domain.ConversationAgentInvocation{}, ErrNotFound
	}
	return matches[0], nil
}

func activeInvocationMatchesReply(
	invocation domain.ConversationAgentInvocation,
	targetRuntimeAgentID string,
	targetRepresentativeAgentID string,
	targetSessionID string,
) bool {
	if invocation.Status != domain.ConversationAgentInvocationStatusActive {
		return false
	}
	if invocation.TargetRuntimeAgentID != strings.TrimSpace(targetRuntimeAgentID) {
		return false
	}
	if targetRepresentativeAgentID != "" &&
		invocation.TargetRepresentativeAgentID != targetRepresentativeAgentID {
		return false
	}
	if targetSessionID != "" && invocation.TargetSessionID != targetSessionID {
		return false
	}
	return true
}

func (s *MemoryStore) createConversationPromptMessageLocked(
	conversationID string,
	agent Agent,
	sessionID string,
	input string,
	display paxInvocationPromptDisplay,
	now time.Time,
) (domain.MessageWithParts, error) {
	messageID, err := newSecret("msg")
	if err != nil {
		return domain.MessageWithParts{}, err
	}
	raw, _ := json.Marshal(map[string]string{"input": input})
	msg := Message{
		MessageID:      messageID,
		ConversationID: conversationID,
		OwnerUserID:    agent.OwnerUserID,
		NodeID:         agent.NodeID,
		AgentID:        agent.AgentID,
		SessionID:      sessionID,
		Source:         domain.MessageSourceACPTunnel,
		Direction:      domain.MessageDirectionUserToAgent,
		Role:           "user",
		Status:         "sent",
		MessageType:    domain.MessageTypePaxUser,
		LogicalKey:     "agent_conversation:" + messageID,
		RawJSON:        raw,
		CreatedAt:      now,
	}
	s.prepareMessageLocked(&msg)
	s.messages[msg.MessageID] = cloneMessage(msg)
	s.messageLogical[msg.LogicalKey] = msg.MessageID
	part := MessagePart{
		MessageID:   msg.MessageID,
		PartIndex:   0,
		PartType:    domain.MessagePartText,
		Text:        input,
		PayloadJSON: raw,
		CreatedAt:   now,
	}
	s.prepareMessagePartLocked(&part)
	s.messageParts[messagePartKey{MessageID: part.MessageID, Index: part.PartIndex}] = cloneMessagePart(
		part,
	)
	if err := s.createPaxInvocationDisplayMessageLocked(msg, display, now); err != nil {
		return domain.MessageWithParts{}, err
	}
	return domain.MessageWithParts{Message: msg, Parts: []MessagePart{part}}, nil
}

func (s *MemoryStore) createPaxInvocationDisplayMessageLocked(
	parent Message,
	display paxInvocationPromptDisplay,
	now time.Time,
) error {
	if display.InvocationID == "" {
		return nil
	}
	if display.Side == "source" {
		if toolParent, toolCallID, ok := s.latestPaxInvocationToolCallLocked(parent); ok {
			return s.createPaxInvocationPendingMessageLocked(
				parent,
				toolParent,
				toolCallID,
				display,
				now,
			)
		}
	}
	messageID, err := newSecret("msg")
	if err != nil {
		return err
	}
	raw, text := paxInvocationDisplayPayload(parent.MessageID, []string{parent.MessageID}, display)
	msg := Message{
		MessageID:       messageID,
		ConversationID:  parent.ConversationID,
		OwnerUserID:     parent.OwnerUserID,
		NodeID:          parent.NodeID,
		AgentID:         parent.AgentID,
		SessionID:       parent.SessionID,
		Source:          parent.Source,
		Direction:       parent.Direction,
		Role:            parent.Role,
		Status:          parent.Status,
		MessageType:     domain.MessageTypePaxInvocation,
		ParentMessageID: parent.MessageID,
		LogicalKey:      paxInvocationDisplayLogicalKey(display),
		RawJSON:         raw,
		CreatedAt:       now,
	}
	s.prepareMessageLocked(&msg)
	s.messages[msg.MessageID] = cloneMessage(msg)
	s.messageLogical[msg.LogicalKey] = msg.MessageID
	part := MessagePart{
		MessageID:   msg.MessageID,
		PartIndex:   0,
		PartType:    domain.MessagePartText,
		Text:        text,
		PayloadJSON: raw,
		CreatedAt:   now,
	}
	s.prepareMessagePartLocked(&part)
	s.messageParts[messagePartKey{MessageID: part.MessageID, Index: part.PartIndex}] = cloneMessagePart(
		part,
	)
	return nil
}

func (s *MemoryStore) createPaxInvocationPendingMessageLocked(
	prompt Message,
	toolParent Message,
	toolCallID string,
	display paxInvocationPromptDisplay,
	now time.Time,
) error {
	messageID, err := newSecret("msg")
	if err != nil {
		return err
	}
	raw, text := paxInvocationPendingPayload(
		prompt.MessageID,
		toolParent.MessageID,
		toolCallID,
		display,
	)
	msg := Message{
		MessageID:       messageID,
		ConversationID:  prompt.ConversationID,
		OwnerUserID:     prompt.OwnerUserID,
		NodeID:          prompt.NodeID,
		AgentID:         prompt.AgentID,
		SessionID:       prompt.SessionID,
		Source:          prompt.Source,
		Direction:       prompt.Direction,
		Role:            prompt.Role,
		Status:          "pending",
		MessageType:     domain.MessageTypePaxInvocationPending,
		ParentMessageID: toolParent.MessageID,
		LogicalKey:      paxInvocationPendingLogicalKey(display),
		RawJSON:         raw,
		CreatedAt:       now,
	}
	s.prepareMessageLocked(&msg)
	s.messages[msg.MessageID] = cloneMessage(msg)
	s.messageLogical[msg.LogicalKey] = msg.MessageID
	part := MessagePart{
		MessageID:   msg.MessageID,
		PartIndex:   0,
		PartType:    domain.MessagePartText,
		Text:        text,
		PayloadJSON: raw,
		CreatedAt:   now,
	}
	s.prepareMessagePartLocked(&part)
	s.messageParts[messagePartKey{MessageID: part.MessageID, Index: part.PartIndex}] = cloneMessagePart(
		part,
	)
	return nil
}

func (s *MemoryStore) latestPaxInvocationToolCallLocked(parent Message) (Message, string, bool) {
	if parent.AgentID == "" || parent.SessionID == "" {
		return Message{}, "", false
	}
	messages := make([]Message, 0)
	for _, message := range s.messages {
		if message.AgentID == parent.AgentID && message.SessionID == parent.SessionID {
			messages = append(messages, cloneMessage(message))
		}
	}
	sort.Slice(messages, func(i, j int) bool {
		return messages[i].ID < messages[j].ID
	})
	return latestPaxInvocationToolCall(messages)
}

func (s *MemoryStore) canReadConversationLocked(userID string, conversationID string) bool {
	member := s.conversationMembers[conversationID+":"+userID]
	return member.ConversationID == conversationID && member.LeftAt == nil
}
