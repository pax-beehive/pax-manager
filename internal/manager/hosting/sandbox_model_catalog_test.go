package hosting_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/hosting"
)

func TestSandboxModelReadyProjectsOnlyActiveCodexCatalogAliases(t *testing.T) {
	ctx := t.Context()
	s, store, owner, _, policies := poolFixture(t)
	parent, _ := connectModelSandbox(t, s, store, owner)
	job := wakeSandboxModel(t, s, owner, "sandbox-codex")
	ready := modelSandboxReady(job)
	for _, ids := range [][]string{
		{}, {"invalid model"}, {strings.Repeat("x", 129)}, {policies[0].ID, policies[0].ID},
		make([]string, 129),
	} {
		ready.ModelIDs = ids
		_, err := s.UpdateSandboxModel(ctx, &ready, true)
		require.ErrorIs(t, err, hosting.ErrConflict)
	}
	ready.ModelIDs = []string{policies[1].ID, "unknown-model", policies[0].ID}
	_, err := s.UpdateSandboxModel(ctx, &ready, true)
	require.NoError(t, err)
	readAliases := func() *[]string {
		status := readSandboxModelAccess(t, s, owner)
		return status.SandboxModelAccess.Agents[0].ModelIDs
	}
	require.NotNil(t, readAliases())
	require.Equal(t, []string{policies[1].ID, policies[0].ID}, *readAliases())
	adminUpdate(t, s, &hosting.AdminUpdate{
		Action: "model", TargetID: policies[1].ID, Enabled: boolean(false),
	})
	require.Equal(t, []string{policies[0].ID}, *readAliases())
	adminUpdate(t, s, &hosting.AdminUpdate{
		Action: "model", TargetID: policies[0].ID, Enabled: boolean(false),
	})
	require.Equal(t, []string{}, *readAliases())

	pi := wakeSandboxModel(t, s, owner, "sandbox-pi")
	piReady := modelSandboxReady(pi)
	piReady.ModelIDs = []string{policies[0].ID}
	_, err = s.UpdateSandboxModel(ctx, &piReady, true)
	require.ErrorIs(t, err, hosting.ErrConflict)
	piReady.ModelIDs = nil
	_, err = s.UpdateSandboxModel(ctx, &piReady, true)
	require.NoError(t, err)
	sleepModelSandbox(t, s, parent)
	require.Nil(t, readAliases())
	resumeModelSandbox(t, s, owner)
	resumed := claimModelPermission(t, s)
	require.NotNil(t, resumed)
	require.Equal(t, "codex", resumed.Harness)
	oldWorkerReady := modelSandboxReady(resumed)
	_, err = s.UpdateSandboxModel(ctx, &oldWorkerReady, true)
	require.NoError(t, err)
	require.Nil(t, readAliases())
}
