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
	if operation := s.activeMemoryOperationLocked(work.SourceID); operation != nil {
		return s.loadMemoryOperationWorkLocked(job, work, operation)
	}
	var ids []string
	for id, revision := range job.changes {
		if revision > job.processed {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		if job.changes[ids[i]] == job.changes[ids[j]] {
			return ids[i] < ids[j]
		}
		return job.changes[ids[i]] < job.changes[ids[j]]
	})
	batch := newSecretaryBatch(&work)
	for _, id := range ids[:min(len(ids), secretaryChangeScanLimit)] {
		if job.processedChanges[id] >= job.changes[id] {
			work.ThroughRevision = job.changes[id]
			continue
		}
		var doc domain.SearchDocument
		err := ErrNotFound
		if domain.SearchVisibleMessageMetadata(s.messages[id]) {
			doc, err = s.readSearchMessageLocked(work.OwnerUserID, id)
		}
		change := secretaryChange{id: id, revision: job.changes[id], hash: job.checkpoint[id],
			canonical: domain.SearchCanonicalizationVersion}
		consumed, err := batch.consume(change, doc, err)
		if err != nil {
			return work, err
		}
		if !consumed || batch.full() {
			break
		}
	}
	if work.ThroughRevision == 0 {
		work.ThroughRevision = job.desired
	}
	secretaryFinalizeDelta(&work)
	for _, id := range sortedSecretaryNodeIDs(s.secretaryLocked().nodes, work.OwnerUserID, work.SourceID, "") {
		versions := s.secretaryLocked().summaries[id]
		latest := versions[len(versions)-1]
		if secretaryChangedSummary(work, latest) {
			secretaryAddInvalidated(
				&work,
				domain.SummaryRef{SummaryID: id, Revision: latest.Revision},
			)
		}
	}
	secretaryHideInvalidatedBodies(&work)
	return work, secretaryWithinBounds(work.Messages)
}

func (s *MemoryStore) memorySecretaryBaselineLocked(
	job *memorySecretaryJob,
	work *domain.SecretaryJob,
) error {
	state := s.secretaryLocked()
	for id, versions := range state.summaries {
		if len(versions) == 0 {
			continue
		}
		latest := versions[len(versions)-1]
		if _, exists := state.nodes[id]; !exists {
			state.nodes[id] = memoryKnowledgeNode{OwnerUserID: latest.OwnerUserID, Type: "summary",
				CurrentRevision: latest.Revision, OriginSourceID: summaryOriginSource(latest)}
		}
	}
	ids := sortedSecretaryNodeIDs(state.nodes, work.OwnerUserID, work.SourceID, "")
	invalidated := s.loadMemoryNodeInvalidationsLocked(work)
	size := 0
	for index, id := range ids {
		if index == 100 {
			work.SummariesHasMore = true
			work.SummariesCursor = summaryNextCursor(
				work.OwnerUserID+":"+work.SourceID,
				ids[index-1],
			)
			break
		}
		if id == work.TargetSummaryID {
			work.ExpectedRevision = state.nodes[id].CurrentRevision
		}
		if invalidated[id] {
			continue
		}
		latest := state.summaries[id][len(state.summaries[id])-1]
		stored, _ := json.Marshal(latest)
		if size+len(stored) > secretaryNodeStateBytes {
			work.SummariesHasMore = true
			if index > 0 {
				work.SummariesCursor = summaryNextCursor(
					work.OwnerUserID+":"+work.SourceID,
					ids[index-1],
				)
			}
			break
		}
		current, err := s.getMemorySummaryLocked(
			work.OwnerUserID,
			id,
			latest.Revision,
			map[domain.SummaryRef]bool{},
		)
		if errors.Is(err, ErrNotFound) {
			work.InvalidatedSummaries = append(
				work.InvalidatedSummaries,
				domain.SummaryRef{SummaryID: id, Revision: latest.Revision},
			)
			for _, ref := range latest.Coverage {
				if _, readErr := s.readSearchMessageLocked(work.OwnerUserID, ref.MessageID); errors.Is(
					readErr,
					ErrNotFound,
				) {
					work.RemovedMessageIDs = append(work.RemovedMessageIDs, ref.MessageID)
				}
			}
			work.ResetRequired, work.ResetReason = true, "source_visibility_changed"
			continue
		}
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(current)
		if size+len(raw) > secretaryNodeStateBytes {
			work.SummariesHasMore = true
			if index > 0 {
				work.SummariesCursor = summaryNextCursor(
					work.OwnerUserID+":"+work.SourceID,
					ids[index-1],
				)
			}
			break
		}
		size += len(raw)
		work.CurrentSummaries = append(work.CurrentSummaries, current)
		if id == work.TargetSummaryID {
			work.ExpectedRevision = current.Revision
			work.CurrentSummary = &current
		}
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
