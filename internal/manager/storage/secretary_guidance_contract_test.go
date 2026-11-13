package storage

import (
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSecretaryGuidanceGivenIndependentScopesWhenSavedThenCASAndClearDoNotChangeSettingsOrTasks(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		store := f.store.(domain.SecretaryGuidanceStore)
		work := secretaryClaim(t, f.store)
		for _, scope := range []string{domain.SecretaryGuidanceUser, domain.SecretaryGuidanceSession} {
			source := ""
			if scope == domain.SecretaryGuidanceSession {
				source = "1"
			}
			initial, err := store.GetSecretaryGuidance(t.Context(), "owner", scope, source)
			require.NoError(t, err)
			assert.EqualValues(t, 0, initial.Revision)
			assert.Empty(t, initial.Instruction)
			updated, err := store.UpdateSecretaryGuidance(
				t.Context(),
				"owner",
				scope,
				source,
				domain.SecretaryGuidanceUpdate{Instruction: "  Keep decisions.\n"},
			)
			require.NoError(t, err)
			assert.EqualValues(t, 1, updated.Revision)
			assert.Equal(t, "Keep decisions.", updated.Instruction)
			assert.False(t, updated.UpdatedAt.IsZero())
			_, err = store.UpdateSecretaryGuidance(
				t.Context(),
				"owner",
				scope,
				source,
				domain.SecretaryGuidanceUpdate{Instruction: "stale"},
			)
			assert.ErrorIs(t, err, ErrConflict)
			cleared, err := store.UpdateSecretaryGuidance(
				t.Context(),
				"owner",
				scope,
				source,
				domain.SecretaryGuidanceUpdate{Instruction: " \n\t", ExpectedRevision: 1},
			)
			require.NoError(t, err)
			assert.EqualValues(t, 2, cleared.Revision)
			assert.Empty(t, cleared.Instruction)
			read, err := store.GetSecretaryGuidance(t.Context(), "owner", scope, source)
			require.NoError(t, err)
			assert.Equal(t, cleared, read)
		}
		settings, err := f.store.GetSecretarySettings(t.Context(), "owner")
		require.NoError(t, err)
		assert.Equal(t, f.settings, settings)
		active, err := f.store.SecretaryJobActive(t.Context(), work)
		require.NoError(t, err)
		assert.True(t, active, "saving guidance must not fence active work")
	})
}

func TestSecretaryGuidanceGivenInvalidOrForeignScopeWhenReadOrUpdatedThenNothingIsWritten(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		store := f.store.(domain.SecretaryGuidanceStore)
		for _, tc := range []struct {
			name, owner, scope, source, instruction string
			revision                                int64
			want                                    error
		}{
			{"unauthorized", "", domain.SecretaryGuidanceUser, "", "ok", 0, ErrUnauthorized},
			{"foreign", "other", domain.SecretaryGuidanceSession, "1", "ok", 0, ErrNotFound},
			{"missing", "owner", domain.SecretaryGuidanceSession, "99999", "ok", 0, ErrNotFound},
			{"scope", "owner", "global", "", "ok", 0, ErrConflict},
			{"source_on_user", "owner", domain.SecretaryGuidanceUser, "1", "ok", 0, ErrConflict},
			{"bad_source", "owner", domain.SecretaryGuidanceSession, "01", "ok", 0, ErrConflict},
			{"negative_revision", "owner", domain.SecretaryGuidanceUser, "", "ok", -1, ErrConflict},
			{"missing_revision", "owner", domain.SecretaryGuidanceUser, "", "ok", 5, ErrConflict},
			{"too_many_bytes", "owner", domain.SecretaryGuidanceUser, "", strings.Repeat("x", domain.SecretaryInstructionMaxBytes+1), 0, ErrConflict},
			{"too_many_runes", "owner", domain.SecretaryGuidanceUser, "", strings.Repeat("x", domain.SecretaryInstructionMaxRunes+1), 0, ErrConflict},
			{"invalid_utf8", "owner", domain.SecretaryGuidanceUser, "", string([]byte{0xff}), 0, ErrConflict},
			{"nul", "owner", domain.SecretaryGuidanceUser, "", "x\x00y", 0, ErrConflict},
		} {
			t.Run(tc.name, func(t *testing.T) {
				_, err := store.UpdateSecretaryGuidance(
					t.Context(),
					tc.owner,
					tc.scope,
					tc.source,
					domain.SecretaryGuidanceUpdate{
						Instruction:      tc.instruction,
						ExpectedRevision: tc.revision,
					},
				)
				assert.ErrorIs(t, err, tc.want)
				if tc.name == "unauthorized" || tc.name == "foreign" || tc.name == "missing" ||
					tc.name == "scope" ||
					tc.name == "source_on_user" ||
					tc.name == "bad_source" {
					_, err = store.GetSecretaryGuidance(t.Context(), tc.owner, tc.scope, tc.source)
					assert.ErrorIs(t, err, tc.want)
				}
			})
		}
		untouched, err := store.GetSecretaryGuidance(
			t.Context(),
			"owner",
			domain.SecretaryGuidanceUser,
			"",
		)
		require.NoError(t, err)
		assert.Zero(t, untouched.Revision)
		other, err := store.UpdateSecretaryGuidance(
			t.Context(),
			"other",
			domain.SecretaryGuidanceUser,
			"",
			domain.SecretaryGuidanceUpdate{Instruction: "other preferences"},
		)
		require.NoError(t, err)
		assert.EqualValues(t, 1, other.Revision)
		untouched, err = store.GetSecretaryGuidance(
			t.Context(),
			"owner",
			domain.SecretaryGuidanceUser,
			"",
		)
		require.NoError(t, err)
		assert.Zero(t, untouched.Revision)
	})
}

