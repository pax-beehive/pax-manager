package userapi

import (
	"context"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *Service) fillSessionHistoryHead(ctx context.Context, session *domain.AgentSession) error {
	reader, ok := s.store.(domain.SessionHistorySyncStore)
	if !ok {
		return nil
	}
	message, err := reader.LatestSessionMessage(ctx, session.AgentID, session.SessionID)
	if err != nil {
		return err
	}
	session.LatestMessageID = message.MessageID
	session.LatestMessageSeq = message.SessionSeq
	session.LatestTurnID = message.TurnID
	return nil
}
