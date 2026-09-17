package storage

import (
	"context"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func queueKey(agentID, sessionID string) string { return agentID + "\x00" + sessionID }

func (s *MemoryStore) PutQueuedTurn(
	_ context.Context,
	q domain.QueuedTurn,
	patch bool,
) (domain.QueuedTurn, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(q.Input) == 0 || len(q.Input) > domain.MaxQueuedTurnBytes {
		return domain.QueuedTurn{}, false, ErrConflict
	}
	key := queueKey(q.AgentID, q.SessionID)
	old, exists := s.turnQueue[key]
	if exists && old.State != "queued" || patch && !exists {
		return domain.QueuedTurn{}, false, ErrConflict
	}
	if exists {
		old.Input = q.Input
		old.CommandID = q.CommandID
		if !patch {
			old.OwnerID = q.OwnerID
		}
		q = old
	} else {
		q.CreatedAt = s.now().UTC()
		q.State = "queued"
	}
	q.UpdatedAt = s.now().UTC()
	s.turnQueue[key] = q
	return q, exists, nil
}

func (s *MemoryStore) GetQueuedTurn(
	_ context.Context,
	agentID, sessionID string,
) (domain.QueuedTurn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.turnQueue[queueKey(agentID, sessionID)]
	if !ok {
		return q, ErrNotFound
	}
	return q, nil
}

func (s *MemoryStore) DeleteQueuedTurn(
	_ context.Context,
	agentID, sessionID string,
) (domain.QueuedTurn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := queueKey(agentID, sessionID)
	q, ok := s.turnQueue[key]
	if !ok {
		return q, ErrNotFound
	}
	if q.State == "sending" {
		return q, ErrConflict
	}
	delete(s.turnQueue, key)
	return q, nil
}

func (s *MemoryStore) ListQueuedTurns(
	_ context.Context,
	nodeID, agentID string,
) ([]domain.QueuedTurn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []domain.QueuedTurn
	for _, q := range s.turnQueue {
		if q.AgentID != agentID || q.State == "uncertain" {
			continue
		}
		session, ok := s.sessions[sessionKey(agentID, q.SessionID)]
		if !ok || session.NodeID != nodeID || session.ArchivedAt != nil {
			continue
		}
		q.NodeID = nodeID
		q.NativeID = session.NativeID
		result = append(result, q)
		if len(result) == 32 {
			break
		}
	}
	return result, nil
}

func (s *MemoryStore) ClaimQueuedTurn(
	_ context.Context,
	q domain.QueuedTurn,
	snapshot domain.AgentRuntimeSnapshot,
) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := queueKey(q.AgentID, q.SessionID)
	current, ok := s.turnQueue[key]
	if !ok || current.TurnID != q.TurnID || current.State != "queued" ||
		!current.UpdatedAt.Equal(q.UpdatedAt) ||
		current.Input != q.Input ||
		current.CommandID != q.CommandID {
		return false, nil
	}
	head := s.agentRuntimeSnapshotHeads[q.AgentID]
	if s.nodeRuntimeFences[q.NodeID] != snapshot.ConnectionFence ||
		head.ConnectionFence != snapshot.ConnectionFence ||
		!head.HasSequence ||
		head.LastSequence != snapshot.Sequence {
		return false, nil
	}
	session, ok := s.sessions[sessionKey(q.AgentID, q.SessionID)]
	if !ok || session.NodeID != q.NodeID || session.NativeID == "" || session.ArchivedAt != nil ||
		session.RuntimeStatus != domain.RuntimeStatusIdle {
		return false, nil
	}
	current.State = "sending"
	current.UpdatedAt = s.now().UTC()
	s.turnQueue[key] = current
	return true, nil
}

func (s *MemoryStore) FinishQueuedTurn(
	_ context.Context,
	q domain.QueuedTurn,
	accepted bool,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := queueKey(q.AgentID, q.SessionID)
	current, ok := s.turnQueue[key]
	if !ok || current.TurnID != q.TurnID ||
		(current.State != "sending" && (!accepted || current.State != "uncertain")) {
		return nil
	}
	if accepted {
		delete(s.turnQueue, key)
	} else {
		current.State = "uncertain"
		current.UpdatedAt = s.now().UTC()
		s.turnQueue[key] = current
	}
	return nil
}
