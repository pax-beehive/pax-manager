package storage

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func TestSecretaryFrozenHeadsGivenMissingOrInvalidBaselineThenCannotPublish(t *testing.T) {
	for _, mode := range []string{"missing-batch", "invalid-directory", "foreign-node"} {
		t.Run(mode, func(t *testing.T) {
			secretaryLifecycleFixtures(t, func(t *testing.T, f *secretaryLifecycleFixture) {
				job, baseline := frozenHeadRecoveryFixture(t, f)
				switch mode {
				case "missing-batch":
					job.BatchID = "missing-batch"
				case "invalid-directory":
					if f.memory != nil {
						f.memory.secretaryExecutionsLocked()[job.BatchID].input = []byte(
							`{"CurrentSummaries":[{}]}`,
						)
					} else {
						_, err := f.postgres.db.ExecContext(t.Context(), `UPDATE secretary_batches SET input_json=$2::jsonb WHERE batch_id=$1`, job.BatchID, `{"CurrentSummaries":[{}]}`)
						require.NoError(t, err)
					}
				case "foreign-node":
					if f.memory != nil {
						node := f.memory.secretaryLocked().nodes[baseline.SummaryID]
						node.OwnerUserID = "other"
						f.memory.secretaryLocked().nodes[baseline.SummaryID] = node
					} else {
						_, err := f.postgres.db.ExecContext(t.Context(), `UPDATE knowledge_nodes SET owner_user_id='other' WHERE id=$1`, baseline.SummaryID)
						require.NoError(t, err)
					}
				}
				_, err := f.store.PublishSecretaryBatch(
					t.Context(),
					job,
					secretaryNodesPublication(job, "new-node"),
				)
				if mode == "foreign-node" {
					assert.ErrorIs(t, err, domain.ErrNotFound)
				} else {
					assert.ErrorIs(t, err, domain.ErrSecretaryFenced)
				}
			})
		})
	}
}

func TestSecretaryFrozenHeadsGivenMalformedOrAmbiguousDirectoryThenRejectsAndSortsOnlyExactRefs(
	t *testing.T,
) {
	_, err := secretaryFrozenSummaryHeads([]byte("invalid"), "owner")
	assert.ErrorIs(t, err, domain.ErrSecretaryFenced)
	base := domain.SummaryVersion{OwnerUserID: "owner", SummaryID: "b", Revision: 1}
	for _, mode := range []string{"foreign", "blank", "zero", "ambiguous", "valid"} {
		versions := []domain.SummaryVersion{base, base}
		switch mode {
		case "foreign":
			versions[0].OwnerUserID = "other"
		case "blank":
			versions[0].SummaryID = ""
		case "zero":
			versions[0].Revision = 0
		case "ambiguous":
			versions[0].Revision = 2
		case "valid":
			versions = append(
				versions,
				domain.SummaryVersion{OwnerUserID: "owner", SummaryID: "a", Revision: 2},
			)
		}
		raw, err := json.Marshal(struct{ CurrentSummaries []domain.SummaryVersion }{versions})
		require.NoError(t, err)
		heads, err := secretaryFrozenSummaryHeads(raw, "owner")
		switch mode {
		case "valid":
			require.NoError(t, err)
			assert.Equal(
				t,
				[]domain.SummaryRef{{SummaryID: "a", Revision: 2}, {SummaryID: "b", Revision: 1}},
				heads,
			)
		case "ambiguous":
			assert.ErrorIs(t, err, domain.ErrSummaryVersionMismatch)
		default:
			assert.ErrorIs(t, err, domain.ErrSecretaryFenced)
		}
	}
}
