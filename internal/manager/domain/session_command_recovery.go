package domain

import (
	"context"
	"encoding/json"
	"time"
)

const DeferredSessionCommandsMetadataKey = "deferred_session_commands"

type SessionCommandUpdate struct {
	Params     json.RawMessage
	ObservedAt time.Time
}

// SessionCommandJournalStore recovers creation metadata from the durable transport journal.
type SessionCommandJournalStore interface {
	LatestDeferredSessionCommands(
		ctx context.Context,
		ownerUserID, agentID, queueID, nativeSessionID string,
		beforeSeq int64,
	) (SessionCommandUpdate, error)
	RecoverSessionCreation(
		ctx context.Context,
		ownerUserID, agentID, queueID, requestID string,
	) (string, error)
}
