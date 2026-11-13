package storage

import (
	"context"
	"errors"
	"maps"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *MemoryStore) PublishSecretaryBatch(
	_ context.Context, job domain.SecretaryJob, publication domain.SecretaryBatchPublication,
) ([]domain.SummaryVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	requests, hash, err := secretaryNodePublication(job, publication)
	if err != nil {
		return nil, err
	}
	state := s.secretaryLocked()
	if results, committed, readErr := s.readSecretaryBatchCommitLocked(job, hash); readErr != nil ||
		committed {
		return results, readErr
	}
	if err = s.validateMemorySecretaryNodeInputsLocked(job); err != nil {
		return nil, err
	}
	if err = s.validateMemorySecretaryFrozenHeadsLocked(job); err != nil {
		return nil, err
	}
	if err = s.validateMemoryNodeSourcesCompleteLocked(job.OwnerUserID, requests); err != nil {
		return nil, err
	}
	before := *state
	state.summaries, state.snapshots = maps.Clone(state.summaries), maps.Clone(state.snapshots)
	state.requests, state.nodes = maps.Clone(state.requests), maps.Clone(state.nodes)
	rollback := func(err error) ([]domain.SummaryVersion, error) {
		*state = before
		return nil, err
	}
	results, err := s.publishMemorySecretaryNodeOutputsLocked(job, requests)
	if err != nil {
		return rollback(err)
	}
	if err = s.validateMemorySecretaryNodeBudgetLocked(results); err != nil {
		return rollback(err)
	}
	if err = s.commitSecretaryBatchLocked(job, hash, results); err != nil {
		return rollback(err)
	}
	s.checkpointMemorySecretaryNodesLocked(job, publication.Outcomes)
	s.finishMemoryOperationBatchLocked(job, results)
	s.clearMemoryNodeInvalidationsLocked(job, results)
	return results, nil
}

func (s *MemoryStore) validateMemorySecretaryNodeInputsLocked(job domain.SecretaryJob) error {
	if !s.secretaryJobActiveLocked(job) {
		return domain.ErrSecretaryFenced
	}
	for _, message := range job.Messages {
		current, err := s.readSearchMessageLocked(job.OwnerUserID, message.SourceID)
		if err != nil {
			return err
		}
		if current.AgentID != job.SourceAgentID || current.SessionID != job.SourceSessionID ||
			current.ContentHash != message.ContentHash ||
			current.CanonicalizationVersion != message.CanonicalizationVersion {
			return domain.ErrSourceVersionUnavailable
		}
	}
	if !s.secretarySourcesCompleteLocked(job) {
		return domain.ErrSecretarySourceIncomplete
	}
	return nil
}

func (s *MemoryStore) publishMemorySecretaryNodeOutputsLocked(
	job domain.SecretaryJob,
	requests []domain.PublishSummaryRequest,
) ([]domain.SummaryVersion, error) {
	results := make([]domain.SummaryVersion, 0, len(requests))
	for _, request := range requests {
		if node, exists := s.secretaryLocked().nodes[request.SummaryID]; exists &&
			(node.OwnerUserID != job.OwnerUserID || node.Type != "summary" || node.OriginSourceID != job.SourceID) {
			return nil, ErrNotFound
		}
		result, err := s.publishMemorySummaryLocked(job.OwnerUserID, request, true)
		if err != nil {
			return nil, err
		}
		result.AuthorKind, result.AuthorID, result.OperationID = "agent", job.AgentID, job.OperationID
		s.secretaryLocked().summaries[result.SummaryID][result.Revision-1] = result
		results = append(results, result)
	}
	return results, nil
}

