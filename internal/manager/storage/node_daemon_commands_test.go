package storage

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestMemoryNodeDaemonCommandStore(t *testing.T) {
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	runNodeDaemonCommandStoreContract(t, NewMemoryStore(func() time.Time { return now }), now)
}

func TestPostgresNodeDaemonCommandStore(t *testing.T) {
	db := pairingPostgresFixture(t)()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "db", "init.sql"))
	require.NoError(t, err)
	ddl := string(raw)
	start := strings.Index(ddl, "CREATE TABLE IF NOT EXISTS node_daemon_commands")
	end := strings.Index(ddl, "CREATE TABLE IF NOT EXISTS agent_events")
	require.Positive(t, start)
	require.Greater(t, end, start)
	// Applying twice proves the DDL is safe to rerun on every startup.
	for range 2 {
		_, err = db.ExecContext(t.Context(), ddl[start:end])
		require.NoError(t, err)
	}
	_, err = db.ExecContext(t.Context(), `INSERT INTO nodes VALUES ('node_2')`)
	require.NoError(t, err)
	now := time.Date(2026, 10, 22, 12, 0, 0, 0, time.UTC)
	runNodeDaemonCommandStoreContract(t, NewPostgresStore(db, func() time.Time { return now }), now)
}

func runNodeDaemonCommandStoreContract(
	t *testing.T,
	store domain.NodeDaemonCommandStore,
	now time.Time,
) {
	t.Helper()
	ctx := context.Background()
	newCommand := func(nodeID, commandID, commandType string, age time.Duration) domain.NodeDaemonCommand {
		return domain.NodeDaemonCommand{
			NodeID:            nodeID,
			CommandID:         commandID,
			OwnerUserID:       "user_1",
			RequestedByUserID: "user_1",
			Type:              commandType,
			Target:            json.RawMessage(`{"tag":"stable"}`),
			TargetVersion:     "1.2.3",
			CreatedAt:         now.Add(-age),
		}
	}

	created, ok, err := store.CreateNodeDaemonCommand(
		ctx,
		newCommand("node_1", "cmd_old", domain.NodeDaemonCommandPaxlUpgrade, 2*time.Hour),
	)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, domain.NodeDaemonCommandStatusDispatching, created.Status)
	require.Equal(t, domain.NodeDaemonCommandStateInProgress, created.State)
	require.JSONEq(t, `{"tag":"stable"}`, string(created.Target))

	again, ok, err := store.CreateNodeDaemonCommand(ctx, domain.NodeDaemonCommand{
		NodeID: "node_1", CommandID: "cmd_old", OwnerUserID: "user_1",
		Type: domain.NodeDaemonCommandPaxdRestart,
	})
	require.NoError(t, err)
	require.False(t, ok)
	require.Equal(t, domain.NodeDaemonCommandPaxlUpgrade, again.Type)

	for _, command := range []domain.NodeDaemonCommand{
		newCommand("node_1", "cmd_new", domain.NodeDaemonCommandPaxlUpgrade, time.Hour),
		newCommand("node_1", "cmd_restart", domain.NodeDaemonCommandPaxdRestart, 30*time.Minute),
		newCommand("node_2", "cmd_other", domain.NodeDaemonCommandPaxlUpgrade, time.Minute),
	} {
		_, _, err := store.CreateNodeDaemonCommand(ctx, command)
		require.NoError(t, err)
	}

	observedAt := now.Add(-time.Minute)
	saved := created
	saved.Status = "applied"
	saved.Phase = "verified"
	saved.Result = json.RawMessage(`{"phase":"verified"}`)
	saved.MaintenanceConfirmation = &domain.NodeDaemonMaintenanceConfirmation{
		CommandID: "cmd_old", Status: "confirmed", UpdatedAt: observedAt,
	}
	saved.State = domain.NodeDaemonCommandStateSucceeded
	saved.ObservedAt = &observedAt
	saved.CompletedAt = &observedAt
	require.NoError(t, store.SaveNodeDaemonCommand(ctx, saved))
	got, err := store.GetNodeDaemonCommand(ctx, "node_1", "cmd_old")
	require.NoError(t, err)
	require.Equal(t, "applied", got.Status)
	require.Equal(t, "verified", got.Phase)
	require.Equal(t, domain.NodeDaemonCommandStateSucceeded, got.State)
	require.JSONEq(t, `{"phase":"verified"}`, string(got.Result))
	require.Equal(t, "confirmed", got.MaintenanceConfirmation.Status)
	require.True(t, got.CompletedAt.Equal(observedAt))
	require.True(t, got.UpdatedAt.Equal(now))

	missing := newCommand("node_1", "cmd_missing", domain.NodeDaemonCommandPaxlUpgrade, 0)
	require.ErrorIs(t, store.SaveNodeDaemonCommand(ctx, missing), domain.ErrNotFound)
	_, err = store.GetNodeDaemonCommand(ctx, "node_2", "cmd_old")
	require.ErrorIs(t, err, domain.ErrNotFound)

	listed, err := store.ListNodeDaemonCommands(ctx, "node_1", domain.NodeDaemonCommandFilter{})
	require.NoError(t, err)
	require.Equal(t, []string{"cmd_restart", "cmd_new", "cmd_old"}, nodeDaemonCommandIDs(listed))

	listed, err = store.ListNodeDaemonCommands(ctx, "node_1", domain.NodeDaemonCommandFilter{
		Type:  domain.NodeDaemonCommandPaxlUpgrade,
		State: domain.NodeDaemonCommandStateInProgress,
	})
	require.NoError(t, err)
	require.Equal(t, []string{"cmd_new"}, nodeDaemonCommandIDs(listed))

	listed, err = store.ListNodeDaemonCommands(
		ctx,
		"node_1",
		domain.NodeDaemonCommandFilter{Limit: 1},
	)
	require.NoError(t, err)
	require.Equal(t, []string{"cmd_restart"}, nodeDaemonCommandIDs(listed))

	latest, err := store.LatestNodeDaemonCommands(ctx, []string{"node_1", "node_2", "node_3"})
	require.NoError(t, err)
	require.Equal(t, []string{"cmd_restart", "cmd_new"}, nodeDaemonCommandIDs(latest["node_1"]))
	require.Equal(t, []string{"cmd_other"}, nodeDaemonCommandIDs(latest["node_2"]))
	require.Empty(t, latest["node_3"])

	latest, err = store.LatestNodeDaemonCommands(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, latest)
}

func nodeDaemonCommandIDs(commands []domain.NodeDaemonCommand) []string {
	ids := make([]string, len(commands))
	for i, command := range commands {
		ids[i] = command.CommandID
	}
	return ids
}
