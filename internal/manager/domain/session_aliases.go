package domain

import "context"

// SessionAliases identifies a session without loading user-facing metadata.
type SessionAliases struct {
	SessionID string
	NativeID  string
}

// SessionAliasStore resolves transport IDs, including internal and archived work.
// Lookups require owner or team access to the agent.
type SessionAliasStore interface {
	ResolveSessionAliases(
		ctx context.Context,
		userID, agentID, sessionID string,
	) (SessionAliases, error)
}
