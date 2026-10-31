package storage

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

var _ domain.SummaryStore = (*MemoryStore)(nil)

func cloneMemorySummary(value domain.SummaryVersion) domain.SummaryVersion {
	raw, _ := json.Marshal(value)
	var result domain.SummaryVersion
	_ = json.Unmarshal(raw, &result)
	return result
}

func (s *MemoryStore) PublishSummary(
	_ context.Context,
	owner string,
	request domain.PublishSummaryRequest,
) (domain.SummaryVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.publishMemorySummaryLocked(owner, request)
}

func (s *MemoryStore) publishMemorySummaryLocked(
	owner string,
	request domain.PublishSummaryRequest,
) (domain.SummaryVersion, error) {
	if owner == "" {
		return domain.SummaryVersion{}, ErrUnauthorized
	}
	if err := request.Validate(); err != nil {
		return domain.SummaryVersion{}, err
	}
	state := s.secretaryLocked()
	request = summaryWithoutReceipts(request)
	hash := secretaryRequestHash(request)
	key := owner + ":" + request.RequestID
	if old, ok := state.requests[key]; ok {
		if old.hash != hash {
			return domain.SummaryVersion{}, ErrConflict
		}
		return s.getMemorySummaryLocked(
			owner,
			old.ref.SummaryID,
			old.ref.Revision,
			map[domain.SummaryRef]bool{},
		)
	}
	versions := state.summaries[request.SummaryID]
	if len(versions) > 0 {
		latest := versions[len(versions)-1]
		if latest.OwnerUserID != owner {
			return domain.SummaryVersion{}, ErrNotFound
		}
		if latest.SubjectType != request.SubjectType || latest.SubjectID != request.SubjectID {
			return domain.SummaryVersion{}, ErrConflict
		}
	}
	if int64(len(versions)) != request.ExpectedRevision {
		return domain.SummaryVersion{}, domain.ErrSummaryVersionMismatch
	}
	var frozen []domain.SearchDocument
	for _, ref := range request.Coverage {
		doc, err := s.readMemoryMessageVersionLocked(owner, ref)
		if err != nil {
			return domain.SummaryVersion{}, err
		}
		frozen = append(frozen, doc)
	}
	if err := validateSummaryQuotes(request, frozen); err != nil {
		return domain.SummaryVersion{}, err
	}
	for _, ref := range request.SummarySources {
		if _, err := s.getMemorySummaryLocked(owner, ref.SummaryID, ref.Revision, map[domain.SummaryRef]bool{}); err != nil {
			return domain.SummaryVersion{}, err
		}
	}
	result := domain.SummaryVersion{
		SummaryID:      request.SummaryID,
		Revision:       request.ExpectedRevision + 1,
		OwnerUserID:    owner,
		SubjectType:    request.SubjectType,
		SubjectID:      request.SubjectID,
		Title:          request.Title,
		Body:           request.Body,
		Claims:         request.Claims,
		Coverage:       request.Coverage,
		SummarySources: request.SummarySources,
		CreatedAt:      s.now().UTC(),
	}
	result = cloneMemorySummary(result)
	for _, doc := range frozen {
		ref := domain.MessageVersionRef{
			MessageID:               doc.SourceID,
			ContentHash:             doc.ContentHash,
			CanonicalizationVersion: doc.CanonicalizationVersion,
		}
		state.snapshots[ref.Identity()] = doc
	}
	state.summaries[result.SummaryID] = append(versions, result)
	state.requests[key] = memorySecretaryRequest{
		hash: hash,
		ref:  domain.SummaryRef{SummaryID: result.SummaryID, Revision: result.Revision},
	}
	return cloneMemorySummary(result), nil
}

func (s *MemoryStore) GetSummary(
	_ context.Context,
	owner, id string,
	revision int64,
) (domain.SummaryVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getMemorySummaryLocked(owner, id, revision, map[domain.SummaryRef]bool{})
}

