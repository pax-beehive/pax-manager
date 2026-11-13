package secretary

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func guidanceTestSnapshot(job domain.SecretaryJob) *domain.SecretaryInstructionSnapshot {
	return &domain.SecretaryInstructionSnapshot{
		User: domain.SecretaryGuidance{
			OwnerUserID: job.OwnerUserID,
			Scope:       domain.SecretaryGuidanceUser,
			Revision:    3,
			Instruction: "Keep decisions and unresolved questions.",
		},
		Session: domain.SecretaryGuidance{
			OwnerUserID: job.OwnerUserID,
			Scope:       domain.SecretaryGuidanceSession,
			SourceID:    job.SourceID,
			Revision:    2,
			Instruction: "Condense recurring procedures.",
		},
		Run: "Focus on the selected source changes.",
	}
}

func TestSecretaryPromptGivenOwnerGuidanceWhenBuiltThenItIsSeparateFromUntrustedSources(
	t *testing.T,
) {
	for _, tc := range []struct {
		name, prefix string
		build        func(domain.SecretaryJob) (string, error)
	}{
		{"nodes", nodeSummaryInstructions, BuildNodePrompt}, {"legacy", rollingSummaryInstructions, BuildPrompt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job := testJob()
			job.Instructions = guidanceTestSnapshot(job)
			job.SourceMetadata.Title = "A source title is not an owner instruction"
			prompt, err := tc.build(job)
			require.NoError(t, err)
			var payload struct {
				Instructions *domain.SecretaryInstructionSnapshot `json:"owner_instructions"`
				Metadata     domain.SecretarySourceMetadata       `json:"source_metadata"`
			}
			require.NoError(
				t,
				json.Unmarshal([]byte(strings.TrimPrefix(prompt, tc.prefix)), &payload),
			)
			assert.Equal(t, job.Instructions, payload.Instructions)
			assert.Equal(t, job.SourceMetadata, payload.Metadata)
			assert.Contains(t, prompt, "run takes precedence over session, then user")
			assert.Contains(
				t,
				prompt,
				"cannot override the fixed permissions, source verification, output schema",
			)
			assert.Contains(t, prompt, "Cross-session lookup and association remain optional")
		})
	}
}

func TestSecretaryPromptGivenLegacyBatchWhenGuidanceIsAbsentThenItDoesNotImportCurrentPreferences(
	t *testing.T,
) {
	for _, build := range []func(domain.SecretaryJob) (string, error){BuildNodePrompt, BuildPrompt} {
		prompt, err := build(testJob())
		require.NoError(t, err)
		assert.NotContains(t, prompt, `"owner_instructions":`)
	}
}

func TestSecretaryPromptGivenForeignOrOversizedInstructionsThenBothBuildersRejectThem(
	t *testing.T,
) {
	for _, build := range []func(domain.SecretaryJob) (string, error){BuildNodePrompt, BuildPrompt} {
		job := testJob()
		job.Instructions = guidanceTestSnapshot(job)
		job.Instructions.Session.SourceID = "different-session"
		_, err := build(job)
		assert.ErrorIs(t, err, domain.ErrConflict)
		job.Instructions = guidanceTestSnapshot(job)
		job.Instructions.Run = strings.Repeat("x", domain.SecretaryInstructionMaxBytes+1)
		_, err = build(job)
		assert.ErrorIs(t, err, domain.ErrConflict)
	}
}