func TestSecretaryGuidanceGivenConcurrentWritersWhenRevisionMatchesThenExactlyOneWins(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		store := f.store.(domain.SecretaryGuidanceStore)
		for _, scope := range []string{domain.SecretaryGuidanceUser, domain.SecretaryGuidanceSession} {
			source := ""
			if scope == domain.SecretaryGuidanceSession {
				source = "1"
			}
			results := make(chan error, 2)
			var wg sync.WaitGroup
			for _, text := range []string{"first preference", "second preference"} {
				wg.Add(1)
				go func(instruction string) {
					defer wg.Done()
					_, err := store.UpdateSecretaryGuidance(
						t.Context(),
						"owner",
						scope,
						source,
						domain.SecretaryGuidanceUpdate{Instruction: instruction},
					)
					results <- err
				}(text)
			}
			wg.Wait()
			close(results)
			succeeded, conflicted := 0, 0
			for err := range results {
				if err == nil {
					succeeded++
				} else {
					assert.ErrorIs(t, err, ErrConflict)
					conflicted++
				}
			}
			assert.Equal(t, 1, succeeded)
			assert.Equal(t, 1, conflicted)
		}
	})
}

func TestSecretaryGuidanceGivenSessionAccessRevokedThenOldPreferencesCannotBeReadOrChanged(
	t *testing.T,
) {
	for _, scenario := range []string{"archived", "internal_source", "registered_internal", "owner_changed", "missing_agent"} {
		t.Run(scenario, func(t *testing.T) {
			secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
				store := f.store.(domain.SecretaryGuidanceStore)
				_, err := store.UpdateSecretaryGuidance(
					t.Context(),
					"owner",
					domain.SecretaryGuidanceSession,
					"1",
					domain.SecretaryGuidanceUpdate{Instruction: "Private preference"},
				)
				require.NoError(t, err)
				if f.memory != nil {
					session := f.memory.sessions[sessionKey("source", "session")]
					switch scenario {
					case "archived":
						now := f.memory.now()
						session.ArchivedAt = &now
					case "internal_source":
						session.Source = domain.SecretaryInternalSource
					case "registered_internal":
						f.memory.secretaryLocked().internal[sessionKey("source", "session")] = true
					case "owner_changed":
						agent := f.memory.agents["source"]
						agent.OwnerUserID = "other"
						f.memory.agents["source"] = agent
					case "missing_agent":
						delete(f.memory.agents, "source")
					}
					f.memory.sessions[sessionKey("source", "session")] = session
				} else {
					statement := map[string]string{
						"archived":            `UPDATE agent_sessions SET archived_at=NOW() WHERE id=1`,
						"internal_source":     `UPDATE agent_sessions SET source='secretary_internal' WHERE id=1`,
						"registered_internal": `INSERT INTO secretary_internal_sessions(agent_id,session_id,owner_user_id) VALUES('source','session','owner')`,
						"owner_changed":       `UPDATE agents SET owner_user_id='other' WHERE agent_id='source'`,
						"missing_agent":       `UPDATE agents SET deleted_at=NOW() WHERE agent_id='source'`,
					}[scenario]
					_, err = f.postgres.db.ExecContext(t.Context(), statement)
					require.NoError(t, err)
				}
				_, err = store.GetSecretaryGuidance(
					t.Context(),
					"owner",
					domain.SecretaryGuidanceSession,
					"1",
				)
				assert.ErrorIs(t, err, ErrNotFound)
				_, err = store.UpdateSecretaryGuidance(
					t.Context(),
					"owner",
					domain.SecretaryGuidanceSession,
					"1",
					domain.SecretaryGuidanceUpdate{Instruction: "replacement", ExpectedRevision: 1},
				)
				assert.ErrorIs(t, err, ErrNotFound)
				if scenario == "owner_changed" {
					fresh, readErr := store.GetSecretaryGuidance(
						t.Context(),
						"other",
						domain.SecretaryGuidanceSession,
						"1",
					)
					require.NoError(t, readErr)
					assert.Zero(t, fresh.Revision)
					assert.Empty(t, fresh.Instruction)
				}
			})
		})
	}
}

