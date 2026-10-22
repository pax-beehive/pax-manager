package userapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/storage"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
	userapimocks "github.com/pax-beehive/pax-manager/internal/manager/userapi/mocks"
)

// commandRecordingStore serves node daemon command records from a real
// in-memory store and everything else from the mock.
type commandRecordingStore struct {
	*userapimocks.MockStore
	commands *storage.MemoryStore
}

func (s commandRecordingStore) CreateNodeDaemonCommand(
	ctx context.Context,
	command domain.NodeDaemonCommand,
) (domain.NodeDaemonCommand, bool, error) {
	return s.commands.CreateNodeDaemonCommand(ctx, command)
}

func (s commandRecordingStore) SaveNodeDaemonCommand(
	ctx context.Context,
	command domain.NodeDaemonCommand,
) error {
	return s.commands.SaveNodeDaemonCommand(ctx, command)
}

func (s commandRecordingStore) GetNodeDaemonCommand(
	ctx context.Context,
	nodeID string,
	commandID string,
) (domain.NodeDaemonCommand, error) {
	return s.commands.GetNodeDaemonCommand(ctx, nodeID, commandID)
}

func (s commandRecordingStore) ListNodeDaemonCommands(
	ctx context.Context,
	nodeID string,
	filter domain.NodeDaemonCommandFilter,
) ([]domain.NodeDaemonCommand, error) {
	return s.commands.ListNodeDaemonCommands(ctx, nodeID, filter)
}

func (s commandRecordingStore) LatestNodeDaemonCommands(
	ctx context.Context,
	nodeIDs []string,
) (map[string][]domain.NodeDaemonCommand, error) {
	return s.commands.LatestNodeDaemonCommands(ctx, nodeIDs)
}

// trackingNodeControlClient adds the maintenance tracker the hub provides.
type trackingNodeControlClient struct {
	*fakeNodeControlClient
	tracked       map[string]domain.NodeDaemonMaintenanceConfirmation
	trackedStatus string
}

func (c *trackingNodeControlClient) TrackMaintenance(
	nodeID, commandID, requestedBootID, expectedVersion string,
) {
	if c.tracked == nil {
		c.tracked = make(map[string]domain.NodeDaemonMaintenanceConfirmation)
	}
	c.tracked[commandID] = domain.NodeDaemonMaintenanceConfirmation{
		CommandID:       commandID,
		RequestedBootID: requestedBootID,
		ExpectedVersion: expectedVersion,
		Status:          c.trackedStatus,
	}
}

func (c *trackingNodeControlClient) MaintenanceConfirmation(
	_ string,
	commandID string,
) (domain.NodeDaemonMaintenanceConfirmation, bool) {
	confirmation, ok := c.tracked[commandID]
	return confirmation, ok
}

type nodeDaemonCommandFixture struct {
	ctx       context.Context
	principal domain.UserPrincipal
	node      domain.Node
	store     commandRecordingStore
	client    *fakeNodeControlClient
	svc       *userapi.Service
}

func newNodeDaemonCommandFixture(t *testing.T) nodeDaemonCommandFixture {
	t.Helper()
	ctx := context.Background()
	principal := userPrincipal("usr_self", false)
	node := domain.Node{NodeID: "node_1", OwnerUserID: "usr_self"}
	mockStore := userapimocks.NewMockStore(t)
	mockStore.EXPECT().GetNode(ctx, principal, "node_1").Return(node, nil).Maybe()
	principals := userapimocks.NewMockPrincipalResolver(t)
	principals.EXPECT().Principal(ctx, auth.RequestMetadata{}).Return(principal, nil).Maybe()
	secrets := userapimocks.NewMockSecretIssuer(t)
	secrets.EXPECT().New("ctlq").Return("ctlq_1", nil).Maybe()
	store := commandRecordingStore{
		MockStore: mockStore,
		commands:  storage.NewMemoryStore(fixedUserClock),
	}
	client := &fakeNodeControlClient{remoteID: "remote_prod"}
	svc := userapi.NewService(store, fixedUserClock, principals, secrets)
	svc.SetNodeControlClient(client)
	return nodeDaemonCommandFixture{
		ctx:       ctx,
		principal: principal,
		node:      node,
		store:     store,
		client:    client,
		svc:       svc,
	}
}

