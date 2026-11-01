package storage

import (
	"encoding/json"
	"errors"
	"sort"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *MemoryStore) loadMemorySecretaryJobLocked(
	job *memorySecretaryJob,
) (domain.SecretaryJob, error) {
	work := domain.SecretaryJob{SourceID: job.work.SourceID, OwnerUserID: job.work.OwnerUserID,
		SourceAgentID: job.work.SourceAgentID, SourceSessionID: job.work.SourceSessionID,
		TargetSummaryID: secretarySummaryID(job.work.OwnerUserID, job.work.SourceID)}
	session := s.sessions[sessionKey(work.SourceAgentID, work.SourceSessionID)]
	work.SourceMetadata = domain.SecretarySourceMetadata{Title: session.SessionName,
		PrimaryProjectID: session.PrimaryProjectID, CWD: session.PaxConfig.CWD,
		WorkspaceRoots: append([]string(nil), session.WorkspaceRoots...)}
	if session.CustomSessionName != "" {
		work.SourceMetadata.Title = session.CustomSessionName
	}
	if work.SourceMetadata.CWD == "" {
		var metadata struct {
			InitialCWD string                  `json:"initial_cwd"`
			PaxConfig  domain.SessionPaxConfig `json:"pax_config"`
		}
		_ = json.Unmarshal(session.Metadata, &metadata)
		work.SourceMetadata.CWD = metadata.PaxConfig.CWD
		if work.SourceMetadata.CWD == "" {
			work.SourceMetadata.CWD = metadata.InitialCWD
		}
	}
	if err := s.memorySecretaryBaselineLocked(job, &work); err != nil {
		return work, err
	}
	var ids []string
	for id, revision := range job.changes {
		if revision > job.processed {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return job.changes[ids[i]] < job.changes[ids[j]] })
	size := 0
	for _, id := range ids {
		if len(work.Messages) >= domain.SecretaryMaxMessages {
			break
		}
		doc, err := s.readSearchMessageLocked(work.OwnerUserID, id)
		if errors.Is(err, ErrNotFound) {
			if job.checkpoint[id] != "" {
				work.RemovedMessageIDs = append(work.RemovedMessageIDs, id)
			}
		} else if err != nil {
			return work, err
		} else if doc.AgentID != work.SourceAgentID || doc.SessionID != work.SourceSessionID {
			work.RemovedMessageIDs = append(work.RemovedMessageIDs, id)
		} else if doc.ContentHash != job.checkpoint[id] {
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
	secretaryFinalizeDelta(&work)
	return work, secretaryWithinBounds(work.Messages)
}

func (s *MemoryStore) memorySecretaryBaselineLocked(
	job *memorySecretaryJob,
	work *domain.SecretaryJob,
) error {
	versions := s.secretaryLocked().summaries[work.TargetSummaryID]
	marker := int64(-1)
	if len(versions) > 0 {
		latest := versions[len(versions)-1]
		if latest.OwnerUserID != work.OwnerUserID {
			return ErrNotFound
		}
		work.ExpectedRevision = latest.Revision
		current, err := s.getMemorySummaryLocked(
			work.OwnerUserID,
			work.TargetSummaryID,
			latest.Revision,
			map[domain.SummaryRef]bool{},
		)
		if err == nil {
			work.CurrentSummary = &current
			return nil
		}
		if !errors.Is(err, ErrNotFound) {
			return err
		}
		marker = latest.Revision
		work.ResetReason = "source_visibility_changed"
		for _, ref := range latest.Coverage {
			if _, err = s.readSearchMessageLocked(work.OwnerUserID, ref.MessageID); errors.Is(
				err,
				ErrNotFound,
			) {
				work.RemovedMessageIDs = append(work.RemovedMessageIDs, ref.MessageID)
			}
		}
	} else if job.processed > 0 {
		work.ResetReason = "rolling_summary_rebuild"
	} else {
		return nil
	}
	work.ResetRequired = true
	if job.resetRevision == marker {
		return nil
	}
	job.resetRevision = marker
	job.checkpoint = map[string]string{}
	var docs []domain.SearchDocument
	for id, message := range s.messages {
		if message.AgentID != work.SourceAgentID || message.SessionID != work.SourceSessionID {
			continue
		}
		if doc, err := s.readSearchMessageLocked(work.OwnerUserID, id); err == nil {
			docs = append(docs, doc)
		}
	}
	sort.Slice(docs, func(i, j int) bool { return docs[i].SessionSeq < docs[j].SessionSeq })
	for _, doc := range docs {
		s.markSecretaryMemoryDirtyLocked(job, doc.SourceID, doc.ContentHash)
	}
	return nil
}

func (s *MemoryStore) validateMemorySecretaryGraphLocked(
	request domain.PublishSummaryRequest,
) error {
	ids := map[string]bool{}
	for _, ref := range request.Coverage {
		ids[ref.MessageID] = true
	}
	pending := append([]domain.SummaryRef(nil), request.SummarySources...)
	seen := map[domain.SummaryRef]bool{}
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
		child := versions[ref.Revision-1]
		for _, source := range child.Coverage {
			ids[source.MessageID] = true
		}
		pending = append(pending, child.SummarySources...)
		if len(ids) > secretaryCoverageLimit || len(seen) > secretaryCoverageLimit {
			return ErrConflict
		}
	}
	if len(ids) > secretaryCoverageLimit {
		return ErrConflict
	}
	return nil
}
