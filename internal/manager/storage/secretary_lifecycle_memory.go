package storage

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type memorySecretaryExecutionBatch struct {
	source             string
	owner              string
	settings           int64
	epoch              int64
	input              []byte
	state              string
	lease              string
	attempts           []domain.SecretaryAttempt
	requestHash        string
	results            []domain.SummaryVersion
	connectionFailures int
	retryAfter         time.Time
}

func (s *MemoryStore) secretaryExecutionsLocked() map[string]*memorySecretaryExecutionBatch {
	state := s.secretaryLocked()
	if state.executions == nil {
		state.executions = make(map[string]*memorySecretaryExecutionBatch)
	}
	return state.executions
}

func (s *MemoryStore) PrepareSecretaryBatch(
	ctx context.Context,
	job domain.SecretaryJob,
) (domain.SecretaryJob, error) {
	if err := ctx.Err(); err != nil {
		return job, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.secretaryJobActiveLocked(job) {
		return job, domain.ErrSecretaryFenced
	}
	job.BatchID = secretaryBatchID(job)
	batches := s.secretaryExecutionsLocked()
	for id, pending := range batches {
		if pending.source == job.SourceID && pending.owner == job.OwnerUserID &&
			pending.settings == job.SettingsRevision &&
			pending.epoch == job.RetryEpoch &&
			(secretaryBatchOpen(pending.state) || secretaryConnectionExhausted(pending.state, pending.connectionFailures)) {
			job.BatchID = id
			break
		}
	}
	batch := batches[job.BatchID]
	if batch == nil {
		if !s.secretarySourcesCompleteLocked(job) {
			queued := s.secretaryLocked().jobs[job.SourceID]
			queued.work.LeaseToken = ""
			queued.leaseUntil = s.now()
			queued.available = s.now().Add(30 * time.Second)
			return job, domain.ErrSecretarySourceIncomplete
		}
		batch = &memorySecretaryExecutionBatch{
			input:    secretaryBatchInput(job),
			state:    "pending",
			source:   job.SourceID,
			owner:    job.OwnerUserID,
			settings: job.SettingsRevision,
			epoch:    job.RetryEpoch,
		}
		batch.connectionFailures, batch.retryAfter = s.inheritSecretaryConnectionLocked(job)
		batches[job.BatchID] = batch
	}
	if err := s.gateSecretaryConnectionLocked(job, batch); err != nil {
		return job, err
	}
	if batch.state == "blocked" || batch.state == "superseded" {
		s.secretaryLocked().jobs[job.SourceID].executionBlocked = true
		return job, domain.ErrSecretaryExecutionBlocked
	}
	restored, err := secretaryRestoreBatch(batch.input, job)
	if err == nil {
		err = validateSecretaryBatchIdentity(restored, job)
	}
	if err == nil {
		err = s.validateSecretaryBatchInputLocked(restored)
	}
	if err != nil {
		return domain.SecretaryJob{}, s.rejectSecretaryBatchInputLocked(job, batch, err)
	}
	batch.lease = job.LeaseToken
	if len(batch.attempts) > 0 {
		attempt := batch.attempts[len(batch.attempts)-1]
		restored.Attempt = &attempt
		restored.AttemptID = attempt.ID
		restored.RunID = attempt.ID
	}
	return restored, nil
}

func (s *MemoryStore) BeginSecretaryAttempt(
	ctx context.Context,
	job domain.SecretaryJob,
) (domain.SecretaryAttempt, error) {
	if err := ctx.Err(); err != nil {
		return domain.SecretaryAttempt{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	batch, err := s.activeSecretaryBatchLocked(job)
	if err != nil {
		return domain.SecretaryAttempt{}, err
	}
	if err = s.gateSecretaryConnectionLocked(job, batch); err != nil {
		return domain.SecretaryAttempt{}, err
	}
	if batch.state != "pending" {
		if batch.state == "running" {
			batch.state = "unknown"
			s.secretaryLocked().jobs[job.SourceID].executionBlocked = true
			if len(batch.attempts) > 0 {
				batch.attempts[len(batch.attempts)-1].State = "unknown"
			}
		}
		return domain.SecretaryAttempt{}, domain.ErrSecretaryExecutionUnknown
	}
	id, err := newSecret("attempt")
	if err != nil {
		return domain.SecretaryAttempt{}, err
	}
	attempt := domain.SecretaryAttempt{
		ID:        id,
		BatchID:   job.BatchID,
		State:     "running",
		CreatedAt: s.now().UTC(),
		UpdatedAt: s.now().UTC(),
	}
	batch.attempts = append(batch.attempts, attempt)
	batch.state = "running"
	return attempt, nil
}

func (s *MemoryStore) activeSecretaryBatchLocked(
	job domain.SecretaryJob,
) (*memorySecretaryExecutionBatch, error) {
	if !s.secretaryJobActiveLocked(job) {
		return nil, domain.ErrSecretaryFenced
	}
	batch := s.secretaryExecutionsLocked()[job.BatchID]
	if batch == nil || batch.lease != job.LeaseToken || batch.owner != job.OwnerUserID ||
		batch.source != job.SourceID {
		return nil, domain.ErrSecretaryFenced
	}
	return batch, nil
}

func (s *MemoryStore) SaveSecretaryAttemptOutput(
	ctx context.Context,
	job domain.SecretaryJob,
	id, output string,
	reads domain.SecretaryToolReads,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := secretaryOutputBytes(output, reads)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	batch, err := s.activeSecretaryBatchLocked(job)
	if err != nil {
		return err
	}
	if len(batch.attempts) == 0 {
		return domain.ErrSecretaryFenced
	}
	attempt := &batch.attempts[len(batch.attempts)-1]
	if attempt.ID != id || (attempt.State != "running" && attempt.State != "unknown") {
		return domain.ErrSecretaryFenced
	}
	attempt.Output, attempt.State, attempt.UpdatedAt = output, "output_received", s.now().UTC()
	_ = json.Unmarshal(raw, &attempt.Reads)
	batch.state = "output_received"
	return nil
}

func (s *MemoryStore) FailSecretaryAttempt(
	ctx context.Context,
	job domain.SecretaryJob,
	id string,
	failure error,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	batch, err := s.activeSecretaryBatchLocked(job)
	if err != nil {
		return err
	}
	if len(batch.attempts) == 0 {
		return domain.ErrSecretaryFenced
	}
	attempt := &batch.attempts[len(batch.attempts)-1]
	if attempt.ID != id ||
		(attempt.State != "running" && attempt.State != "output_received" && attempt.State != "unknown") {
		return domain.ErrSecretaryFenced
	}
	if secretaryNotDispatched(failure) {
		if attempt.State != "running" {
			return domain.ErrSecretaryFenced
		}
		s.failSecretaryConnectionLocked(job, batch, attempt, failure)
		return nil
	}
	if attempt.State == "output_received" &&
		errors.Is(failure, domain.ErrSecretaryExecutionUnknown) {
		return nil
	}
	validationAttempts := len(batch.attempts)
	for _, previous := range batch.attempts {
		if previous.NotDispatched {
			validationAttempts--
		}
	}
	attempt.State, batch.state = secretaryAttemptFailure(failure, validationAttempts)
	attempt.Error, attempt.UpdatedAt = secretaryErrorText(failure), s.now().UTC()
	queued := s.secretaryLocked().jobs[job.SourceID]
	if batch.state == "superseded" {
		queued.work.RetryEpoch++
		queued.executionBlocked = false
	} else if batch.state == "unknown" {
		queued.executionBlocked = true
	} else if batch.state != "pending" {
		var input domain.SecretaryJob
		_ = json.Unmarshal(batch.input, &input)
		queued.executionBlocked = queued.desired <= input.ThroughRevision
	}
	return nil
}

func (s *MemoryStore) readSecretaryBatchCommitLocked(
	job domain.SecretaryJob,
	hash string,
) ([]domain.SummaryVersion, bool, error) {
	batch := s.secretaryExecutionsLocked()[job.BatchID]
	if batch != nil && (batch.owner != job.OwnerUserID || batch.source != job.SourceID) {
		return nil, false, domain.ErrNotFound
	}
	if batch == nil || batch.state != "committed" {
		return nil, false, nil
	}
	if batch.requestHash != hash {
		return nil, false, domain.ErrConflict
	}
	results := make([]domain.SummaryVersion, 0, len(batch.results))
	for _, ref := range batch.results {
		version, err := s.getMemorySummaryLocked(
			job.OwnerUserID,
			ref.SummaryID,
			ref.Revision,
			map[domain.SummaryRef]bool{},
		)
		if err != nil {
			return nil, false, err
		}
		results = append(results, version)
	}
	return results, true, nil
}

func (s *MemoryStore) commitSecretaryBatchLocked(
	job domain.SecretaryJob,
	hash string,
	results []domain.SummaryVersion,
) error {
	if job.BatchID == "" {
		return nil
	}
	batch, err := s.activeSecretaryBatchLocked(job)
	if err != nil {
		return err
	}
	if len(batch.attempts) == 0 {
		return domain.ErrSecretaryFenced
	}
	attempt := &batch.attempts[len(batch.attempts)-1]
	if attempt.ID != job.AttemptID || attempt.State != "output_received" {
		return domain.ErrSecretaryFenced
	}
	batch.state, batch.requestHash = "committed", hash
	batch.results = append([]domain.SummaryVersion{}, results...)
	attempt.State, attempt.UpdatedAt = "committed", s.now().UTC()
	return nil
}
