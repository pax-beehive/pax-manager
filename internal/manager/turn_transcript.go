package manager

import (
	"context"
	"errors"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s canonicalSessionStore) ListTurnTranscript(
	ctx context.Context, agentID, sessionID, turnID string,
) ([]domain.MessageWithParts, error) {
	store, ok := s.Store.(domain.TurnTranscriptStore)
	if !ok {
		return nil, errors.New("turn transcript store unavailable")
	}
	return store.ListTurnTranscript(ctx, agentID, sessionID, turnID)
}
