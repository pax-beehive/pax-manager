package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

var _ domain.SecretaryOperationStore = (*PostgresStore)(nil)

func (s *PostgresStore) PrepareSecretaryOperation(ctx context.Context, owner string,
	input domain.SecretaryOperationPrepare,
) (domain.SecretaryOperation, error) {
	if owner == "" {
		return domain.SecretaryOperation{}, ErrUnauthorized
	}
	if err := validateSecretaryOperationPrepare(input); err != nil {
		return domain.SecretaryOperation{}, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead})
	if err != nil {
		return domain.SecretaryOperation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	job, err := lockSecretaryOperationSourceTx(ctx, tx, owner, input.SourceID)
	if err != nil {
		return domain.SecretaryOperation{}, err
	}
	operation := domain.SecretaryOperation{
		SourceID:     input.SourceID,
		Kind:         input.Kind,
		Status:       "draft",
		Revision:     1,
		Targets:      []domain.SecretaryOperationTarget{},
		BaselineRefs: []domain.SummaryRef{},
		ResultRefs:   []domain.SummaryRef{},
		CreatedAt:    s.now().UTC(),
		UpdatedAt:    s.now().UTC(),
	}
	operation.Instructions, err = loadSecretaryInstructionsTx(ctx, tx, owner, input.SourceID)
	if err != nil {
		return domain.SecretaryOperation{}, err
	}
	operation.BaselineRefs, err = listSecretaryNodeRefs(
		ctx,
		tx,
		owner,
		input.SourceID,
		"",
		domain.SecretaryOperationTargetLimit+1,
	)
	if err != nil {
		return domain.SecretaryOperation{}, err
	}
	if len(operation.BaselineRefs) > domain.SecretaryOperationTargetLimit {
		return domain.SecretaryOperation{}, ErrConflict
	}
	if input.Kind == "human_edit" {
		summary, readErr := getSummaryTx(ctx, tx, owner, input.SummaryID, 0)
		if readErr != nil {
			return domain.SecretaryOperation{}, readErr
		}
		if summaryOriginSource(summary) != input.SourceID {
			return domain.SecretaryOperation{}, ErrNotFound
		}
		operation.Summary = &summary
	} else if operation.Targets, err = prepareSecretaryOperationTargetsTx(ctx, tx, job, input); err != nil {
		return domain.SecretaryOperation{}, err
	}
	operation.OperationID, err = newSecret("operation")
	if err != nil {
		return domain.SecretaryOperation{}, err
	}
	raw, _ := json.Marshal(operation)
	_, err = tx.ExecContext(ctx, `INSERT INTO secretary_operations
 (operation_id,source_id,owner_user_id,kind,status,revision,operation_json,created_at,updated_at)
 VALUES($1,$2,$3,$4,'draft',1,$5::jsonb,$6,$6)`, operation.OperationID,
		operation.SourceID, owner, operation.Kind, string(raw), operation.CreatedAt)
	if err != nil {
		return domain.SecretaryOperation{}, err
	}
	return operation, tx.Commit()
}