func TestSecretaryGuidanceGivenNewBatchWhenDefaultsChangeThenFrozenInstructionsAndLegacyIdentityStayStable(
	t *testing.T,
) {
	secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
		store := f.store.(domain.SecretaryGuidanceStore)
		_, err := store.UpdateSecretaryGuidance(
			t.Context(),
			"owner",
			domain.SecretaryGuidanceUser,
			"",
			domain.SecretaryGuidanceUpdate{Instruction: "Keep decisions."},
		)
		require.NoError(t, err)
		_, err = store.UpdateSecretaryGuidance(
			t.Context(),
			"owner",
			domain.SecretaryGuidanceSession,
			"1",
			domain.SecretaryGuidanceUpdate{Instruction: "Condense repeated procedures."},
		)
		require.NoError(t, err)
		job := f.prepare(t)
		require.NotNil(t, job.Instructions)
		assert.Equal(t, "Keep decisions.", job.Instructions.User.Instruction)
		assert.Equal(t, "Condense repeated procedures.", job.Instructions.Session.Instruction)
		raw := secretaryBatchInput(job)
		batchID := secretaryBatchID(job)
		runID := secretaryRunID(job)
		_, err = store.UpdateSecretaryGuidance(
			t.Context(),
			"owner",
			domain.SecretaryGuidanceUser,
			"",
			domain.SecretaryGuidanceUpdate{Instruction: "New default.", ExpectedRevision: 1},
		)
		require.NoError(t, err)
		current := job
		current.Instructions = &domain.SecretaryInstructionSnapshot{
			Run: "do not replace the old snapshot",
		}
		restored, err := secretaryRestoreBatch(raw, current)
		require.NoError(t, err)
		assert.Equal(t, job.Instructions, restored.Instructions)
		assert.Equal(t, batchID, secretaryBatchID(restored))
		assert.Equal(t, runID, secretaryRunID(restored))
		legacy := job
		legacy.Instructions = nil
		legacyRaw := secretaryBatchInput(legacy)
		assert.NotContains(t, string(legacyRaw), "Instructions")
		legacyRestored, err := secretaryRestoreBatch(legacyRaw, current)
		require.NoError(t, err)
		assert.Nil(t, legacyRestored.Instructions)
		assert.Equal(t, secretaryBatchID(legacy), secretaryBatchID(legacyRestored))
		changed := job
		snapshot := *job.Instructions
		snapshot.Run = "Retain only unresolved work."
		changed.Instructions = &snapshot
		assert.NotEqual(t, batchID, secretaryBatchID(changed))
		assert.NotEqual(t, runID, secretaryRunID(changed))
	})
}
