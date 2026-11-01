package manager

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/secretary"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
)

func TestSecretaryAgentContextGivenCapabilityThenOnlyInternalMCPReceivesIt(t *testing.T) {
	session := domain.AgentSession{AgentID: "agent", SessionID: "sess_secretary_internal"}
	ordinary := secretaryConversationMCPServers(session, "")
	assert.Equal(t, agentConversationMCPServers(session), ordinary)
	servers := secretaryConversationMCPServers(session, "private-capability")
	require.Len(t, servers, 1)
	server := servers[0].(map[string]any)
	assert.Contains(
		t,
		server["env"],
		map[string]string{"name": "PAX_SECRETARY_CAPABILITY", "value": "private-capability"},
	)
	encoded, err := json.Marshal(ordinary)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "private-capability")
	session.AgentType = "openclaw"
	assert.Empty(t, secretaryConversationMCPServers(session, "private-capability"))
	var external conversationRequest
	require.NoError(
		t,
		json.Unmarshal([]byte(`{"secretaryCapability":"attacker","input":"text"}`), &external),
	)
	assert.Empty(
		t,
		external.secretaryCapability,
		"public conversation JSON cannot inject a tool grant",
	)
}

func TestSecretaryAgentContextGivenRecoveredOutputThenVerifiesSelectedReadReceipt(t *testing.T) {
	srv, fixture, job, _, _ := secretaryToolsFixture(t)
	job.SourceID = "1"
	putSecretaryToolMessage(
		t,
		srv,
		fixture,
		"related-proof",
		"assistant",
		"First paragraph\n\nLast paragraph",
	)
	readData, err := srv.workspaceSearch.WithPrincipal(secretaryToolPrincipal{owner: job.OwnerUserID}).
		Read(
			t.Context(),
			auth.RequestMetadata{}, userapi.WorkspaceReadRequest{Ref: userapi.SearchRef{Kind: "message", ID: "related-proof"}, View: "last"},
		)
	require.NoError(t, err)
	read := readData.(userapi.WorkspaceMessageRead)
	ref := domain.MessageVersionRef{
		MessageID:               read.MessageID,
		ContentHash:             read.Ref.ContentHash,
		CanonicalizationVersion: read.Ref.CanonicalizationVersion,
		ReadReceipt:             read.ReadReceipt,
	}
	output, err := json.Marshal(
		map[string]any{"body": "Updated", "related_sources": []domain.MessageVersionRef{ref}},
	)
	require.NoError(t, err)
	request := secretary.ExecutionRequest{OwnerID: job.OwnerUserID, Job: job}
	result, err := srv.finishSecretaryExecution(
		t.Context(),
		request,
		secretary.ExecutionResult{Text: string(output)},
	)
	require.NoError(t, err)
	require.Len(t, result.Reads.Messages, 1)
	assert.Equal(t, "Last paragraph", result.Reads.Messages[0].Text)
	assert.Equal(t, "last", result.Reads.Messages[0].View)
	assert.Equal(t, ref, result.Reads.Messages[0].Ref)
	request.OwnerID = "other-owner"
	_, err = srv.finishSecretaryExecution(
		t.Context(),
		request,
		secretary.ExecutionResult{Text: string(output)},
	)
	assert.Error(t, err)
	request.OwnerID = job.OwnerUserID
	ref.ReadReceipt = "forged"
	output, err = json.Marshal(
		map[string]any{"body": "Updated", "related_sources": []domain.MessageVersionRef{ref}},
	)
	require.NoError(t, err)
	_, err = srv.finishSecretaryExecution(
		t.Context(),
		request,
		secretary.ExecutionResult{Text: string(output)},
	)
	assert.Error(t, err)
	_, err = srv.finishSecretaryExecution(
		t.Context(),
		request,
		secretary.ExecutionResult{Text: "not json"},
	)
	assert.Error(t, err)
	plain, err := srv.finishSecretaryExecution(
		t.Context(),
		secretary.ExecutionRequest{},
		secretary.ExecutionResult{Text: "plain response"},
	)
	require.NoError(t, err)
	assert.Equal(t, "plain response", plain.Text)
}
