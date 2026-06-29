package userapi

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestRenderTeamMemexIndex(t *testing.T) {
	updated := time.Date(2026, 6, 28, 10, 0, 0, 0, time.FixedZone("PDT", -7*60*60))

	t.Run("Given no documents then index is still a valid markdown root", func(t *testing.T) {
		require.Equal(t, "# Team LLM Wiki\n", renderTeamMemexIndex(nil))
	})

	t.Run("Given documents then index groups and sorts by path", func(t *testing.T) {
		index := renderTeamMemexIndex([]domain.TeamMemexDocument{
			{
				Path:      "runtime/sessions.md",
				Title:     "Sessions",
				Summary:   "How sessions are stored",
				UpdatedAt: updated,
			},
			{
				Path:      "architecture.md",
				Title:     "Architecture",
				Summary:   "System boundaries",
				UpdatedAt: updated.Add(time.Hour),
			},
			{
				Path:      "runtime/messages.md",
				Title:     "Messages",
				Summary:   "Message chunking rules",
				UpdatedAt: updated.Add(2 * time.Hour),
			},
		})

		require.Equal(t, `# Team LLM Wiki

## root

- [Architecture](architecture.md) - System boundaries. Updated 2026-06-28.

## runtime

- [Messages](runtime/messages.md) - Message chunking rules. Updated 2026-06-28.
- [Sessions](runtime/sessions.md) - How sessions are stored. Updated 2026-06-28.
`, index)
	})
}

func TestNormalizeTeamMemexDocumentPath(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "clean relative path",
			input: "runtime/../runtime/sessions.md",
			want:  "runtime/sessions.md",
		},
		{name: "trim whitespace", input: " docs/index.md ", want: "docs/index.md"},
		{name: "empty", input: " ", want: ""},
		{name: "current dir", input: ".", want: ""},
		{name: "parent traversal", input: "../secret.md", want: ""},
		{name: "absolute path", input: "/secret.md", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, normalizeTeamMemexDocumentPath(tt.input))
		})
	}
}

func TestTeamMemexDocumentResponseExcludesInternalFields(t *testing.T) {
	response := teamMemexDocumentResponseFromDomain(domain.TeamMemexDocument{
		DocumentID: "memex_doc_1",
		TeamID:     "team_1",
		Path:       "runtime/sessions.md",
		Title:      "Sessions",
		Summary:    "How sessions are stored",
		Tags:       json.RawMessage(`["runtime"]`),
		BodyMD:     "# Sessions\n",
		Status:     domain.TeamMemexDocumentStatusActive,
		CreatedAt:  time.Date(2026, 6, 28, 9, 0, 0, 0, time.UTC),
		UpdatedAt:  time.Date(2026, 6, 28, 10, 0, 0, 0, time.UTC),
	})
	raw, err := json.Marshal(response)
	require.NoError(t, err)

	require.JSONEq(t, `{
		"path":"runtime/sessions.md",
		"title":"Sessions",
		"summary":"How sessions are stored",
		"tags":["runtime"],
		"body_md":"# Sessions\n",
		"updated_at":"2026-06-28T10:00:00Z"
	}`, string(raw))
	require.NotContains(t, string(raw), "document_id")
	require.NotContains(t, string(raw), "team_id")
	require.NotContains(t, string(raw), "status")
	require.NotContains(t, string(raw), "created_at")
}
