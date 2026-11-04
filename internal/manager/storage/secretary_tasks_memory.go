package storage

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

var _ domain.SecretaryTaskStore = (*MemoryStore)(nil)

func (s *MemoryStore) secretaryTaskSourceLocked(
	owner, id string,
) (*memorySecretaryJob, domain.AgentSession, error) {
	job := s.secretaryLocked().jobs[id]
	if job == nil || job.work.OwnerUserID != owner ||
		!s.secretaryAgentOwnedLocked(owner, job.work.SourceAgentID) {
		return nil, domain.AgentSession{}, ErrNotFound
	}
	session, ok := s.sessions[sessionKey(job.work.SourceAgentID, job.work.SourceSessionID)]
	if !ok || session.ArchivedAt != nil || session.Source == domain.SecretaryInternalSource ||
		s.secretaryLocked().internal[sessionKey(session.AgentID, session.SessionID)] || !s.searchNodesVisibleLocked(session.NodeID) {
		return nil, domain.AgentSession{}, ErrNotFound
	}
	return job, session, nil
}

func (s *MemoryStore) secretaryTaskBatchLocked(
	job *memorySecretaryJob,
	settings int64,
) (string, *memorySecretaryExecutionBatch) {
	var selected *memorySecretaryExecutionBatch
	var selectedID string
	var latest time.Time
	for id, batch := range s.secretaryExecutionsLocked() {
		if batch.source != job.work.SourceID || batch.owner != job.work.OwnerUserID ||
			batch.settings != settings || batch.epoch != job.work.RetryEpoch || batch.state == "superseded" {
			continue
		}
		updated := time.Time{}
		if len(batch.attempts) > 0 {
			updated = batch.attempts[len(batch.attempts)-1].UpdatedAt
		}
		if selected != nil &&
			(secretaryTaskBatchPriority(batch.state) < secretaryTaskBatchPriority(selected.state) ||
				secretaryTaskBatchPriority(batch.state) == secretaryTaskBatchPriority(selected.state) &&
					(updated.Before(latest) || updated.Equal(latest) && id < selectedID)) {
			continue
		}
		selected, selectedID, latest = batch, id, updated
	}
	return selectedID, selected
}

func secretaryTaskBatchPriority(state string) int {
	if secretaryBatchOpen(state) {
		return 2
	}
	if state == "blocked" {
		return 1
	}
	return 0
}

func (s *MemoryStore) memorySecretaryTaskLocked(owner, id string) (secretaryTaskRecord, error) {
	job, session, err := s.secretaryTaskSourceLocked(owner, id)
	if err != nil {
		return secretaryTaskRecord{}, err
	}
	cfg := s.secretaryLocked().settings[owner]
	record := secretaryTaskRecord{
		desired:       job.desired,
		processed:     job.processed,
		available:     job.available,
		enabled:       cfg.Enabled,
		blocked:       job.executionBlocked,
		sourceRuntime: session.RuntimeStatus,
	}
	record.task = domain.SecretaryTask{
		TaskID:        id,
		SessionID:     session.SessionID,
		SourceAgentID: session.AgentID,
		Title: secretaryTaskSessionTitle(
			session,
		),
		SecretaryAgentID: cfg.AgentID,
		Revision:         job.controlRevision,
		SettingsRevision: cfg.Revision,
		RetryEpoch:       job.work.RetryEpoch,
		Paused:           job.paused,
		LeaseActive:      job.work.LeaseToken != "" && job.leaseUntil.After(s.now()),
	}
	updated := job.updated
	record.task.UpdatedAt = &updated
	if !session.CreatedAt.IsZero() {
		created := session.CreatedAt
		record.task.CreatedAt = &created
	}
	id, batch := s.secretaryTaskBatchLocked(job, cfg.Revision)
	if batch == nil {
		record.failure = job.lastError
		return record, nil
	}
	record.task.BatchID, record.batchState = id, batch.state
	record.task.AttemptCount, record.task.ConnectionFailures = len(
		batch.attempts,
	), batch.connectionFailures
	record.retryAfter = batch.retryAfter
	if len(batch.attempts) > 0 {
		attempt := batch.attempts[len(batch.attempts)-1]
		if attempt.UpdatedAt.After(updated) {
			updated = attempt.UpdatedAt
		}
		record.task.AttemptID, record.attemptState = attempt.ID, attempt.State
		record.failure, record.notDispatched = attempt.Error, attempt.NotDispatched
		record.task.ExecutionSessionID, record.task.ExecutionAgentID, record.task.ExecutionStatus =
			s.memorySecretaryExecutionLinkLocked(owner, cfg.AgentID, attempt.ID)
	}
	return record, nil
}

func (s *MemoryStore) memorySecretaryExecutionLinkLocked(
	owner, agent, attempt string,
) (string, string, string) {
	if !s.secretaryAgentOwnedLocked(owner, agent) {
		return "", "", ""
	}
	id := secretaryExecutionSessionID(owner, attempt)
	session, ok := s.sessions[sessionKey(agent, id)]
	if !ok || session.ArchivedAt != nil || !s.searchNodesVisibleLocked(session.NodeID) {
		return "", "", ""
	}
	return id, agent, session.RuntimeStatus
}