func (s *MemoryStore) getMemorySummaryLocked(
	owner, id string,
	revision int64,
	seen map[domain.SummaryRef]bool,
) (domain.SummaryVersion, error) {
	if owner == "" {
		return domain.SummaryVersion{}, ErrUnauthorized
	}
	versions := s.secretaryLocked().summaries[id]
	if revision == 0 {
		revision = int64(len(versions))
	}
	if revision <= 0 || revision > int64(len(versions)) {
		return domain.SummaryVersion{}, ErrNotFound
	}
	result := versions[revision-1]
	if result.OwnerUserID != owner {
		return domain.SummaryVersion{}, ErrNotFound
	}
	ref := domain.SummaryRef{SummaryID: id, Revision: revision}
	if seen[ref] {
		return cloneMemorySummary(result), nil
	}
	if len(seen) >= 4096 {
		return domain.SummaryVersion{}, ErrConflict
	}
	seen[ref] = true
	for _, message := range result.Coverage {
		if _, err := s.readSearchMessageLocked(owner, message.MessageID); err != nil {
			return domain.SummaryVersion{}, err
		}
	}
	for _, child := range result.SummarySources {
		if _, err := s.getMemorySummaryLocked(owner, child.SummaryID, child.Revision, seen); err != nil {
			return domain.SummaryVersion{}, err
		}
	}
	return cloneMemorySummary(result), nil
}

func (s *MemoryStore) ReadMessageVersion(
	_ context.Context,
	owner string,
	ref domain.MessageVersionRef,
) (domain.SearchDocument, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readMemoryMessageVersionLocked(owner, ref)
}

func (s *MemoryStore) readMemoryMessageVersionLocked(
	owner string,
	ref domain.MessageVersionRef,
) (domain.SearchDocument, error) {
	if owner == "" {
		return domain.SearchDocument{}, ErrUnauthorized
	}
	if !ref.Valid() {
		return domain.SearchDocument{}, domain.ErrSourceVersionUnavailable
	}
	current, err := s.readSearchMessageLocked(owner, ref.MessageID)
	if err != nil {
		return domain.SearchDocument{}, err
	}
	if current.ContentHash == ref.ContentHash &&
		current.CanonicalizationVersion == ref.CanonicalizationVersion {
		return current, nil
	}
	doc, ok := s.secretaryLocked().snapshots[ref.Identity()]
	if !ok {
		return domain.SearchDocument{}, domain.ErrSourceVersionUnavailable
	}
	if doc.OwnerUserID != owner {
		return domain.SearchDocument{}, ErrNotFound
	}
	return doc, nil
}

func (s *MemoryStore) PublishSecretaryJob(
	_ context.Context,
	work domain.SecretaryJob,
	request domain.PublishSummaryRequest,
) (domain.SummaryVersion, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validateSecretaryPublication(work, request); err != nil {
		return domain.SummaryVersion{}, err
	}
	if !s.secretaryJobActiveLocked(work) {
		return domain.SummaryVersion{}, domain.ErrSecretaryFenced
	}
	job := s.secretaryLocked().jobs[work.SourceID]
	s.refreshSecretaryMessagesLocked(job)
	result, err := s.publishMemorySummaryLocked(work.OwnerUserID, request)
	if err != nil {
		return domain.SummaryVersion{}, err
	}
	for _, message := range work.Messages {
		job.checkpoint[message.SourceID] = message.ContentHash
	}
	job.processed = max(job.processed, work.ThroughRevision)
	job.work.LeaseToken = ""
	job.leaseUntil = s.now()
	job.lastError = ""
	now := s.now()
	job.completed = &now
	return result, nil
}

func (s *MemoryStore) memorySecretaryBlockLocked(
	work *domain.SecretaryJob,
	messageID string,
) error {
	for _, versions := range s.secretaryLocked().summaries {
		latest := versions[len(versions)-1]
		if latest.OwnerUserID != work.OwnerUserID || latest.SubjectType != "fragment" ||
			latest.SubjectID != secretarySubject(work.SourceID) {
			continue
		}
		matched := false
		for _, ref := range latest.Coverage {
			matched = matched || ref.MessageID == messageID
		}
		if !matched {
			continue
		}
		work.TargetSummaryID = latest.SummaryID
		work.ExpectedRevision = latest.Revision
		for _, ref := range latest.Coverage {
			doc, err := s.readSearchMessageLocked(work.OwnerUserID, ref.MessageID)
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			work.Messages = append(work.Messages, doc)
		}
		return secretaryWithinBounds(work.Messages)
	}
	return nil
}

