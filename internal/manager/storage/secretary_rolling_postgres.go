package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func loadSecretaryRollingBaseline(ctx context.Context, tx *sql.Tx, job *domain.SecretaryJob) error {
	job.TargetSummaryID = secretarySummaryID(job.OwnerUserID, job.SourceID)
	var roots []byte
	var processed, reset int64
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(NULLIF(s.custom_session_name,''),s.session_name,''),
 COALESCE(s.primary_project_id,''),COALESCE(s.workspace_roots,'[]'::jsonb),
 COALESCE(s.metadata #>> '{pax_config,cwd}',s.metadata->>'initial_cwd',''),
 j.processed_revision,j.reset_revision FROM agent_sessions s JOIN secretary_jobs j ON j.source_id=s.id
 WHERE s.id=$1`, job.SourceID).
		Scan(&job.SourceMetadata.Title, &job.SourceMetadata.PrimaryProjectID,
			&roots, &job.SourceMetadata.CWD, &processed, &reset)
	if err != nil {
		return err
	}
	if err = json.Unmarshal(roots, &job.SourceMetadata.WorkspaceRoots); err != nil {
		return err
	}
	invalidated, err := loadSecretaryInvalidationsTx(ctx, tx, job)
	if err != nil {
		return err
	}
	refs, err := listSecretaryNodeRefs(ctx, tx, job.OwnerUserID, job.SourceID, "", 101)
	if err != nil {
		return err
	}
	size := 0
	for index, ref := range refs {
		if ref.SummaryID == job.TargetSummaryID {
			job.ExpectedRevision = ref.Revision
		}
		if index == 100 {
			job.SummariesHasMore = true
			job.SummariesCursor = summaryNextCursor(
				job.OwnerUserID+":"+job.SourceID,
				refs[index-1].SummaryID,
			)
			break
		}
		if invalidated[ref.SummaryID] {
			continue
		}
		current, readErr := getSummaryTx(ctx, tx, job.OwnerUserID, ref.SummaryID, ref.Revision)
		if errors.Is(readErr, ErrNotFound) {
			job.InvalidatedSummaries = append(job.InvalidatedSummaries, ref)
			job.ResetRequired, job.ResetReason = true, "source_visibility_changed"
			continue
		}
		if readErr != nil {
			return readErr
		}
		if current.NodeStatus == "stale" {
			continue
		}
		raw, _ := json.Marshal(current)
		if size+len(raw) > secretaryNodeStateBytes {
			job.SummariesHasMore = true
			if index > 0 {
				job.SummariesCursor = summaryNextCursor(
					job.OwnerUserID+":"+job.SourceID,
					refs[index-1].SummaryID,
				)
			}
			break
		}
		size += len(raw)
		job.CurrentSummaries = append(job.CurrentSummaries, current)
		if current.SummaryID == job.TargetSummaryID {
			job.ExpectedRevision, job.CurrentSummary = current.Revision, &current
		}
	}
	return nil
}