func (s *MemoryStore) checkpointMemorySecretaryNodesLocked(
	job domain.SecretaryJob,
	outcomes []domain.SecretaryMessageOutcome,
) {
	current := s.secretaryLocked().jobs[job.SourceID]
	if current.outcomes == nil {
		current.outcomes = map[string]domain.SecretaryMessageOutcome{}
	}
	if current.processedChanges == nil {
		current.processedChanges = map[string]int64{}
	}
	for _, outcome := range outcomes {
		current.outcomes[outcome.MessageID] = outcome
	}
	for _, message := range job.Messages {
		current.checkpoint[message.SourceID] = message.ContentHash
		current.processedChanges[message.SourceID] = current.changes[message.SourceID]
	}
	for _, id := range job.RemovedMessageIDs {
		delete(current.checkpoint, id)
		current.processedChanges[id] = current.changes[id]
	}
	if job.OperationID == "" {
		current.processed = max(current.processed, job.ThroughRevision)
	} else {
		s.advanceMemorySecretaryCursorLocked(current)
	}
	current.executionBlocked = false
	current.work.LeaseToken, current.work.ValidationFeedback = "", ""
	current.autoRetryKey, current.autoRetryCount, current.lastError = "", 0, ""
	current.leaseUntil = s.now()
	now := s.now()
	current.completed = &now
}

func (s *MemoryStore) validateMemorySecretaryNodeBudgetLocked(
	results []domain.SummaryVersion,
) error {
	pending := secretarySummaryRefs(results)
	seen, messages := map[domain.SummaryRef]bool{}, map[string]bool{}
	bytes := 0
	for len(pending) > 0 {
		ref := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if seen[ref] {
			continue
		}
		seen[ref] = true
		versions := s.secretaryLocked().summaries[ref.SummaryID]
		if ref.Revision <= 0 || ref.Revision > int64(len(versions)) {
			return ErrNotFound
		}
		version := versions[ref.Revision-1]
		pending = append(pending, version.SummarySources...)
		for _, message := range version.Coverage {
			if messages[message.Identity()] {
				continue
			}
			messages[message.Identity()] = true
			doc, err := s.readMemoryMessageVersionLocked(version.OwnerUserID, message)
			if err != nil {
				return err
			}
			bytes += len(doc.Body)
		}
		if len(seen) > secretaryCoverageLimit || len(messages) > secretaryCoverageLimit ||
			bytes > domain.SecretaryMaxSourceBytes {
			return ErrConflict
		}
	}
	return nil
}

func (s *MemoryStore) ListSecretaryNodeSummaries(
	_ context.Context, owner, source string, query domain.SummaryListQuery,
) (domain.SecretarySummaryPage, error) {
	after, limit, err := summaryListQuery(owner+":"+source, query)
	if owner == "" {
		return domain.SecretarySummaryPage{}, ErrUnauthorized
	}
	if err != nil {
		return domain.SecretarySummaryPage{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.secretaryLocked().jobs[source]
	if job == nil || job.work.OwnerUserID != owner ||
		!s.secretaryAgentOwnedLocked(owner, job.work.SourceAgentID) {
		return domain.SecretarySummaryPage{}, ErrNotFound
	}
	if _, err = s.readSearchGroupSessionLocked(owner, "", domain.SearchDocument{AgentID: job.work.SourceAgentID, SessionID: job.work.SourceSessionID}); err != nil {
		return domain.SecretarySummaryPage{}, err
	}
	return s.memorySecretarySummaryPageLocked(owner, source, after, limit)
}

func (s *MemoryStore) memorySecretarySummaryPageLocked(
	owner, source, after string, limit int,
) (domain.SecretarySummaryPage, error) {
	ids := sortedSecretaryNodeIDs(s.secretaryLocked().nodes, owner, source, after)
	page := domain.SecretarySummaryPage{Items: []domain.SummaryVersion{}}
	if len(ids) > limit {
		page.NextCursor = summaryNextCursor(owner+":"+source, ids[limit-1])
		ids = ids[:limit]
	}
	for _, id := range ids {
		current, err := s.getMemorySummaryLocked(owner, id, 0, map[domain.SummaryRef]bool{})
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return domain.SecretarySummaryPage{}, err
		}
		page.Items = append(page.Items, current)
	}
	return page, nil
}
