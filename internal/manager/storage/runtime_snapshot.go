package storage

import (
	"context"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type memoryAgentRuntimeSnapshotHead struct {
	ConnectionFence string
	LastSequence    int64
	HasSequence     bool
	Authority       string
}

type resolvedRuntimeSession struct {
	key     string
	session AgentSession
}

func (s *MemoryStore) ActivateNodeRuntimeFence(
	ctx context.Context,
	node Node,
	fence string,
) error {
	_ = ctx
	if strings.TrimSpace(fence) == "" {
		return ErrConflict
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, ok := s.nodes[node.NodeID]
	if !ok || stored.OwnerUserID != node.OwnerUserID {
		return ErrNotFound
	}
	s.nodeRuntimeFences[node.NodeID] = fence
	return nil
}

func (s *MemoryStore) ReplaceAgentActiveTurns(
	ctx context.Context,
	node Node,
	snapshot domain.AgentRuntimeSnapshot,
) (domain.ReplaceAgentActiveTurnsResult, error) {
	_ = ctx
	if err := snapshot.Validate(); err != nil {
		return domain.ReplaceAgentActiveTurnsResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.nodeRuntimeFences[node.NodeID] != snapshot.ConnectionFence {
		return domain.ReplaceAgentActiveTurnsResult{Status: domain.RuntimeSnapshotFenced}, nil
	}
	storedNode, ok := s.nodes[node.NodeID]
	if !ok || storedNode.OwnerUserID != node.OwnerUserID {
		return domain.ReplaceAgentActiveTurnsResult{}, ErrNotFound
	}
	agent, ok := s.agents[snapshot.AgentID]
	if !ok || agent.NodeID != node.NodeID || agent.OwnerUserID != node.OwnerUserID {
		return domain.ReplaceAgentActiveTurnsResult{}, ErrNotFound
	}

	head := s.agentRuntimeSnapshotHeads[snapshot.AgentID]
	if head.ConnectionFence == snapshot.ConnectionFence && head.HasSequence {
		switch {
		case snapshot.Sequence == head.LastSequence:
			return domain.ReplaceAgentActiveTurnsResult{
				Status: domain.RuntimeSnapshotDuplicate,
			}, nil
		case snapshot.Sequence < head.LastSequence:
			return domain.ReplaceAgentActiveTurnsResult{Status: domain.RuntimeSnapshotStale}, nil
		}
	}

	resolved, err := s.resolveRuntimeSessionsLocked(node, snapshot)
	if err != nil {
		return domain.ReplaceAgentActiveTurnsResult{}, err
	}
	now := s.now().UTC()
	previouslyActive := s.claimSnapshotAuthorityLocked(snapshot.AgentID)

	changes := make(
		[]domain.RuntimeStatusChange,
		0,
		len(snapshot.ActiveTurns)+len(previouslyActive),
	)
	for _, turn := range snapshot.ActiveTurns {
		entry, reportable := resolved[turn.NativeSessionID]
		if !reportable {
			continue
		}
		session := entry.session
		previousStatus := session.RuntimeStatus
		previousTurn := session.RuntimeTurnInstanceID
		session.NodeID = node.NodeID
		session.AgentID = snapshot.AgentID
		session.NativeID = turn.NativeSessionID
		session.RuntimeStatus = turn.RuntimeStatus
		session.RuntimeTurnInstanceID = turn.TurnInstanceID
		session.RuntimeAuthority = domain.RuntimeAuthoritySnapshot
		session.RunStatus = turn.RuntimeStatus
		session.Status = turn.RuntimeStatus
		session.UpdatedAt = now
		session.RuntimeState = runtimeStateFromTurn(
			node,
			snapshot.AgentID,
			session.SessionID,
			turn,
			now,
		)
		session.Metadata = runtimeMetadata(session.Metadata, *session.RuntimeState)
		s.sessions[entry.key] = session
		delete(previouslyActive, entry.key)
		if previousStatus != turn.RuntimeStatus || previousTurn != turn.TurnInstanceID {
			changes = append(changes, runtimeStatusChange(session))
		}
	}
	for key, session := range previouslyActive {
		session.RuntimeStatus = domain.RuntimeStatusIdle
		session.RuntimeTurnInstanceID = ""
		session.RuntimeAuthority = domain.RuntimeAuthoritySnapshot
		session.RunStatus = domain.RuntimeStatusIdle
		session.Status = domain.RuntimeStatusIdle
		session.UpdatedAt = now
		session.RuntimeState = idleRuntimeState(node, session, now)
		session.Metadata = runtimeMetadata(session.Metadata, *session.RuntimeState)
		s.sessions[key] = session
		changes = append(changes, runtimeStatusChange(session))
	}

	authorityChanged := head.Authority != domain.RuntimeAuthoritySnapshot
	head.ConnectionFence = snapshot.ConnectionFence
	head.LastSequence = snapshot.Sequence
	head.HasSequence = true
	head.Authority = domain.RuntimeAuthoritySnapshot
	s.agentRuntimeSnapshotHeads[snapshot.AgentID] = head
	return domain.ReplaceAgentActiveTurnsResult{
		Status: domain.RuntimeSnapshotApplied, AuthorityChanged: authorityChanged, Changes: changes,
	}, nil
}

func (s *MemoryStore) resolveRuntimeSessionsLocked(
	node Node,
	snapshot domain.AgentRuntimeSnapshot,
) (map[string]resolvedRuntimeSession, error) {
	resolved := make(map[string]resolvedRuntimeSession, len(snapshot.ActiveTurns))
	for _, turn := range snapshot.ActiveTurns {
		var matches []resolvedRuntimeSession
		for key, session := range s.sessions {
			if session.AgentID == snapshot.AgentID && session.NativeID == turn.NativeSessionID {
				matches = append(matches, resolvedRuntimeSession{key: key, session: session})
			}
		}
		if len(matches) > 1 {
			return nil, ErrConflict
		}
		if len(matches) == 1 {
			resolved[turn.NativeSessionID] = matches[0]
			continue
		}
	}
	return resolved, nil
}

func runtimeStateFromTurn(
	node Node,
	agentID string,
	sessionID string,
	turn domain.ActiveTurnSnapshot,
	now time.Time,
) *domain.SessionRuntimeState {
	state := &domain.SessionRuntimeState{
		OwnerUserID: node.OwnerUserID, NodeID: node.NodeID, AgentID: agentID, SessionID: sessionID,
		Lifecycle: turn.RuntimeStatus, ActiveTurnID: turn.TurnInstanceID,
		ActivePromptRequestID: domain.RuntimePromptRequestID(turn.PromptRequestID),
		TurnInstanceID:        turn.TurnInstanceID, PendingApprovalID: turn.PendingApprovalID, UpdatedAt: now,
	}
	if turn.RuntimeStatus == domain.RuntimeStatusWaitingApproval {
		state.BlockedReason = domain.RuntimeBlockedReasonToolApproval
		state.BlockedRef = turn.PendingApprovalID
	}
	return state
}

func idleRuntimeState(node Node, session AgentSession, now time.Time) *domain.SessionRuntimeState {
	return &domain.SessionRuntimeState{
		OwnerUserID: node.OwnerUserID, NodeID: node.NodeID, AgentID: session.AgentID,
		SessionID: session.SessionID, Lifecycle: domain.RuntimeLifecycleIdle, UpdatedAt: now,
	}
}

func runtimeStatusChange(session AgentSession) domain.RuntimeStatusChange {
	return domain.RuntimeStatusChange{
		SessionID: session.SessionID, NativeSessionID: session.NativeID,
		RuntimeStatus: session.RuntimeStatus, TurnInstanceID: session.RuntimeTurnInstanceID,
	}
}

func (s *MemoryStore) claimSnapshotAuthorityLocked(agentID string) map[string]AgentSession {
	previouslyActive := make(map[string]AgentSession)
	for key, session := range s.sessions {
		if session.AgentID != agentID {
			continue
		}
		if session.RuntimeStatus == domain.RuntimeStatusRunning ||
			session.RuntimeStatus == domain.RuntimeStatusWaitingApproval ||
			session.RuntimeStatus == domain.RuntimeStatusUnknown {
			previouslyActive[key] = session
		}
		session.RuntimeAuthority = domain.RuntimeAuthoritySnapshot
		s.sessions[key] = session
	}

	return previouslyActive
}
