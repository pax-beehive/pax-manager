package storage

import (
	"context"
	"encoding/json"

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
	rolling ...bool,
) (domain.SummaryVersion, error) {
	if owner == "" {
		return domain.SummaryVersion{}, ErrUnauthorized
	}
	validation := request.Validate()
	if len(rolling) > 0 {
		validation = validateSecretaryExpanded(request, rolling[0])
	}
	if validation != nil {
		return domain.SummaryVersion{}, validation
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
	if len(rolling) > 0 {
		if err := s.validateMemorySecretaryGraphLocked(request); err != nil {
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
	request, err := secretaryComposePublication(work, request)
	if err != nil {
		return domain.SummaryVersion{}, err
	}
	if !s.secretaryJobActiveLocked(work) {
		return domain.SummaryVersion{}, domain.ErrSecretaryFenced
	}
	job := s.secretaryLocked().jobs[work.SourceID]
	s.refreshSecretaryMessagesLocked(job)
	for _, message := range work.Messages {
		doc, readErr := s.readSearchMessageLocked(work.OwnerUserID, message.SourceID)
		if readErr != nil {
			return domain.SummaryVersion{}, readErr
		}
		if doc.AgentID != work.SourceAgentID || doc.SessionID != work.SourceSessionID {
			return domain.SummaryVersion{}, ErrConflict
		}
	}
	result, err := s.publishMemorySummaryLocked(work.OwnerUserID, request, work.ResetRequired)
	if err != nil {
		return domain.SummaryVersion{}, err
	}
	for _, message := range work.Messages {
		job.checkpoint[message.SourceID] = message.ContentHash
	}
	for _, id := range work.RemovedMessageIDs {
		delete(job.checkpoint, id)
	}
	job.processed = max(job.processed, work.ThroughRevision)
	job.work.LeaseToken = ""
	job.leaseUntil = s.now()
	job.lastError = ""
	now := s.now()
	job.completed = &now
	return result, nil
}