func lockSecretaryOperationSourceTx(
	ctx context.Context,
	tx *sql.Tx,
	owner, source string,
) (domain.SecretaryJob, error) {
	group, err := readSessionSearchGroupTx(ctx, tx, owner, source, "", "")
	if err != nil {
		return domain.SecretaryJob{}, err
	}
	// Match the publication lock order; manual completion also works without settings.
	if _, err = tx.ExecContext(ctx, `SELECT owner_user_id FROM secretary_settings WHERE owner_user_id=$1 FOR UPDATE`, owner); err != nil {
		return domain.SecretaryJob{}, err
	}
	_, err = tx.ExecContext(
		ctx,
		`INSERT INTO secretary_jobs(source_id,owner_user_id,source_agent_id,source_session_id,desired_revision)
 VALUES($1,$2,$3,$4,0) ON CONFLICT(source_id) DO NOTHING`,
		source,
		owner,
		group.AgentID,
		group.SessionID,
	)
	if err != nil {
		return domain.SecretaryJob{}, err
	}
	job := domain.SecretaryJob{SourceID: source, OwnerUserID: owner, SourceAgentID: group.AgentID,
		SourceSessionID: group.SessionID}
	var storedOwner string
	if err = tx.QueryRowContext(ctx, `SELECT owner_user_id,desired_revision,retry_epoch FROM secretary_jobs WHERE source_id=$1 FOR UPDATE`, source).
		Scan(&storedOwner, &job.ThroughRevision, &job.RetryEpoch); err != nil {
		return domain.SecretaryJob{}, err
	}
	if storedOwner != owner {
		return domain.SecretaryJob{}, ErrNotFound
	}
	_, err = tx.ExecContext(ctx, `WITH missing AS (
 SELECT m.message_id,row_number() OVER(ORDER BY m.session_seq,m.message_id) position FROM messages m
 WHERE m.agent_id=$2 AND m.session_id=$3 AND NOT EXISTS(SELECT 1 FROM secretary_message_state state
 WHERE state.source_id=$1 AND state.message_id=m.message_id)
), inserted AS (
 INSERT INTO secretary_message_state(source_id,message_id,change_revision)
 SELECT $1,message_id,$4::bigint+position FROM missing RETURNING change_revision
) UPDATE secretary_jobs SET desired_revision=GREATEST(desired_revision,
 COALESCE((SELECT max(change_revision) FROM inserted),desired_revision)) WHERE source_id=$1`,
		source, group.AgentID, group.SessionID, job.ThroughRevision)
	return job, err
}

func prepareSecretaryOperationTargetsTx(ctx context.Context, tx *sql.Tx, job domain.SecretaryJob,
	input domain.SecretaryOperationPrepare,
) ([]domain.SecretaryOperationTarget, error) {
	rows, err := tx.QueryContext(
		ctx,
		`SELECT state.message_id,state.change_revision FROM secretary_message_state state
 JOIN secretary_jobs j USING(source_id) JOIN messages m ON m.message_id=state.message_id
 WHERE state.source_id=$1 AND m.agent_id=$2 AND m.session_id=$3
 AND m.role IN ('user','assistant') AND COALESCE(m.message_type,'') IN (`+secretaryPendingKindsSQL()+`)
 AND ($4<>'pending' OR (state.change_revision>j.processed_revision
 AND (state.processed_owner_user_id<>$6 OR state.processed_change_revision<state.change_revision)))
 AND ($4<>'selected' OR state.message_id=ANY($5))
 ORDER BY state.change_revision,state.message_id LIMIT 4097`,
		job.SourceID,
		job.SourceAgentID,
		job.SourceSessionID,
		input.Scope,
		input.MessageIDs,
		job.OwnerUserID,
	)
	if err != nil {
		return nil, err
	}
	var changes []secretaryChange
	for rows.Next() {
		var change secretaryChange
		if err = rows.Scan(&change.id, &change.revision); err != nil {
			_ = rows.Close()
			return nil, err
		}
		changes = append(changes, change)
	}
	err = errors.Join(rows.Err(), rows.Close())
	if err != nil {
		return nil, err
	}
	if len(changes) > domain.SecretaryOperationTargetLimit {
		return nil, ErrConflict
	}
	var targets []domain.SecretaryOperationTarget
	var docs []domain.SearchDocument
	seen := map[string]bool{}
	for _, change := range changes {
		doc, readErr := loadSearchMessage(ctx, tx, change.id)
		if errors.Is(readErr, ErrNotFound) && input.Scope != "selected" {
			continue
		}
		if readErr != nil {
			return nil, readErr
		}
		if doc.OwnerUserID != job.OwnerUserID || doc.AgentID != job.SourceAgentID ||
			doc.SessionID != job.SourceSessionID {
			return nil, ErrNotFound
		}
		targets = append(targets, domain.SecretaryOperationTarget{MessageID: change.id,
			ContentHash: doc.ContentHash, CanonicalizationVersion: doc.CanonicalizationVersion,
			ChangeRevision: change.revision, Role: doc.Role, Preview: secretaryOperationPreview(doc)})
		docs, seen[change.id] = append(docs, domain.SearchDocument{SourceID: doc.SourceID}), true
	}
	if len(targets) == 0 {
		return nil, ErrConflict
	}
	if input.Scope == "selected" {
		for _, id := range input.MessageIDs {
			if !seen[id] {
				return nil, ErrNotFound
			}
			delete(seen, id)
		}
	}
	job.Messages = docs
	complete, err := secretarySourcesCompleteTx(ctx, tx, job)
	if err != nil {
		return nil, err
	}
	if !complete {
		return nil, domain.ErrSecretarySourceIncomplete
	}
	return targets, nil
}

