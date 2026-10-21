package storage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestNodeSessionRegistrationIsIdempotentAndPreservesOrigin(t *testing.T) {
	s := NewMemoryStore(time.Now)
	s.agents["agent"] = Agent{AgentID: "agent", NodeID: "node", OwnerUserID: "owner"}
	node := Node{NodeID: "node", OwnerUserID: "owner"}
	req := &domain.RegisterNodeSessionRequest{
		NativeID: "native",
		CWD:      "/workspace",
		Origin: domain.SessionOrigin{
			Kind:           "cronjob",
			RegistrationID: "run1",
			CronjobID:      "cron1",
			RunID:          "run1",
			Name:           "Daily",
		},
	}
	first, err := s.RegisterNodeSession(context.Background(), node, "agent", req)
	require.NoError(t, err)
	again, err := s.RegisterNodeSession(context.Background(), node, "agent", req)
	require.NoError(t, err)
	require.Equal(t, first.SessionID, again.SessionID)
	require.Len(t, s.sessions, 1)
	require.Contains(t, string(first.Metadata), `"cronjob_id":"cron1"`)
	_, err = s.RegisterNodeSession(context.Background(), Node{NodeID: "other"}, "agent", req)
	require.ErrorIs(t, err, ErrNotFound)
	changed := *req
	changed.CWD = "/elsewhere"
	_, err = s.RegisterNodeSession(context.Background(), node, "agent", &changed)
	require.ErrorIs(t, err, ErrConflict)
	changed = *req
	changed.NativeID = "other-native"
	_, err = s.RegisterNodeSession(context.Background(), node, "agent", &changed)
	require.ErrorIs(t, err, ErrConflict)
}
