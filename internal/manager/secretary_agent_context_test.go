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
	assert.ErrorIs(t, err, domain.ErrSecretaryFenced)
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
	assert.ErrorIs(t, err, domain.ErrSecretaryInvalidReadReceipt)
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

func TestSecretaryAgentContextGivenRecoveredDeltaExpansionThenReverifiesProofWithoutRelatedSource(
	t *testing.T,
) {
	srv, fixture, job, _, _ := secretaryToolsFixture(t)
	job.SourceID = "1"
	putSecretaryToolMessage(
		t,
		srv,
		fixture,
		"expanded-delta",
		"assistant",
		"Earlier explanation.\n\nFinal decision.",
	)
	readData, err := srv.workspaceSearch.WithPrincipal(secretaryToolPrincipal{owner: job.OwnerUserID}).
		Read(t.Context(), auth.RequestMetadata{}, userapi.WorkspaceReadRequest{
			Ref: userapi.SearchRef{
				Kind: "message",
				ID:   "expanded-delta",
			}, View: "full",
		})
	require.NoError(t, err)
	read := readData.(userapi.WorkspaceMessageRead)
	doc := domain.SearchDocument{
		Kind:                    "message",
		SourceID:                read.MessageID,
		OwnerUserID:             job.OwnerUserID,
		AgentID:                 read.AgentID,
		SessionID:               read.SessionID,
		Role:                    read.Role,
		Body:                    read.Text,
		ContentHash:             read.Ref.ContentHash,
		CanonicalizationVersion: read.Ref.CanonicalizationVersion,
	}
	job.Messages = []domain.SearchDocument{doc}
	output := func(receipt string) string {
		value, marshalErr := json.Marshal(
			map[string]any{"body": "Updated", "claims": []map[string]any{{
				"text": "Earlier", "evidence": []map[string]string{{"message_id": doc.SourceID, "quote": "Earlier explanation", "view": "full", "read_receipt": receipt}},
			}}},
		)
		require.NoError(t, marshalErr)
		return string(value)
	}
	request := secretary.ExecutionRequest{OwnerID: job.OwnerUserID, Job: job}
	result, err := srv.finishSecretaryExecution(
		t.Context(),
		request,
		secretary.ExecutionResult{Text: output(read.ReadReceipt)},
	)
	require.NoError(t, err)
	require.Len(t, result.Reads.Messages, 1)
	assert.Equal(t, doc.Body, result.Reads.Messages[0].Text)
	assert.Equal(t, "full", result.Reads.Messages[0].View)
	_, err = srv.finishSecretaryExecution(
		t.Context(),
		request,
		secretary.ExecutionResult{Text: output("forged")},
	)
	assert.Error(t, err)
	request.OwnerID = "foreign"
	_, err = srv.finishSecretaryExecution(
		t.Context(),
		request,
		secretary.ExecutionResult{Text: output(read.ReadReceipt)},
	)
	assert.Error(t, err)
}

func TestSecretaryAgentContextGivenRecoveredRelatedEvidenceThenVerifiesIndependentRead(
	t *testing.T,
) {
	srv, fixture, job, _, _ := secretaryToolsFixture(t)
	job.SourceID, job.TargetSummaryID = "1", "summary-target"
	putSecretaryToolMessage(t, srv, fixture, "changed-message", "user", "A source update.")
	putSecretaryToolMessage(
		t,
		srv,
		fixture,
		"related-pages",
		"assistant",
		"First explanation.\n\nLast decision.",
	)
	reader := srv.workspaceSearch.WithPrincipal(secretaryToolPrincipal{owner: job.OwnerUserID})
	versions := make(map[string]userapi.SearchRef)
	readSource := func(t *testing.T, id, view string, offset, limit int) userapi.WorkspaceMessageRead {
		t.Helper()
		ref := versions[id]
		if ref.ID == "" {
			ref = userapi.SearchRef{Kind: "message", ID: id}
		}
		data, err := reader.Read(t.Context(), auth.RequestMetadata{}, userapi.WorkspaceReadRequest{
			Ref: ref, View: view, Offset: offset, Limit: limit,
		})
		require.NoError(t, err)
		read := data.(userapi.WorkspaceMessageRead)
		versions[id] = read.Ref
		return read
	}
	delta := readSource(t, "changed-message", "full", 0, 0)
	job.Messages = []domain.SearchDocument{{
		Kind: "message", SourceID: delta.MessageID, OwnerUserID: job.OwnerUserID,
		AgentID: delta.AgentID, SessionID: delta.SessionID, Role: delta.Role, Body: delta.Text,
		ContentHash: delta.Ref.ContentHash, CanonicalizationVersion: delta.Ref.CanonicalizationVersion,
	}}
	for _, test := range []struct {
		name    string
		view    string
		offset  int
		limit   int
		invalid string
	}{
		{name: "another page", view: "full", offset: 6, limit: 6},
		{name: "another view", view: "last"},
		{name: "forged proof", view: "last", invalid: "receipt"},
		{name: "wrong owner", view: "last", invalid: "owner"},
		{name: "wrong source version", view: "last", invalid: "version"},
		{name: "unselected source", view: "last", invalid: "selection"},
		{name: "wrong exposed view", view: "last", invalid: "view"},
		{name: "unseen quote", view: "last", invalid: "quote"},
	} {
		t.Run(test.name, func(t *testing.T) {
			first := readSource(t, "related-pages", "full", 0, 6)
			second := readSource(t, "related-pages", test.view, test.offset, test.limit)
			selected := domain.MessageVersionRef{
				MessageID: first.MessageID, ContentHash: first.Ref.ContentHash,
				CanonicalizationVersion: first.Ref.CanonicalizationVersion, ReadReceipt: first.ReadReceipt,
			}
			evidence := map[string]string{
				"message_id": second.MessageID, "quote": second.Text,
				"view": second.View, "read_receipt": second.ReadReceipt,
			}
			request := secretary.ExecutionRequest{OwnerID: job.OwnerUserID, Job: job}
			sources := []domain.MessageVersionRef{selected}
			switch test.invalid {
			case "receipt":
				evidence["read_receipt"] = "forged"
			case "owner":
				request.OwnerID = "foreign-owner"
			case "version":
				sources[0].ContentHash = "different-version"
			case "selection":
				sources = nil
			case "view":
				evidence["view"] = "first"
			case "quote":
				evidence["quote"] = "Text never exposed."
			}
			output, err := json.Marshal(map[string]any{
				"body": "Updated", "related_sources": sources,
				"claims": []map[string]any{
					{"text": "Related fact", "evidence": []map[string]string{evidence}},
				},
			})
			require.NoError(t, err)
			result, err := srv.finishSecretaryExecution(
				t.Context(),
				request,
				secretary.ExecutionResult{Text: string(output)},
			)
			if err == nil {
				_, err = secretary.BuildPublicationWithReads(job, result.Text, result.Reads)
			}
			if test.invalid != "" {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Len(t, result.Reads.Messages, 2)
			assert.Equal(t, second.Text, result.Reads.Messages[1].Text)
			assert.Equal(t, second.ReadReceipt, result.Reads.Messages[1].ReadReceipt)
		})
	}
}
