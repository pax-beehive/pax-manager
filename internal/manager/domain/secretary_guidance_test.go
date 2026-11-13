package domain

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSecretaryGuidanceGivenScopeWhenValidatedThenOnlyCanonicalOwnerAndSessionScopesPass(
	t *testing.T,
) {
	for _, tc := range []struct {
		scope, source string
		valid         bool
	}{
		{SecretaryGuidanceUser, "", true}, {SecretaryGuidanceSession, "1", true},
		{SecretaryGuidanceUser, "1", false}, {SecretaryGuidanceSession, "", false},
		{"", "", false}, {"global", "", false}, {SecretaryGuidanceSession, "0", false},
		{SecretaryGuidanceSession, "-1", false}, {SecretaryGuidanceSession, "01", false},
		{SecretaryGuidanceSession, "+1", false}, {SecretaryGuidanceSession, "9223372036854775808", false},
	} {
		t.Run(tc.scope+"_"+tc.source, func(t *testing.T) {
			err := ValidateSecretaryGuidanceScope(tc.scope, tc.source)
			if tc.valid {
				require.NoError(t, err)
			} else {
				assert.ErrorIs(t, err, ErrConflict)
			}
		})
	}
}

func TestSecretaryGuidanceGivenInstructionWhenNormalizedThenTextIsBoundedAndBlankClears(
	t *testing.T,
) {
	for _, tc := range []struct {
		name, input, want string
		invalid           bool
	}{
		{"trim", "  Keep decisions.\n", "Keep decisions.", false},
		{"clear", " \n\t", "", false},
		{"rune_limit", strings.Repeat("x", SecretaryInstructionMaxRunes), strings.Repeat("x", SecretaryInstructionMaxRunes), false},
		{"too_many_runes", strings.Repeat("x", SecretaryInstructionMaxRunes+1), "", true},
		{"too_many_bytes", strings.Repeat("\u4e2d", SecretaryInstructionMaxBytes/3+1), "", true},
		{"invalid_utf8", string([]byte{0xff}), "", true}, {"nul", "a\x00b", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizeSecretaryInstruction(tc.input)
			if tc.invalid {
				assert.ErrorIs(t, err, ErrConflict)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tc.want, got)
			}
		})
	}
}

func TestSecretaryGuidanceGivenFrozenSnapshotWhenValidatedThenEachLayerRemainsOwnedAndBounded(
	t *testing.T,
) {
	valid := SecretaryInstructionSnapshot{
		User: SecretaryGuidance{OwnerUserID: "owner", Scope: SecretaryGuidanceUser},
		Session: SecretaryGuidance{
			OwnerUserID: "owner",
			Scope:       SecretaryGuidanceSession,
			SourceID:    "1",
			Revision:    2,
			Instruction: "Keep important outcomes.",
		},
		Run: "Condense repetitive steps.",
	}
	require.NoError(t, valid.Validate("owner", "1"))
	for name, mutate := range map[string]func(*SecretaryInstructionSnapshot){
		"foreign_user":            func(s *SecretaryInstructionSnapshot) { s.User.OwnerUserID = "other" },
		"foreign_session":         func(s *SecretaryInstructionSnapshot) { s.Session.SourceID = "2" },
		"wrong_scope":             func(s *SecretaryInstructionSnapshot) { s.User.Scope = SecretaryGuidanceSession },
		"negative_revision":       func(s *SecretaryInstructionSnapshot) { s.Session.Revision = -1 },
		"unversioned_instruction": func(s *SecretaryInstructionSnapshot) { s.User.Instruction = "unversioned" },
		"oversized_user": func(s *SecretaryInstructionSnapshot) {
			s.User.Revision = 1
			s.User.Instruction = strings.Repeat("x", SecretaryInstructionMaxBytes+1)
		},
		"oversized_run": func(s *SecretaryInstructionSnapshot) { s.Run = strings.Repeat("x", SecretaryInstructionMaxBytes+1) },
	} {
		t.Run(name, func(t *testing.T) {
			snapshot := valid
			mutate(&snapshot)
			assert.ErrorIs(t, snapshot.Validate("owner", "1"), ErrConflict)
		})
	}
}
