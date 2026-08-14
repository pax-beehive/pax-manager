package manager

import (
	"context"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type canonicalSessionStore struct {
	domain.Store
}

func newCanonicalSessionStore(store domain.Store) domain.Store {
	return canonicalSessionStore{Store: store}
}

func (s canonicalSessionStore) ActivateNodeRuntimeFence(ctx context.Context, node domain.Node, fence string) error {
	runtimeStore, ok := s.Store.(domain.SessionRuntimeSnapshotStore)
	if !ok {
		return domain.ErrConflict
	}
	return runtimeStore.ActivateNodeRuntimeFence(ctx, node, fence)
}

func (s canonicalSessionStore) MarkNodeRuntimeStale(
	ctx context.Context,
	node domain.Node,
	fence string,
) (bool, error) {
	runtimeStore, ok := s.Store.(domain.SessionRuntimeSnapshotStore)
	if !ok {
		return false, domain.ErrConflict
	}
	return runtimeStore.MarkNodeRuntimeStale(ctx, node, fence)
}

func (s canonicalSessionStore) ReplaceAgentActiveTurns(
	ctx context.Context,
	node domain.Node,
	snapshot domain.AgentRuntimeSnapshot,
) (domain.ReplaceAgentActiveTurnsResult, error) {
	runtimeStore, ok := s.Store.(domain.SessionRuntimeSnapshotStore)
	if !ok {
		return domain.ReplaceAgentActiveTurnsResult{}, domain.ErrConflict
	}
	return runtimeStore.ReplaceAgentActiveTurns(ctx, node, snapshot)
}

func (s canonicalSessionStore) UpsertMessage(ctx context.Context, msg *domain.Message) error {
	if msg != nil && msg.AgentID != "" && msg.SessionID != "" && msg.OwnerUserID != "" {
		sessionID, err := s.canonicalSessionID(ctx, msg.OwnerUserID, msg.AgentID, msg.SessionID)
		if err != nil {
			return err
		}
		msg.SessionID = sessionID
	}
	return s.Store.UpsertMessage(ctx, msg)
}

func (s canonicalSessionStore) RecordSecretAccess(
	ctx context.Context,
	event domain.SecretAccessEvent,
) error {
	if event.AgentID != "" && event.SessionID != "" && event.NodeID != "" {
		agent, err := s.GetNodeAgent(ctx, event.NodeID, event.AgentID)
		if err == nil {
			sessionID, err := s.canonicalSessionID(
				ctx,
				agent.OwnerUserID,
				event.AgentID,
				event.SessionID,
			)
			if err != nil {
				return err
			}
			event.SessionID = sessionID
		}
	}
	return s.Store.RecordSecretAccess(ctx, event)
}

func (s canonicalSessionStore) canonicalSessionID(
	ctx context.Context,
	ownerUserID string,
	agentID string,
	sessionID string,
) (string, error) {
	sessions, err := s.ListAgentSessions(
		ctx,
		domain.UserPrincipal{User: domain.User{UserID: ownerUserID}},
		agentID,
	)
	if err != nil {
		return sessionID, nil
	}
	for _, session := range sessions {
		if session.SessionID == sessionID || session.NativeID == sessionID {
			return session.SessionID, nil
		}
	}
	return sessionID, nil
}
