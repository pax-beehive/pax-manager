package manager

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/auth"
	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/secretary"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
)

func TestSecretaryToolsGivenLongUserPreviewWhenReadingMiddleThenRequiresItsExactReadReceipt(
	t *testing.T,
) {
	srv, fixture, job, _, capability := secretaryToolsFixture(t)
	const messageID = "long-user-source"
	const hidden = "The hidden requirement is to keep the old database."
	body := "Visible opening.\n\n" + strings.Repeat("h", 32*1024) + hidden +
		strings.Repeat("m", 282*1024) + "\n\nVisible closing."
	putSecretaryToolMessage(t, srv, fixture, messageID, "user", body)
	sources := srv.store.(canonicalSessionStore).Store.(domain.SearchSourceStore)
	document, err := sources.ReadSearchDocument(t.Context(), job.OwnerUserID, "message", messageID)
	require.NoError(t, err)
	job.BatchID, job.SourceID, job.Messages = "preview-batch", "preview-source", []domain.SearchDocument{
		document,
	}
	job.InputPolicyVersion = domain.SecretaryInputPolicyVersion
	prompt, err := secretary.BuildNodePrompt(job)
	require.NoError(t, err)
	assert.Contains(t, prompt, "Visible opening.")
	assert.Contains(t, prompt, "Visible closing.")
	assert.Contains(t, prompt, `"view":"head_tail"`)
	assert.NotContains(t, prompt, hidden)
	assert.Empty(t, srv.secretaryToolsReads(capability).Messages)

	output := longUserPreviewPublication(t, messageID, hidden, "")
	_, err = secretary.BuildNodePublication(job, output, domain.SecretaryToolReads{})
	require.ErrorContains(t, err, "secretary evidence was not present")

	arguments, err := json.Marshal(userapi.WorkspaceReadRequest{
		Ref: userapi.SearchRef{Kind: "message", ID: messageID,
			ContentHash: document.ContentHash, CanonicalizationVersion: document.CanonicalizationVersion},
		View: "full", Offset: strings.Index(body, hidden), Limit: len(hidden),
	})
	require.NoError(t, err)
	data, err := secretaryToolCall(t, srv, fixture, capability, "read", string(arguments))
	require.NoError(t, err)
	read := data.(userapi.WorkspaceMessageRead)
	assert.Equal(t, hidden, read.Text)
	assert.Equal(t, "full", read.View)
	assert.Equal(t, document.ContentHash, read.Ref.ContentHash)
	assert.Equal(t, document.CanonicalizationVersion, read.Ref.CanonicalizationVersion)
	require.NotEmpty(t, read.ReadReceipt)
	ledger := srv.secretaryToolsReads(capability)
	require.Len(t, ledger.Messages, 1)
	assert.Equal(t, hidden, ledger.Messages[0].Text)

	_, err = secretary.BuildNodePublication(job, output, ledger)
	require.ErrorContains(t, err, "secretary evidence was not present",
		"an unbound quote cannot borrow a separate tool read")
	publication, err := secretary.BuildNodePublication(job,
		longUserPreviewPublication(t, messageID, hidden, read.ReadReceipt), ledger)
	require.NoError(t, err)
	require.Len(t, publication.Outputs, 1)
	evidence := publication.Outputs[0].Request.Claims[0].Evidence[0]
	assert.Equal(t, hidden, evidence.Quote)
	assert.Equal(t, "full", evidence.View)
	assert.Equal(t, read.ReadReceipt, evidence.ReadReceipt)
	assert.Equal(t, document.ContentHash, evidence.Message.ContentHash)
	assert.Equal(t, document.CanonicalizationVersion, evidence.Message.CanonicalizationVersion)
	verified, err := srv.workspaceSearch.VerifyReadReceipt(t.Context(), job.OwnerUserID,
		evidence.Message, evidence.ReadReceipt)
	require.NoError(t, err)
	assert.Equal(t, hidden, verified.Text)

	_, err = srv.workspaceSearch.WithPrincipal(secretaryToolPrincipal{owner: "foreign"}).
		Read(t.Context(), auth.RequestMetadata{}, userapi.WorkspaceReadRequest{Ref: read.Ref, View: "full"})
	require.ErrorIs(t, err, domain.ErrNotFound)
	_, err = srv.workspaceSearch.VerifyReadReceipt(
		t.Context(),
		"foreign",
		evidence.Message,
		evidence.ReadReceipt,
	)
	require.Error(t, err)
	assert.NotContains(t, err.Error(), hidden)
}

func TestSecretaryToolsGivenLongOriginalWhenPagingFullThenPreservesBodyVersionAndBudget(
	t *testing.T,
) {
	srv, fixture, job, _, capability := secretaryToolsFixture(t)
	const messageID = "paged-long-user-source"
	body := strings.Repeat("full original message\n", 16*1024)
	putSecretaryToolMessage(t, srv, fixture, messageID, "user", body)
	request := userapi.WorkspaceReadRequest{
		Ref: userapi.SearchRef{Kind: "message", ID: messageID}, View: "full",
	}
	var combined strings.Builder
	pageCount := 0
	for {
		arguments, err := json.Marshal(request)
		require.NoError(t, err)
		data, err := secretaryToolCall(t, srv, fixture, capability, "read", string(arguments))
		require.NoError(t, err)
		read := data.(userapi.WorkspaceMessageRead)
		pageCount++
		require.LessOrEqual(t, pageCount, 32)
		assert.LessOrEqual(t, len(read.Text), 16*1024)
		assert.Equal(t, "full", read.View)
		assert.Equal(t, domain.SearchContentHash(body), read.Ref.ContentHash)
		verified, err := srv.workspaceSearch.VerifyReadReceipt(t.Context(), job.OwnerUserID,
			domain.MessageVersionRef{MessageID: read.MessageID, ContentHash: read.Ref.ContentHash,
				CanonicalizationVersion: read.Ref.CanonicalizationVersion}, read.ReadReceipt)
		require.NoError(t, err)
		assert.Equal(t, read.Text, verified.Text)
		combined.WriteString(read.Text)
		if !read.HasMore {
			break
		}
		require.Greater(t, read.NextOffset, request.Offset)
		request.Ref, request.Offset = read.Ref, read.NextOffset
	}
	assert.Greater(t, pageCount, 1)
	assert.Equal(t, body, combined.String())
	assert.Len(t, srv.secretaryToolsReads(capability).Messages, pageCount)
	value, ok := srv.secretaryTools.Load(capability)
	require.True(t, ok)
	assert.Equal(t, len(body), value.(*secretaryToolGrant).readBytes)
}

func longUserPreviewPublication(t *testing.T, messageID, quote, receipt string) string {
	t.Helper()
	evidence := map[string]any{"message_id": messageID, "quote": quote}
	if receipt != "" {
		evidence["view"], evidence["read_receipt"] = "full", receipt
	}
	encoded, err := json.Marshal(map[string]any{
		"summaries": []any{map[string]any{
			"action": "create", "proposal_key": "database", "title": "Database requirement",
			"body": "Keep the old database.", "source_message_ids": []string{messageID},
			"claims": []any{
				map[string]any{"text": "Keep the old database.", "evidence": []any{evidence}},
			},
		}},
		"outcomes": []any{map[string]any{
			"message_id": messageID, "action": "summarized", "summary_keys": []string{"database"},
		}},
	})
	require.NoError(t, err)
	return string(encoded)
}
