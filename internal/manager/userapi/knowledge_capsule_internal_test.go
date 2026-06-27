package userapi

import (
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestTruncateStringPreservesUTF8(t *testing.T) {
	input := strings.Repeat("界", knowledgeContentLimit+1)

	output := truncateString(input, knowledgeContentLimit)

	require.True(t, utf8.ValidString(output))
	require.Equal(t, knowledgeContentLimit, utf8.RuneCountInString(output))
	require.True(t, strings.HasSuffix(output, "..."))
}

func TestExtractKnowledgeContentUsesRaisedLineLimit(t *testing.T) {
	history := make([]domain.MessageWithParts, 0, 45)
	for i := range 45 {
		messageID := fmt.Sprintf("msg_%03d", i)
		history = append(history, domain.MessageWithParts{
			Message: domain.Message{
				MessageID: messageID,
				Role:      "assistant",
				CreatedAt: time.Date(2026, 6, 27, 7, i, 0, 0, time.UTC),
			},
			Parts: []domain.MessagePart{{
				MessageID: messageID,
				PartType:  domain.MessagePartText,
				Text:      fmt.Sprintf("paxl limit detail line %03d", i),
			}},
		})
	}

	content, _, truncated := extractKnowledgeContent("paxl limit", history)

	require.False(t, truncated)
	require.Contains(t, content, "paxl limit detail line 044")
}

func TestImportedEnvelopeCapsuleContentMarksManagerTruncation(t *testing.T) {
	rawContent := strings.Repeat("界", knowledgeContentLimit+5)

	content, truncated, originalEstimatedChars := importedEnvelopeCapsuleContent(
		paxlKnowledgeCapsulePayloadCapsule{
			Content:                rawContent,
			Truncated:              false,
			OriginalEstimatedChars: 42,
		},
	)

	require.True(t, utf8.ValidString(content))
	require.Equal(t, knowledgeContentLimit, utf8.RuneCountInString(content))
	require.True(t, strings.HasSuffix(content, "..."))
	require.True(t, truncated)
	require.Equal(t, int64(knowledgeContentLimit+5), originalEstimatedChars)
}
