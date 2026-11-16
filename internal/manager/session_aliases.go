package manager

import (
	"context"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func resolveStoreSessionAliases(
	ctx context.Context,
	store domain.Store,
	ownerUserID, agentID, sessionID string,
) (domain.SessionAliases, error) {
	if resolver, ok := store.(domain.SessionAliasStore); ok {
		return resolver.ResolveSessionAliases(ctx, ownerUserID, agentID, sessionID)
	}
	// Compatibility for custom stores that do not implement the internal lookup.
	sessions, err := store.ListAgentSessions(
		ctx,
		domain.UserPrincipal{User: domain.User{UserID: ownerUserID}},
		agentID,
	)
	if err != nil {
		return domain.SessionAliases{}, err
	}
	for _, session := range sessions {
		if session.SessionID == sessionID || session.NativeID == sessionID {
			return domain.SessionAliases{
				SessionID: session.SessionID,
				NativeID:  session.NativeID,
			}, nil
		}
	}
	return domain.SessionAliases{}, domain.ErrNotFound
}
