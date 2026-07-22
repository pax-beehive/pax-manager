package storage

import (
	"context"
	"database/sql/driver"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestMemoryStoreNextAgentACPRequestIDPersistsAcrossCallers(t *testing.T) {
	store := NewMemoryStore(func() time.Time { return time.Time{} })
	store.agents["agent-1"] = Agent{AgentID: "agent-1"}

	first, err := store.NextAgentACPRequestID(context.Background(), "agent-1")
	require.NoError(t, err)
	second, err := store.NextAgentACPRequestID(context.Background(), "agent-1")
	require.NoError(t, err)

	require.Equal(t, int64(1), first)
	require.Equal(t, int64(2), second)
}

func TestMemoryStoreNextAgentACPRequestIDIsConcurrentSafe(t *testing.T) {
	store := NewMemoryStore(func() time.Time { return time.Time{} })
	store.agents["agent-1"] = Agent{AgentID: "agent-1"}

	const count = 32
	ids := make(chan int64, count)
	var wait sync.WaitGroup
	for range count {
		wait.Add(1)
		go func() {
			defer wait.Done()
			id, err := store.NextAgentACPRequestID(context.Background(), "agent-1")
			require.NoError(t, err)
			ids <- id
		}()
	}
	wait.Wait()
	close(ids)

	seen := make(map[int64]bool, count)
	for id := range ids {
		seen[id] = true
	}
	require.Len(t, seen, count)
	for id := int64(1); id <= count; id++ {
		require.True(t, seen[id], "missing request id %d", id)
	}
}

func TestPostgresStoreNextAgentACPRequestIDUsesAtomicUpdate(t *testing.T) {
	script := &scriptedPostgresScript{
		queries: []scriptedRows{{
			columns: []string{"next_acp_request_id"},
			values:  [][]driver.Value{{int64(42)}},
		}},
	}
	store, cleanup := scriptedPostgresStore(t, script)
	defer cleanup()

	id, err := store.NextAgentACPRequestID(context.Background(), "agent-1")
	require.NoError(t, err)
	require.Equal(t, int64(42), id)
	require.Len(t, script.queryTexts, 1)
	require.Contains(t, script.queryTexts[0], "next_acp_request_id = next_acp_request_id + 1")
	require.Equal(t, "agent-1", script.queryArgs[0][0].Value)
}