func (s *MemoryStore) ListSecretaryTasks(
	ctx context.Context,
	owner string,
	query domain.SecretaryTaskQuery,
) (domain.SecretaryTaskPage, error) {
	after, limit, err := secretaryTaskAfter(owner, query)
	if err != nil {
		return domain.SecretaryTaskPage{}, err
	}
	if err = ctx.Err(); err != nil {
		return domain.SecretaryTaskPage{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []int64
	for id, job := range s.secretaryLocked().jobs {
		numeric, _ := strconv.ParseInt(id, 10, 64)
		if numeric > after && job.work.OwnerUserID == owner && job.desired > job.processed {
			ids = append(ids, numeric)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	page := domain.SecretaryTaskPage{Items: []domain.SecretaryTask{}}
	for _, id := range ids {
		record, readErr := s.memorySecretaryTaskLocked(owner, strconv.FormatInt(id, 10))
		if readErr != nil {
			continue
		}
		if len(page.Items) == limit {
			page.NextCursor = secretaryTaskNextCursor(owner, "list", page.Items[limit-1].TaskID)
			break
		}
		page.Items = append(page.Items, secretaryTaskView(record, s.now()))
	}
	return page, nil
}

func (s *MemoryStore) GetSecretaryTask(
	ctx context.Context,
	owner, id string,
	query domain.SecretaryTaskQuery,
) (domain.SecretaryTaskDetail, error) {
	after, afterID, limit, err := secretaryTaskAttemptAfter(owner, id, query)
	if err != nil {
		return domain.SecretaryTaskDetail{}, err
	}
	if _, err = secretaryTaskID(id); err != nil {
		return domain.SecretaryTaskDetail{}, err
	}
	if err = ctx.Err(); err != nil {
		return domain.SecretaryTaskDetail{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.memorySecretaryTaskLocked(owner, id)
	if err != nil {
		return domain.SecretaryTaskDetail{}, err
	}
	detail := domain.SecretaryTaskDetail{
		Task:     secretaryTaskView(record, s.now()),
		Attempts: []domain.SecretaryTaskAttempt{},
	}
	for batchID, batch := range s.secretaryExecutionsLocked() {
		if batch.owner != owner || batch.source != id {
			continue
		}
		var input domain.SecretaryJob
		_ = json.Unmarshal(batch.input, &input)
		for _, attempt := range batch.attempts {
			if !after.IsZero() &&
				(attempt.CreatedAt.After(after) || attempt.CreatedAt.Equal(after) && attempt.ID >= afterID) {
				continue
			}
			item := domain.SecretaryTaskAttempt{
				AttemptID:     attempt.ID,
				BatchID:       batchID,
				State:         attempt.State,
				NotDispatched: attempt.NotDispatched,
				CreatedAt:     attempt.CreatedAt,
				UpdatedAt:     attempt.UpdatedAt,
				HasOutput:     attempt.Output != "",
			}
			item.ErrorCode, item.ErrorMessage = secretaryTaskFailure(
				batch.state,
				attempt.State,
				attempt.Error,
				attempt.NotDispatched,
			)
			item.ExecutionSessionID, item.ExecutionAgentID, item.ExecutionStatus = s.memorySecretaryExecutionLinkLocked(
				owner,
				input.AgentID,
				attempt.ID,
			)
			detail.Attempts = append(detail.Attempts, item)
		}
	}
	sort.Slice(detail.Attempts, func(i, j int) bool {
		if detail.Attempts[i].CreatedAt.Equal(detail.Attempts[j].CreatedAt) {
			return detail.Attempts[i].AttemptID > detail.Attempts[j].AttemptID
		}
		return detail.Attempts[i].CreatedAt.After(detail.Attempts[j].CreatedAt)
	})
	if len(detail.Attempts) > limit {
		detail.NextCursor = secretaryTaskAttemptCursor(owner, id, detail.Attempts[limit-1])
		detail.Attempts = detail.Attempts[:limit]
	}
	return detail, nil
}

func (s *MemoryStore) ControlSecretaryTask(
	ctx context.Context,
	owner, id string,
	action domain.SecretaryTaskAction,
) (domain.SecretaryTask, error) {
	if err := ctx.Err(); err != nil {
		return domain.SecretaryTask{}, err
	}
	if owner == "" {
		return domain.SecretaryTask{}, ErrUnauthorized
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.memorySecretaryTaskLocked(owner, id)
	if err != nil {
		return domain.SecretaryTask{}, err
	}
	if err = validateSecretaryTaskAction(secretaryTaskView(record, s.now()), action); err != nil {
		return domain.SecretaryTask{}, err
	}
	job := s.secretaryLocked().jobs[id]
	switch action.Action {
	case "pause", "resume":
		job.paused = action.Action == "pause"
	case "retry":
		if !record.enabled {
			return domain.SecretaryTask{}, ErrConflict
		}
		s.retryMemorySecretaryTaskLocked(job)
	default:
		return domain.SecretaryTask{}, ErrConflict
	}
	job.controlRevision++
	record, err = s.memorySecretaryTaskLocked(owner, id)
	return secretaryTaskView(record, s.now()), err
}

func (s *MemoryStore) retryMemorySecretaryTaskLocked(job *memorySecretaryJob) {
	job.work.RetryEpoch++
	job.work.RetryGeneration++
	job.work.ValidationFeedback, job.lastError, job.autoRetryKey = "", "", ""
	job.autoRetryCount = 0
	job.paused, job.executionBlocked = false, false
	job.work.LeaseToken, job.leaseUntil = "", time.Time{}
	job.available, job.updated = s.now(), s.now().Add(-30*time.Second)
}

func (s *MemoryStore) ClaimSecretaryTaskRecovery(
	ctx context.Context,
	owner, id string,
	action domain.SecretaryTaskAction,
	lease time.Duration,
) (domain.SecretaryJob, error) {
	if err := ctx.Err(); err != nil {
		return domain.SecretaryJob{}, err
	}
	if owner == "" {
		return domain.SecretaryJob{}, ErrUnauthorized
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, err := s.memorySecretaryTaskLocked(owner, id)
	if err != nil {
		return domain.SecretaryJob{}, err
	}
	if err = validateSecretaryRecoveryAction(secretaryTaskView(record, s.now()), action, record.enabled); err != nil {
		return domain.SecretaryJob{}, err
	}
	if !s.secretaryAgentOwnedLocked(owner, record.task.SecretaryAgentID) {
		return domain.SecretaryJob{}, ErrConflict
	}
	queued := s.secretaryLocked().jobs[id]
	batch := s.secretaryExecutionsLocked()[record.task.BatchID]
	current := queued.work
	current.BatchID, current.AgentID, current.SettingsRevision = record.task.BatchID, record.task.SecretaryAgentID, record.task.SettingsRevision
	current.ThroughRevision = queued.desired
	job, err := secretaryRestoreBatch(batch.input, current)
	if err == nil {
		err = validateSecretaryBatchIdentity(job, current)
	}
	if err == nil {
		err = s.validateSecretaryRecoveryInputLocked(job)
	}
	if err != nil {
		return domain.SecretaryJob{}, ErrConflict
	}
	job.LeaseToken, err = newSecret("secretary_recheck")
	if err != nil {
		return domain.SecretaryJob{}, err
	}
	attempt := batch.attempts[len(batch.attempts)-1]
	job.Attempt, job.AttemptID, job.RunID = &attempt, attempt.ID, attempt.ID
	batch.lease, queued.work.LeaseToken = job.LeaseToken, job.LeaseToken
	queued.leaseUntil, queued.executionBlocked = s.now().Add(secretaryRecoveryLease(lease)), true
	queued.controlRevision++
	return job, nil
}

func validateSecretaryRecoveryAction(
	task domain.SecretaryTask,
	action domain.SecretaryTaskAction,
	enabled bool,
) error {
	if action.Action != "recheck" || !enabled || task.AttemptID == "" ||
		(task.ExecutionState != "unknown" && task.ExecutionState != "needs_recovery" && task.ExecutionState != "output_received") {
		return ErrConflict
	}
	return validateSecretaryTaskAction(task, action)
}

func (s *MemoryStore) validateSecretaryRecoveryInputLocked(job domain.SecretaryJob) error {
	if err := s.validateSecretaryBatchInputLocked(job); err != nil {
		return err
	}
	for _, message := range job.Messages {
		current, err := s.readSearchMessageLocked(job.OwnerUserID, message.SourceID)
		if err != nil || current.ContentHash != message.ContentHash ||
			current.CanonicalizationVersion != message.CanonicalizationVersion {
			return ErrConflict
		}
	}
	for _, summary := range job.CurrentSummaries {
		current, err := s.getMemorySummaryLocked(
			job.OwnerUserID,
			summary.SummaryID,
			0,
			map[domain.SummaryRef]bool{},
		)
		if err != nil || current.Revision != summary.Revision {
			return ErrConflict
		}
	}
	return nil
}

func (s *MemoryStore) ReleaseSecretaryTaskRecovery(
	ctx context.Context,
	job domain.SecretaryJob,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	queued := s.secretaryLocked().jobs[job.SourceID]
	batch := s.secretaryExecutionsLocked()[job.BatchID]
	if queued == nil || batch == nil || job.LeaseToken == "" || batch.owner != job.OwnerUserID ||
		queued.work.OwnerUserID != job.OwnerUserID || batch.source != job.SourceID {
		return domain.ErrSecretaryFenced
	}
	if queued.work.LeaseToken != job.LeaseToken || batch.lease != job.LeaseToken {
		if batch.state == "committed" {
			return nil
		}
		return domain.ErrSecretaryFenced
	}
	queued.work.LeaseToken, queued.leaseUntil = "", time.Time{}
	queued.executionBlocked = batch.state != "committed"
	return nil
}
