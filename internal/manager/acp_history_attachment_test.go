package manager

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/storage"
)

func TestACPHistoryGivenAttachmentOnlyPromptWhenReopenedThenRetainsUserMessage(t *testing.T) {
	for _, turnID := range []string{"turn_attachment", ""} {
		t.Run("turn="+turnID, func(t *testing.T) {
			store := storage.NewMemoryStore(time.Now)
			agent := &ACPTunnelAgent{
				agentID:     "agent",
				ownerUserID: "user",
				sessionID:   "session",
				store:       store,
			}
			payload := []byte(
				`{"jsonrpc":"2.0","id":7,"method":"session/prompt","params":{"sessionId":"session","prompt":[{"type":"resource_link","uri":"file:///tmp/att_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/screenshot.png","name":"screenshot.png","mimeType":"image/png"}]}}`,
			)
			for range 2 {
				require.NoError(
					t,
					projectACPUserPromptForSessionTurn(
						t.Context(),
						agent,
						"session",
						turnID,
						payload,
					),
				)
			}
			page, err := store.ListMessageSummaryPage(t.Context(), "agent", "session", 0, 0, 100)
			require.NoError(t, err)
			require.Len(
				t,
				page.Messages,
				1,
				"attachment-only prompts must survive history reload without duplicates",
			)
			message := page.Messages[0]
			assert.Equal(t, "user", message.Role)
			assert.Equal(t, "user_message", message.MessageType)
			assert.Equal(t, turnID, message.TurnID)
			assert.JSONEq(t, string(payload), string(message.RawJSON))
			parts, err := store.ListMessageSummaryParts(t.Context(), []string{message.MessageID})
			require.NoError(t, err)
			require.Len(t, parts[message.MessageID], 1)
			assert.Empty(t, parts[message.MessageID][0].Text, "do not invent prompt text")
		})
	}
}

func TestACPHistoryGivenAttachmentPromptsWithoutIDsThenDifferentAttachmentsRemainDistinct(
	t *testing.T,
) {
	store := storage.NewMemoryStore(time.Now)
	agent := &ACPTunnelAgent{
		agentID:     "agent",
		ownerUserID: "user",
		sessionID:   "session",
		store:       store,
	}
	for _, name := range []string{"one", "two", "one"} {
		payload := []byte(
			fmt.Sprintf(
				`{"method":"session/prompt","params":{"sessionId":"session","prompt":[{"type":"resource_link","uri":"attachment://%s","name":"image.png"}]}}`,
				name,
			),
		)
		require.NoError(t, projectACPUserPrompt(t.Context(), agent, payload))
	}
	messages, err := store.ListMessages(t.Context(), "agent", "session", 100)
	require.NoError(t, err)
	require.Len(t, messages, 2)
	assert.NotEqual(t, messages[0].MessageID, messages[1].MessageID)
}

func TestACPHistoryGivenEmptyPromptThenDoesNotCreateHistory(t *testing.T) {
	for _, prompt := range []string{`[]`, `"invalid-prompt"`, `[{"type":"text","text":""}]`, `[{"type":"resource_link","uri":" "}]`} {
		t.Run(prompt, func(t *testing.T) {
			store := storage.NewMemoryStore(time.Now)
			agent := &ACPTunnelAgent{
				agentID:     "agent",
				ownerUserID: "user",
				sessionID:   "session",
				store:       store,
			}
			payload := json.RawMessage(
				`{"id":7,"method":"session/prompt","params":{"sessionId":"session","prompt":` + prompt + `}}`,
			)
			require.NoError(t, projectACPUserPrompt(t.Context(), agent, payload))
			messages, err := store.ListMessages(t.Context(), "agent", "session", 100)
			require.NoError(t, err)
			assert.Empty(t, messages)
		})
	}
}