func (s *PostgresStore) GetSecretaryOperation(
	ctx context.Context,
	owner, id string,
) (domain.SecretaryOperation, error) {
	if owner == "" {
		return domain.SecretaryOperation{}, ErrUnauthorized
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return domain.SecretaryOperation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	stored, err := readSecretaryOperationTx(ctx, tx, owner, id, false)
	if err != nil {
		return domain.SecretaryOperation{}, err
	}
	if _, err = readSessionSearchGroupTx(ctx, tx, owner, stored.value.SourceID, "", ""); err != nil {
		return domain.SecretaryOperation{}, err
	}
	if err = validateSecretaryOperationAccessTx(ctx, tx, owner, stored.value); err != nil {
		return domain.SecretaryOperation{}, err
	}
	return stored.value, tx.Commit()
}

func validateSecretaryOperationAccessTx(
	ctx context.Context,
	tx *sql.Tx,
	owner string,
	operation domain.SecretaryOperation,
) error {
	if len(operation.Targets) > 0 {
		ids := make([]string, 0, len(operation.Targets))
		for _, target := range operation.Targets {
			ids = append(ids, target.MessageID)
		}
		var count int
		err := tx.QueryRowContext(ctx, `SELECT count(*)`+searchMessageVisibilityFromSQL+
			` WHERE m.message_id=ANY($1) AND a.owner_user_id=$2`+
			searchMessageVisibilityConditionsSQL, ids, owner).Scan(&count)
		if err != nil {
			return err
		}
		if count != len(ids) {
			return ErrNotFound
		}
	}
	if summary := operation.Summary; summary != nil {
		_, err := getSummaryTx(ctx, tx, owner, summary.SummaryID, summary.Revision)
		return err
	}
	return nil
}

func readSecretaryOperationTx(
	ctx context.Context,
	tx *sql.Tx,
	owner, id string,
	lock bool,
) (*memorySecretaryOperation, error) {
	query := `SELECT operation_json,COALESCE(request_id,''),request_hash,completed_ids
 FROM secretary_operations WHERE operation_id=$1 AND owner_user_id=$2`
	if lock {
		query += " FOR UPDATE"
	}
	result := &memorySecretaryOperation{owner: owner, completed: map[string]bool{}}
	var raw, completed []byte
	err := tx.QueryRowContext(ctx, query, id, owner).
		Scan(&raw, &result.requestID, &result.hash, &completed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(raw, &result.value); err != nil {
		return nil, err
	}
	var ids []string
	if err = json.Unmarshal(completed, &ids); err != nil {
		return nil, err
	}
	for _, id := range ids {
		result.completed[id] = true
	}
	return result, nil
}

func writeSecretaryOperationTx(
	ctx context.Context,
	tx *sql.Tx,
	operation *memorySecretaryOperation,
) error {
	operation.value.UpdatedAt = time.Now().UTC()
	raw, _ := json.Marshal(operation.value)
	ids := make([]string, 0, len(operation.completed))
	for id := range operation.completed {
		ids = append(ids, id)
	}
	completed, _ := json.Marshal(ids)
	_, err := tx.ExecContext(ctx, `UPDATE secretary_operations SET status=$3,revision=$4,
 request_id=NULLIF($5,''),request_hash=$6,operation_json=$7::jsonb,completed_ids=$8::jsonb,updated_at=$9
 WHERE operation_id=$1 AND owner_user_id=$2`, operation.value.OperationID, operation.owner,
		operation.value.Status, operation.value.Revision, operation.requestID, operation.hash,
		string(raw), string(completed), operation.value.UpdatedAt)
	return err
}