func (f nodeDaemonCommandFixture) upgradePaxl(t *testing.T, commandID string) map[string]any {
	t.Helper()
	status, data, err := f.svc.UpgradeNodePaxl(
		f.ctx,
		auth.RequestMetadata{},
		domain.UpgradeNodePaxlRequest{NodeID: "node_1", CommandID: commandID, Version: "1.2.3"},
	)
	require.NoError(t, err)
	require.Equal(t, http.StatusAccepted, status)
	return data.(map[string]any)
}

func (f nodeDaemonCommandFixture) record(t *testing.T, commandID string) domain.NodeDaemonCommand {
	t.Helper()
	record, err := f.store.commands.GetNodeDaemonCommand(f.ctx, "node_1", commandID)
	require.NoError(t, err)
	return record
}

func TestUpgradeNodePaxlRecordsCommandBeforeDispatch(t *testing.T) {
	f := newNodeDaemonCommandFixture(t)
	f.client.commandAck = json.RawMessage(`{"command_id":"cmd_1","ok":true,"status":"received"}`)

	data := f.upgradePaxl(t, "cmd_1")

	record := f.record(t, "cmd_1")
	require.Equal(t, domain.NodeDaemonCommandPaxlUpgrade, record.Type)
	require.Equal(t, "usr_self", record.OwnerUserID)
	require.Equal(t, "usr_self", record.RequestedByUserID)
	require.Equal(t, "1.2.3", record.TargetVersion)
	require.JSONEq(t, `{"tag":"stable"}`, string(record.Target))
	require.Equal(t, "received", record.Status)
	require.Equal(t, domain.NodeDaemonCommandStateInProgress, record.State)
	require.Nil(t, record.CompletedAt)
	require.Equal(t, record, data["daemon_command"])
}

func TestRecordedCommandDispatchFailureMarksRecordFailed(t *testing.T) {
	f := newNodeDaemonCommandFixture(t)
	f.client.commandErr = errors.New("tunnel closed")

	data := f.upgradePaxl(t, "cmd_1")

	require.Equal(t, "tunnel closed", data["dispatch_error"])
	record := f.record(t, "cmd_1")
	require.Equal(t, domain.NodeDaemonCommandStatusDispatchFailed, record.Status)
	require.Equal(t, domain.NodeDaemonCommandStateFailed, record.State)
	require.Equal(t, "tunnel closed", record.ErrorMessage)
	require.NotNil(t, record.CompletedAt)

	// Retrying the same command ID re-dispatches and adopts the paxd status.
	f.client.commandErr = nil
	f.client.commandAck = json.RawMessage(`{"command_id":"cmd_1","ok":true,"status":"received"}`)
	f.upgradePaxl(t, "cmd_1")
	record = f.record(t, "cmd_1")
	require.Equal(t, "received", record.Status)
	require.Equal(t, domain.NodeDaemonCommandStateInProgress, record.State)
	require.Empty(t, record.ErrorMessage)
}

func TestRecordedCommandRejectsReusedIDForDifferentType(t *testing.T) {
	f := newNodeDaemonCommandFixture(t)
	f.client.commandAck = json.RawMessage(`{"command_id":"cmd_1","ok":true,"status":"received"}`)
	f.upgradePaxl(t, "cmd_1")

	_, _, err := f.svc.RestartNodeDaemon(
		f.ctx,
		auth.RequestMetadata{},
		domain.RestartNodeDaemonRequest{NodeID: "node_1", CommandID: "cmd_1"},
	)

	var appErr apperr.Error
	require.ErrorAs(t, err, &appErr)
	require.Equal(t, http.StatusConflict, appErr.Status)
	require.Equal(t, 1, f.client.commandCalls)
}

