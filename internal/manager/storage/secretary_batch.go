package storage

import (
	"errors"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const (
	secretaryChangePageSize  = 128
	secretaryChangeScanLimit = 1024
)

type secretaryBatch struct {
	work        *domain.SecretaryJob
	bytes       int
	sourceBytes int
	removals    int
	removed     map[string]bool
}

func newSecretaryBatch(work *domain.SecretaryJob) *secretaryBatch {
	batch := &secretaryBatch{work: work, removed: map[string]bool{}}
	for _, id := range work.RemovedMessageIDs {
		batch.removed[id] = true
	}
	return batch
}

func (b *secretaryBatch) full() bool {
	return len(b.work.Messages) >= domain.SecretaryMaxMessages ||
		b.removals >= domain.SecretaryMaxMessages
}

// A false result leaves the next visible message outside the checkpoint.
func (b *secretaryBatch) consume(
	change secretaryChange,
	doc domain.SearchDocument,
	readErr error,
) (bool, error) {
	switch {
	case errors.Is(readErr, ErrNotFound):
		if change.hash != "" {
			b.remove(change.id)
		}
	case readErr != nil:
		return false, readErr
	case doc.OwnerUserID != b.work.OwnerUserID:
		return false, ErrNotFound
	case doc.AgentID != b.work.SourceAgentID || doc.SessionID != b.work.SourceSessionID:
		b.remove(change.id)
	case doc.ContentHash != change.hash || doc.CanonicalizationVersion != change.canonical:
		text, _ := domain.SecretaryMessageView(doc.Body, doc.Role)
		if len(b.work.Messages) > 0 && (b.bytes+len(text) > domain.SecretaryMaxBytes ||
			b.sourceBytes+len(doc.Body) > domain.SecretaryMaxSourceBytes) {
			return false, nil
		}
		b.work.Messages = append(b.work.Messages, doc)
		b.bytes += len(text)
		b.sourceBytes += len(doc.Body)
	}
	b.work.ThroughRevision = change.revision
	return true, nil
}

func (b *secretaryBatch) remove(id string) {
	if !b.removed[id] {
		b.work.RemovedMessageIDs = append(b.work.RemovedMessageIDs, id)
		b.removed[id] = true
		b.removals++
	}
}