func (s *MemoryStore) loadMemorySecretaryJobLocked(
	job *memorySecretaryJob,
) (domain.SecretaryJob, error) {
	work := domain.SecretaryJob{
		SourceID:        job.work.SourceID,
		OwnerUserID:     job.work.OwnerUserID,
		SourceAgentID:   job.work.SourceAgentID,
		SourceSessionID: job.work.SourceSessionID,
	}
	var ids []string
	for id, revision := range job.changes {
		if revision > job.processed {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return job.changes[ids[i]] < job.changes[ids[j]] })
	if len(ids) > domain.SecretaryMaxMessages {
		ids = ids[:domain.SecretaryMaxMessages]
	}
	block := map[string]bool{}
	size := 0
	for _, id := range ids {
		doc, err := s.readSearchMessageLocked(work.OwnerUserID, id)
		if errors.Is(err, ErrNotFound) || err == nil && doc.ContentHash == job.checkpoint[id] {
			work.ThroughRevision = job.changes[id]
			continue
		}
		if err != nil {
			return work, err
		}
		if len(work.Messages) == 0 {
			if err = s.memorySecretaryBlockLocked(&work, id); err != nil {
				return work, err
			}
			for _, member := range work.Messages {
				block[member.SourceID] = true
				size += len(member.Body)
			}
		}
		if work.TargetSummaryID != "" {
			if !block[id] {
				break
			}
		} else {
			boundary, boundaryErr := s.memorySecretaryBlockBoundaryLocked(work, id)
			if boundaryErr != nil {
				return work, boundaryErr
			}
			if boundary {
				break
			}
			if len(work.Messages) > 0 && size+len(doc.Body) > domain.SecretaryMaxBytes {
				break
			}
			work.Messages = append(work.Messages, doc)
			size += len(doc.Body)
		}
		work.ThroughRevision = job.changes[id]
	}
	if work.ThroughRevision == 0 {
		work.ThroughRevision = job.desired
	}
	if err := secretaryWithinBounds(work.Messages); err != nil {
		return work, err
	}
	work.PreviousSummaries = s.memorySecretaryPreviousLocked(work)
	return work, nil
}

func (s *MemoryStore) memorySecretaryBlockBoundaryLocked(
	work domain.SecretaryJob,
	id string,
) (bool, error) {
	if len(work.Messages) == 0 {
		return false, nil
	}
	existing := domain.SecretaryJob{OwnerUserID: work.OwnerUserID, SourceID: work.SourceID}
	err := s.memorySecretaryBlockLocked(&existing, id)
	return existing.TargetSummaryID != "", err
}

func (s *MemoryStore) memorySecretaryPreviousLocked(
	work domain.SecretaryJob,
) []domain.SummaryVersion {
	for id, versions := range s.secretaryLocked().summaries {
		latest := versions[len(versions)-1]
		if latest.OwnerUserID != work.OwnerUserID || latest.SubjectType != "fragment" ||
			latest.SubjectID != secretarySubject(work.SourceID) {
			continue
		}
		if value, err := s.getMemorySummaryLocked(work.OwnerUserID, id, 0, map[domain.SummaryRef]bool{}); err == nil {
			work.PreviousSummaries = append(work.PreviousSummaries, value)
		}
	}
	sort.Slice(work.PreviousSummaries, func(i, j int) bool {
		return work.PreviousSummaries[i].CreatedAt.After(work.PreviousSummaries[j].CreatedAt)
	})
	if len(work.PreviousSummaries) > 10 {
		work.PreviousSummaries = work.PreviousSummaries[:10]
	}
	return work.PreviousSummaries
}