func TestGetNodeDaemonCommandRefreshesRecordFromPaxd(t *testing.T) {
	f := newNodeDaemonCommandFixture(t)
	f.client.commandAck = json.RawMessage(`{"command_id":"cmd_1","ok":true,"status":"received"}`)
	f.upgradePaxl(t, "cmd_1")
	f.client.result = json.RawMessage(`{
		"type":"command.get",
		"command":{
			"command_id":"cmd_1",
			"type":"paxl.upgrade",
			"status":"applied",
			"result":{"phase":"verified","paxl":{"status":"installed","version":"1.2.3"}}
		}
	}`)

	status, data, err := f.svc.GetNodeDaemonCommand(
		f.ctx,
		auth.RequestMetadata{},
		"node_1",
		"cmd_1",
	)

	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	record := f.record(t, "cmd_1")
	require.Equal(t, "applied", record.Status)
	require.Equal(t, "verified", record.Phase)
	require.Equal(t, domain.NodeDaemonCommandStateSucceeded, record.State)
	require.NotNil(t, record.CompletedAt)
	require.NotNil(t, record.ObservedAt)
	result := data.(map[string]any)
	require.Equal(t, record, result["daemon_command"])
	require.Equal(t, "applied", result["command"].(map[string]any)["status"])
}

func TestGetNodeDaemonCommandServesStoredRecordWhenNodeIsOffline(t *testing.T) {
	f := newNodeDaemonCommandFixture(t)
	f.client.commandAck = json.RawMessage(`{"command_id":"cmd_1","ok":true,"status":"received"}`)
	f.upgradePaxl(t, "cmd_1")
	f.client.err = errors.New("node control tunnel is not connected")

	status, data, err := f.svc.GetNodeDaemonCommand(
		f.ctx,
		auth.RequestMetadata{},
		"node_1",
		"cmd_1",
	)

	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	result := data.(map[string]any)
	require.Equal(t, true, result["stale"])
	require.Equal(t, "received", result["command"].(map[string]any)["status"])
	require.Equal(t, f.record(t, "cmd_1"), result["daemon_command"])
}

func TestGetNodeDaemonCommandWithoutRecordStillRequiresPaxd(t *testing.T) {
	f := newNodeDaemonCommandFixture(t)
	f.client.err = errors.New("node control tunnel is not connected")

	_, _, err := f.svc.GetNodeDaemonCommand(f.ctx, auth.RequestMetadata{}, "node_1", "cmd_unknown")

	var appErr apperr.Error
	require.ErrorAs(t, err, &appErr)
	require.Equal(t, http.StatusServiceUnavailable, appErr.Status)
}

func TestGetNodeDaemonCommandMarksForgottenCommandLost(t *testing.T) {
	f := newNodeDaemonCommandFixture(t)
	f.client.commandAck = json.RawMessage(`{"command_id":"cmd_1","ok":true,"status":"received"}`)
	f.upgradePaxl(t, "cmd_1")
	f.client.result = json.RawMessage(`{
		"type":"command.get",
		"error":{"code":"not_found","message":"not found"}
	}`)

	_, _, err := f.svc.GetNodeDaemonCommand(f.ctx, auth.RequestMetadata{}, "node_1", "cmd_1")

	require.NoError(t, err)
	record := f.record(t, "cmd_1")
	require.Equal(t, domain.NodeDaemonCommandStatusLost, record.Status)
	require.Equal(t, domain.NodeDaemonCommandStateFailed, record.State)
}

