package manager

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/storage"
)

func TestSecretaryControlGivenOwnerThenHTTPManualSummaryAndEditKeepExactProgress(t *testing.T) {
	assertSecretaryControlPersistence(t, storage.NewMemoryStore(time.Now))
}

func assertSecretaryControlPersistence(t *testing.T, store Store) {
	t.Helper()
	s := newServer(Config{LocalUserID: "local@example.test", AllowLocalUserHeader: true}, store)
	t.Cleanup(func() { require.NoError(t, s.CloseTransportStore(context.Background())) })
	f := testNodeAgent(t, s, "control-persistence@example.test")
	p := testUserPrincipal(t, s, f.userEmail)
	createConversationTestSession(t, s, f, "control-source", "control-native")
	sessions, err := store.ListAgentSessions(t.Context(), p, f.agentID)
	require.NoError(t, err)
	require.Len(t, sessions, 1)
	sourceID := fmt.Sprint(sessions[0].ID)
	for index, id := range []string{"control-early", "control-late"} {
		message := domain.Message{
			MessageID:   id,
			AgentID:     f.agentID,
			SessionID:   "control-source",
			Role:        "user",
			MessageType: "user_message",
			Source:      domain.MessageSourceACPTunnel,
			Direction:   domain.MessageDirectionUserToAgent,
			SessionSeq:  int64(index + 1),
			TurnID:      id,
			Status:      "complete",
		}
		require.NoError(t, store.UpsertMessage(t.Context(), &message))
		require.NoError(
			t,
			store.UpsertMessagePart(
				t.Context(),
				&domain.MessagePart{MessageID: id, PartType: domain.MessagePartText, Text: id},
			),
		)
	}
	base := "/api/v1/user/self/secretary"
	for _, query := range []string{"scope=user", "scope=session&source_id=" + sourceID} {
		r := taskHTTPRequest(
			t,
			s,
			http.MethodPut,
			base+"/guidance?"+query,
			f.userEmail,
			`{"instruction":"Keep decisions concise.","expected_revision":0}`,
		)
		require.Equal(t, 200, r.Code, r.Body.String())
	}
	prepare := func(request domain.SecretaryOperationPrepare) domain.SecretaryOperation {
		t.Helper()
		raw, marshalErr := json.Marshal(request)
		require.NoError(t, marshalErr)
		r := taskHTTPRequest(
			t,
			s,
			http.MethodPost,
			base+"/operations/prepare",
			f.userEmail,
			string(raw),
		)
		require.Equal(t, 201, r.Code, r.Body.String())
		var result struct {
			Data domain.SecretaryOperation `json:"data"`
		}
		require.NoError(t, json.Unmarshal(r.Body.Bytes(), &result))
		return result.Data
	}
	submit := func(op domain.SecretaryOperation, request domain.SecretaryOperationSubmit) domain.SecretaryOperation {
		t.Helper()
		raw, marshalErr := json.Marshal(request)
		require.NoError(t, marshalErr)
		r := taskHTTPRequest(
			t,
			s,
			http.MethodPost,
			base+"/operations/"+op.OperationID+"/submit",
			f.userEmail,
			string(raw),
		)
		require.Equal(t, 200, r.Code, r.Body.String())
		var result struct {
			Data domain.SecretaryOperation `json:"data"`
		}
		require.NoError(t, json.Unmarshal(r.Body.Bytes(), &result))
		return result.Data
	}
	op := prepare(
		domain.SecretaryOperationPrepare{
			SourceID:   sourceID,
			Kind:       "human_summary",
			Scope:      "selected",
			MessageIDs: []string{"control-late"},
		},
	)
	require.Len(t, op.Targets, 1)
	require.NotNil(t, op.Instructions)
	assert.Equal(t, "Keep decisions concise.", op.Instructions.Session.Instruction)
	input := domain.SecretaryOperationSubmit{
		RequestID:        "human-create",
		ExpectedRevision: op.Revision,
		Title:            "Manual releases",
		Body:             "Many repeated manual releases.",
	}
	done := submit(op, input)
	assert.Equal(t, "completed", done.Status)
	require.Len(t, done.ResultRefs, 1)
	assert.Equal(
		t,
		done.ResultRefs,
		submit(op, input).ResultRefs,
		"an identical retry never creates a second revision",
	)
	summary, err := store.(domain.SummaryStore).GetSummary(
		t.Context(),
		p.User.UserID,
		done.ResultRefs[0].SummaryID,
		0,
	)
	require.NoError(t, err)
	assert.Equal(t, "user", summary.AuthorKind)
	assert.Equal(t, p.User.UserID, summary.AuthorID)
	assert.Equal(t, done.OperationID, summary.OperationID)
	require.Len(t, summary.Coverage, 1)
	assert.Equal(t, "control-late", summary.Coverage[0].MessageID)
	edit := prepare(
		domain.SecretaryOperationPrepare{
			SourceID:  sourceID,
			Kind:      "human_edit",
			SummaryID: summary.SummaryID,
		},
	)
	assert.Empty(t, edit.Targets)
	revised := submit(
		edit,
		domain.SecretaryOperationSubmit{
			RequestID:               "human-edit",
			ExpectedRevision:        edit.Revision,
			SummaryID:               summary.SummaryID,
			ExpectedSummaryRevision: summary.Revision,
			Title:                   "Manual releases",
			Body:                    "Many repeated manual releases; only outcomes matter.",
		},
	)
	require.Len(t, revised.ResultRefs, 1)
	assert.Equal(t, summary.Revision+1, revised.ResultRefs[0].Revision)
	current, err := store.(domain.SummaryStore).GetSummary(
		t.Context(),
		p.User.UserID,
		summary.SummaryID,
		0,
	)
	require.NoError(t, err)
	assert.Equal(t, summary.Coverage, current.Coverage)
	old, err := store.(domain.SummaryStore).GetSummary(
		t.Context(),
		p.User.UserID,
		summary.SummaryID,
		summary.Revision,
	)
	require.NoError(t, err)
	assert.Equal(t, summary.Body, old.Body, "editing publishes a new immutable revision")
	progress, err := store.(domain.SecretaryProgressStore).ReadSecretaryProgress(
		t.Context(),
		p.User.UserID,
		domain.SecretaryProgressQuery{},
	)
	require.NoError(t, err)
	assert.Equal(t, 1, progress.Counts.CompletedMessages)
	assert.Equal(
		t,
		1,
		progress.Counts.PendingMessages,
		"editing the summary does not complete the earlier message",
	)
}
