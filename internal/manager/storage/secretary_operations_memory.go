package storage

import (
	"context"
	"sort"
	"strconv"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

var _ domain.SecretaryOperationStore = (*MemoryStore)(nil)

func (s *MemoryStore) secretaryOperationsLocked() map[string]*memorySecretaryOperation {
	state := s.secretaryLocked()
	if state.operations == nil {
		state.operations = map[string]*memorySecretaryOperation{}
	}
	return state.operations
}

func (s *MemoryStore) secretaryOperationSourceLocked(
	owner, source string,
) (*memorySecretaryJob, error) {
	if owner == "" {
		return nil, ErrUnauthorized
	}
	state := s.secretaryLocked()
	job := state.jobs[source]
	if job == nil {
		for _, session := range s.sessions {
			if strconv.FormatInt(session.ID, 10) != source || session.ID == 0 {
				continue
			}
			job = &memorySecretaryJob{
				work: domain.SecretaryJob{SourceID: source, OwnerUserID: owner,
					SourceAgentID: session.AgentID, SourceSessionID: session.SessionID},
				changes:    map[string]int64{},
				seen:       map[string]string{},
				checkpoint: map[string]string{},
				outcomes:   map[string]domain.SecretaryMessageOutcome{},
			}
			break
		}
	}
	if job == nil || job.work.OwnerUserID != owner || !s.secretaryPendingSourceVisibleLocked(job) {
		return nil, ErrNotFound
	}
	state.jobs[source] = job
	s.refreshSecretaryMessagesLocked(job)
	s.refreshMemoryNodeInvalidationsLocked()
	return job, nil
}

func (s *MemoryStore) PrepareSecretaryOperation(ctx context.Context, owner string,
	input domain.SecretaryOperationPrepare,
) (domain.SecretaryOperation, error) {
	if err := ctx.Err(); err != nil {
		return domain.SecretaryOperation{}, err
	}
	if err := validateSecretaryOperationPrepare(input); err != nil {
		return domain.SecretaryOperation{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job, err := s.secretaryOperationSourceLocked(owner, input.SourceID)
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
		Instructions: s.secretaryInstructionsLocked(owner, input.SourceID),
		CreatedAt:    s.now().UTC(),
		UpdatedAt:    s.now().UTC(),
	}
	if err = s.prepareMemoryOperationBaselineLocked(owner, &operation, input.SummaryID); err != nil {
		return domain.SecretaryOperation{}, err
	}
	if input.Kind != "human_edit" {
		operation.Targets, err = s.prepareMemoryOperationTargetsLocked(job, input)
		if err != nil {
			return domain.SecretaryOperation{}, err
		}
	}
	operation.OperationID, err = newSecret("operation")
	if err != nil {
		return domain.SecretaryOperation{}, err
	}
	s.secretaryOperationsLocked()[operation.OperationID] = &memorySecretaryOperation{
		value: operation, owner: owner, completed: map[string]bool{}}
	return cloneSecretaryOperation(operation), nil
}

func (s *MemoryStore) prepareMemoryOperationBaselineLocked(
	owner string,
	operation *domain.SecretaryOperation,
	id string,
) error {
	for _, nodeID := range sortedSecretaryNodeIDs(s.secretaryLocked().nodes, owner, operation.SourceID, "") {
		node := s.secretaryLocked().nodes[nodeID]
		operation.BaselineRefs = append(
			operation.BaselineRefs,
			domain.SummaryRef{SummaryID: nodeID, Revision: node.CurrentRevision},
		)
	}
	if operation.Kind != "human_edit" {
		return nil
	}
	summary, err := s.getMemorySummaryLocked(owner, id, 0, map[domain.SummaryRef]bool{})
	if err != nil {
		return err
	}
	if summaryOriginSource(summary) != operation.SourceID {
		return ErrNotFound
	}
	operation.Summary = &summary
	return nil
}

func (s *MemoryStore) prepareMemoryOperationTargetsLocked(
	job *memorySecretaryJob,
	input domain.SecretaryOperationPrepare,
) ([]domain.SecretaryOperationTarget, error) {
	selected := map[string]bool{}
	for _, id := range input.MessageIDs {
		if selected[id] || id == "" {
			return nil, ErrConflict
		}
		selected[id] = true
	}
	ids := make([]string, 0, len(job.changes))
	for id, revision := range job.changes {
		if input.Scope == "pending" &&
			(revision <= job.processed || job.processedChanges[id] >= revision) ||
			input.Scope == "selected" && !selected[id] {
			continue
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return job.changes[ids[i]] < job.changes[ids[j]] })
	var targets []domain.SecretaryOperationTarget
	var docs []domain.SearchDocument
	for _, id := range ids {
		doc, err := s.readSearchMessageLocked(job.work.OwnerUserID, id)
		if err != nil {
			if input.Scope == "selected" {
				return nil, err
			}
			continue
		}
		if doc.AgentID != job.work.SourceAgentID || doc.SessionID != job.work.SourceSessionID {
			return nil, ErrNotFound
		}
		targets = append(
			targets,
			domain.SecretaryOperationTarget{MessageID: id, ContentHash: doc.ContentHash,
				CanonicalizationVersion: doc.CanonicalizationVersion, ChangeRevision: job.changes[id],
				Role: doc.Role, Preview: secretaryOperationPreview(doc)},
		)
		docs = append(docs, domain.SearchDocument{SourceID: doc.SourceID})
		delete(selected, id)
		if len(targets) > domain.SecretaryOperationTargetLimit {
			return nil, ErrConflict
		}
	}
	if len(selected) != 0 || len(targets) == 0 {
		return nil, ErrConflict
	}
	work := job.work
	work.Messages = docs
	if !s.secretarySourcesCompleteLocked(work) {
		return nil, domain.ErrSecretarySourceIncomplete
	}
	return targets, nil
}

func (s *MemoryStore) GetSecretaryOperation(
	ctx context.Context,
	owner, id string,
) (domain.SecretaryOperation, error) {
	if err := ctx.Err(); err != nil {
		return domain.SecretaryOperation{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	operation, err := s.readMemoryOperationLocked(owner, id)
	if err != nil {
		return domain.SecretaryOperation{}, err
	}
	return cloneSecretaryOperation(operation.value), nil
}

func (s *MemoryStore) readMemoryOperationLocked(
	owner, id string,
) (*memorySecretaryOperation, error) {
	if owner == "" {
		return nil, ErrUnauthorized
	}
	operation := s.secretaryOperationsLocked()[id]
	if operation == nil || operation.owner != owner {
		return nil, ErrNotFound
	}
	if _, _, err := s.secretaryTaskSourceLocked(owner, operation.value.SourceID); err != nil {
		return nil, err
	}
	for _, target := range operation.value.Targets {
		_, err := s.readSearchMessageLocked(owner, target.MessageID)
		if err != nil {
			return nil, err
		}
	}
	if summary := operation.value.Summary; summary != nil {
		if _, err := s.getMemorySummaryLocked(owner, summary.SummaryID, summary.Revision, map[domain.SummaryRef]bool{}); err != nil {
			return nil, err
		}
	}
	return operation, nil
}

func (s *MemoryStore) SubmitSecretaryOperation(ctx context.Context, owner, id string,
	input domain.SecretaryOperationSubmit,
) (domain.SecretaryOperation, error) {
	if err := ctx.Err(); err != nil {
		return domain.SecretaryOperation{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	stored, err := s.readMemoryOperationLocked(owner, id)
	if err != nil {
		return domain.SecretaryOperation{}, err
	}
	hash := secretaryOperationHash(input)
	for _, previous := range s.secretaryOperationsLocked() {
		if previous.owner == owner && previous.requestID == input.RequestID &&
			input.RequestID != "" {
			if previous.value.OperationID != id || previous.hash != hash {
				return domain.SecretaryOperation{}, ErrConflict
			}
			return cloneSecretaryOperation(previous.value), nil
		}
	}
	if stored.value.Status != "draft" || stored.value.Revision != input.ExpectedRevision {
		return domain.SecretaryOperation{}, ErrConflict
	}
	operation := cloneSecretaryOperation(stored.value)
	operation.Targets, err = secretaryOperationTargets(operation, input)
	if err != nil {
		return domain.SecretaryOperation{}, err
	}
	job := s.secretaryLocked().jobs[operation.SourceID]
	if err = s.validateMemoryOperationSubmitLocked(job, operation, input); err != nil {
		return domain.SecretaryOperation{}, err
	}
	if operation.Instructions == nil {
		operation.Instructions = s.secretaryInstructionsLocked(owner, operation.SourceID)
	}
	operation.Instructions.Run, _ = domain.NormalizeSecretaryInstruction(input.RunInstruction)
	var results []domain.SummaryVersion
	if operation.Kind != "guided_summary" {
		request, requestErr := secretaryOperationSummaryRequest(operation, input)
		if requestErr != nil {
			return domain.SecretaryOperation{}, requestErr
		}
		result, publishErr := s.publishMemorySummaryLocked(owner, request)
		if publishErr != nil {
			return domain.SecretaryOperation{}, publishErr
		}
		versions := s.secretaryLocked().summaries[result.SummaryID]
		result.AuthorKind, result.AuthorID, result.OperationID = "user", owner, id
		versions[result.Revision-1] = result
		results = append(results, result)
	}
	s.supersedeMemorySecretaryWorkLocked(job, id)
	operation.Revision++
	operation.UpdatedAt = s.now().UTC()
	if operation.Kind == "guided_summary" {
		operation.Status = "queued"
	} else {
		operation.Status, operation.ResultRefs = "completed", secretarySummaryRefs(results)
		s.acknowledgeMemoryOperationInvalidationsLocked(job, operation)
		s.acknowledgeMemoryOperationLocked(job, operation.Targets)
		if len(operation.Targets) > 0 {
			now := s.now().UTC()
			job.completed = &now
		}
	}
	stored.value, stored.requestID, stored.hash = operation, input.RequestID, hash
	return cloneSecretaryOperation(operation), nil
}

func (s *MemoryStore) validateMemoryOperationSubmitLocked(
	job *memorySecretaryJob,
	operation domain.SecretaryOperation,
	input domain.SecretaryOperationSubmit,
) error {
	if operation.Kind == "guided_summary" {
		cfg := s.secretaryLocked().settings[job.work.OwnerUserID]
		if job.paused || !cfg.Enabled ||
			!s.secretaryAgentOwnedLocked(job.work.OwnerUserID, cfg.AgentID) {
			return ErrConflict
		}
	}
	if s.memorySecretaryReplacementNeededLocked(job) && !input.ConfirmReplace {
		return fmtSecretaryReplacementRequired()
	}
	if len(
		sortedSecretaryNodeIDs(
			s.secretaryLocked().nodes,
			job.work.OwnerUserID,
			operation.SourceID,
			"",
		),
	) != len(
		operation.BaselineRefs,
	) {
		return domain.ErrSummaryVersionMismatch
	}
	for _, ref := range operation.BaselineRefs {
		node := s.secretaryLocked().nodes[ref.SummaryID]
		if node.OwnerUserID != job.work.OwnerUserID || node.CurrentRevision != ref.Revision {
			return domain.ErrSummaryVersionMismatch
		}
	}
	work := job.work
	work.Messages = nil
	for _, target := range operation.Targets {
		doc, err := s.readSearchMessageLocked(job.work.OwnerUserID, target.MessageID)
		if err != nil {
			return err
		}
		if !secretaryOperationMessageMatches(job.work, target, doc) {
			return secretaryOperationVersionError(target.MessageID)
		}
		work.Messages = append(work.Messages, domain.SearchDocument{SourceID: doc.SourceID})
	}
	if !s.secretarySourcesCompleteLocked(work) {
		return domain.ErrSecretarySourceIncomplete
	}
	return nil
}

func (s *MemoryStore) memorySecretaryReplacementNeededLocked(job *memorySecretaryJob) bool {
	if job.work.LeaseToken != "" && job.leaseUntil.After(s.now()) {
		return true
	}
	for _, batch := range s.secretaryExecutionsLocked() {
		if batch.owner == job.work.OwnerUserID && batch.source == job.work.SourceID &&
			batch.epoch == job.work.RetryEpoch && secretaryBatchOpen(batch.state) {
			return true
		}
	}
	return s.activeMemoryOperationLocked(job.work.SourceID) != nil
}

func (s *MemoryStore) supersedeMemorySecretaryWorkLocked(
	job *memorySecretaryJob,
	replacement string,
) {
	for _, batch := range s.secretaryExecutionsLocked() {
		if batch.owner != job.work.OwnerUserID || batch.source != job.work.SourceID ||
			batch.state == "committed" {
			continue
		}
		batch.state, batch.lease = "superseded", ""
		for i := range batch.attempts {
			attempt := &batch.attempts[i]
			if attempt.State == "running" || attempt.State == "unknown" ||
				attempt.State == "output_received" {
				attempt.State, attempt.UpdatedAt = "superseded", s.now().UTC()
			}
		}
	}
	for id, operation := range s.secretaryOperationsLocked() {
		if operation.value.SourceID == job.work.SourceID && id != replacement &&
			(operation.value.Status == "queued" || operation.value.Status == "running" || operation.value.Status == "needs_attention") {
			operation.value.Status = "superseded"
			operation.value.Revision++
			operation.value.UpdatedAt = s.now().UTC()
		}
	}
	job.work.RetryEpoch++
	job.work.RetryGeneration++
	job.work.LeaseToken, job.work.OperationID, job.lastError = "", "", ""
	job.leaseUntil, job.executionBlocked = time.Time{}, false
	job.autoRetryKey, job.autoRetryCount, job.work.ValidationFeedback = "", 0, ""
	job.available, job.updated = s.now(), s.now().Add(-30*time.Second)
	job.controlRevision++
}

func (s *MemoryStore) acknowledgeMemoryOperationLocked(
	job *memorySecretaryJob,
	targets []domain.SecretaryOperationTarget,
) {
	if job.processedChanges == nil {
		job.processedChanges = map[string]int64{}
	}
	for _, target := range targets {
		job.checkpoint[target.MessageID] = target.ContentHash
		job.processedChanges[target.MessageID] = job.changes[target.MessageID]
		job.outcomes[target.MessageID] = domain.SecretaryMessageOutcome{MessageID: target.MessageID,
			Action: "no_summary", Reason: "Completed by user summary operation"}
	}
	s.advanceMemorySecretaryCursorLocked(job)
}

func (s *MemoryStore) advanceMemorySecretaryCursorLocked(job *memorySecretaryJob) {
	through := job.desired
	for id, revision := range job.changes {
		if revision > job.processed && job.processedChanges[id] < revision && revision <= through {
			if s.memorySecretaryUncoveredChangeHiddenLocked(job, id) {
				continue
			}
			through = revision - 1
		}
	}
	for _, pending := range s.secretaryLocked().invalidations {
		if pending.source == job.work.SourceID && pending.acknowledgedOperationID == "" &&
			pending.changeRevision > job.processed && pending.changeRevision <= through {
			through = pending.changeRevision - 1
		}
	}
	job.processed = max(job.processed, through)
}

func (s *MemoryStore) activeMemoryOperationLocked(source string) *memorySecretaryOperation {
	job := s.secretaryLocked().jobs[source]
	for _, operation := range s.secretaryOperationsLocked() {
		if job != nil && operation.owner == job.work.OwnerUserID &&
			operation.value.SourceID == source &&
			(operation.value.Status == "queued" || operation.value.Status == "running") {
			return operation
		}
	}
	return nil
}