func TestSyncNodeDaemonCommandsConfirmsPaxdUpgradeFromHeartbeat(t *testing.T) {
	f := newNodeDaemonCommandFixture(t)
	tracker := &trackingNodeControlClient{
		fakeNodeControlClient: f.client,
		trackedStatus:         "awaiting_new_boot",
	}
	f.svc.SetNodeControlClient(tracker)
	f.client.commandAck = json.RawMessage(`{
		"command_id":"cmd_upgrade","ok":true,"status":"received",
		"result":{"paxd_upgrade":{"requested_boot_id":"boot_old","target_version":"1.2.3"}}
	}`)
	_, _, err := f.svc.UpgradeNodeDaemon(
		f.ctx,
		auth.RequestMetadata{},
		domain.UpgradeNodeDaemonRequest{
			NodeID:    "node_1",
			CommandID: "cmd_upgrade",
			Version:   "1.2.3",
		},
	)
	require.NoError(t, err)
	record := f.record(t, "cmd_upgrade")
	require.Equal(t, "awaiting_new_boot", record.MaintenanceConfirmation.Status)
	require.Equal(t, domain.NodeDaemonCommandStateInProgress, record.State)

	// paxd keeps the record at "received"; the new boot confirms it.
	confirmed := tracker.tracked["cmd_upgrade"]
	confirmed.Status = "confirmed"
	tracker.tracked["cmd_upgrade"] = confirmed
	f.client.result = json.RawMessage(`{
		"type":"command.get",
		"command":{
			"command_id":"cmd_upgrade",
			"type":"paxd.upgrade",
			"status":"received",
			"result":{"requested_boot_id":"boot_old","target_version":"1.2.3","phase":"awaiting_ack"}
		}
	}`)

	require.NoError(t, f.svc.SyncNodeDaemonCommands(f.ctx, "node_1"))

	record = f.record(t, "cmd_upgrade")
	require.Equal(t, "received", record.Status)
	require.Equal(t, "confirmed", record.MaintenanceConfirmation.Status)
	require.Equal(t, domain.NodeDaemonCommandStateSucceeded, record.State)
	require.Equal(t, map[string]any{
		"type":        "command.get",
		"get_command": map[string]any{"command_id": "cmd_upgrade"},
	}, f.client.query)
}

func TestSyncNodeDaemonCommandsRetracksMaintenanceAfterManagerRestart(t *testing.T) {
	f := newNodeDaemonCommandFixture(t)
	f.client.commandAck = json.RawMessage(
		`{"command_id":"cmd_restart","ok":true,"status":"received"}`,
	)
	_, _, err := f.svc.RestartNodeDaemon(
		f.ctx,
		auth.RequestMetadata{},
		domain.RestartNodeDaemonRequest{NodeID: "node_1", CommandID: "cmd_restart"},
	)
	require.NoError(t, err)
	// A fresh tracker has no memory of the command, as after a restart.
	tracker := &trackingNodeControlClient{
		fakeNodeControlClient: f.client,
		trackedStatus:         "confirmed",
	}
	f.svc.SetNodeControlClient(tracker)
	f.client.result = json.RawMessage(`{
		"type":"command.get",
		"command":{
			"command_id":"cmd_restart",
			"type":"paxd.restart",
			"status":"received",
			"result":{"requested_boot_id":"boot_old"}
		}
	}`)

	require.NoError(t, f.svc.SyncNodeDaemonCommands(f.ctx, "node_1"))

	require.Equal(t, "boot_old", tracker.tracked["cmd_restart"].RequestedBootID)
	require.Equal(t, domain.NodeDaemonCommandStateSucceeded, f.record(t, "cmd_restart").State)
}

func TestSyncNodeDaemonCommandsSkipsSettledRecords(t *testing.T) {
	f := newNodeDaemonCommandFixture(t)
	f.client.commandAck = json.RawMessage(`{"command_id":"cmd_1","ok":true,"status":"applied"}`)
	f.upgradePaxl(t, "cmd_1")
	require.Equal(t, domain.NodeDaemonCommandStateSucceeded, f.record(t, "cmd_1").State)

	require.NoError(t, f.svc.SyncNodeDaemonCommands(f.ctx, "node_1"))

	require.Zero(t, f.client.calls)
}

func TestListNodeDaemonCommandsFiltersByState(t *testing.T) {
	f := newNodeDaemonCommandFixture(t)
	f.client.commandAck = json.RawMessage(`{"command_id":"cmd_ok","ok":true,"status":"applied"}`)
	f.upgradePaxl(t, "cmd_ok")
	f.client.commandAck = json.RawMessage(
		`{"command_id":"cmd_pending","ok":true,"status":"received"}`,
	)
	f.upgradePaxl(t, "cmd_pending")

	status, data, err := f.svc.ListNodeDaemonCommands(
		f.ctx,
		auth.RequestMetadata{},
		"node_1",
		domain.NodeDaemonCommandFilter{State: domain.NodeDaemonCommandStateInProgress},
	)

	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	commands := data.(map[string]any)["commands"].([]domain.NodeDaemonCommand)
	require.Len(t, commands, 1)
	require.Equal(t, "cmd_pending", commands[0].CommandID)
}

func TestListNodeDaemonCommandsRejectsInvalidFilters(t *testing.T) {
	f := newNodeDaemonCommandFixture(t)
	for _, filter := range []domain.NodeDaemonCommandFilter{
		{State: "done"},
		{Limit: 101},
		{Limit: -1},
	} {
		_, _, err := f.svc.ListNodeDaemonCommands(f.ctx, auth.RequestMetadata{}, "node_1", filter)
		var appErr apperr.Error
		require.ErrorAs(t, err, &appErr)
		require.Equal(t, http.StatusBadRequest, appErr.Status)
	}
}

func TestListNodesAttachesLatestDaemonCommandPerType(t *testing.T) {
	f := newNodeDaemonCommandFixture(t)
	f.client.commandAck = json.RawMessage(`{"command_id":"x","ok":true,"status":"received"}`)
	_, _, err := f.store.commands.CreateNodeDaemonCommand(f.ctx, domain.NodeDaemonCommand{
		NodeID: "node_1", CommandID: "cmd_old", Type: domain.NodeDaemonCommandPaxlUpgrade,
		CreatedAt: fixedUserClock().Add(-time.Hour),
	})
	require.NoError(t, err)
	f.upgradePaxl(t, "cmd_new")
	_, _, err = f.svc.RestartNodeDaemon(
		f.ctx,
		auth.RequestMetadata{},
		domain.RestartNodeDaemonRequest{NodeID: "node_1", CommandID: "cmd_restart"},
	)
	require.NoError(t, err)
	f.store.MockStore.EXPECT().
		ListNodes(f.ctx, f.principal).
		Return([]domain.Node{f.node, {NodeID: "node_2"}}, nil).
		Once()

	_, data, err := f.svc.ListNodes(f.ctx, auth.RequestMetadata{})

	require.NoError(t, err)
	nodes := data.(map[string]any)["nodes"].([]domain.Node)
	ids := map[string]string{}
	for _, command := range nodes[0].LatestDaemonCommands {
		ids[command.Type] = command.CommandID
	}
	require.Equal(t, map[string]string{
		domain.NodeDaemonCommandPaxlUpgrade: "cmd_new",
		domain.NodeDaemonCommandPaxdRestart: "cmd_restart",
	}, ids)
	require.Empty(t, nodes[1].LatestDaemonCommands)
}

func expectNoNodeDaemonCommands(store *userapimocks.MockStore) {
	store.EXPECT().
		LatestNodeDaemonCommands(mock.Anything, mock.Anything).
		Return(map[string][]domain.NodeDaemonCommand{}, nil).
		Maybe()
}

// expectRecordedNodeDaemonCommand accepts the record writes made around a
// dispatch for tests that only assert on the forwarded command.
func expectRecordedNodeDaemonCommand(store *userapimocks.MockStore) {
	store.EXPECT().
		CreateNodeDaemonCommand(mock.Anything, mock.Anything).
		RunAndReturn(func(
			_ context.Context,
			command domain.NodeDaemonCommand,
		) (domain.NodeDaemonCommand, bool, error) {
			return command, true, nil
		}).
		Maybe()
	store.EXPECT().SaveNodeDaemonCommand(mock.Anything, mock.Anything).Return(nil).Maybe()
}
