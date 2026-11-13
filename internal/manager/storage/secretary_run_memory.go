package storage

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

const secretaryRunCommandVersion = "pending-retry-v1"
const secretaryRunBatchLimit = 128

type memorySecretaryRunCommand struct {
	receipt         domain.SecretaryRunReceipt
	owner, version  string
	state           string
	upper, after    string
	settings, count int64
}

var _ domain.SecretaryRunStore = (*MemoryStore)(nil)
var _ domain.SecretaryRunQueueStore = (*MemoryStore)(nil)

func (s *MemoryStore) RequestSecretaryRun(
	ctx context.Context,
	owner, id string,
) (domain.SecretaryRunReceipt, error) {
	if err := secretaryRunRequestError(ctx, owner, id); err != nil {
		return domain.SecretaryRunReceipt{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.secretaryLocked()
	if state.runCommands == nil {
		state.runCommands = map[string]*memorySecretaryRunCommand{}
	}
	key := owner + "\x00" + id
	if current := state.runCommands[key]; current != nil {
		return current.receipt, nil
	}
	cfg := state.settings[owner]
	if !cfg.Enabled {
		return domain.SecretaryRunReceipt{}, ErrConflict
	}
	command := &memorySecretaryRunCommand{
		owner: owner, version: secretaryRunCommandVersion, state: "pending", settings: cfg.Revision,
		receipt: domain.SecretaryRunReceipt{
			RequestID:  id,
			State:      "accepted",
			AcceptedAt: s.now().UTC(),
		},
	}
	for source := range state.jobs {
		if secretaryRunSourceLess(command.upper, source) {
			command.upper = source
		}
	}
	state.runCommands[key] = command
	return command.receipt, nil
}

func secretaryRunRequestError(ctx context.Context, owner, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if owner == "" {
		return ErrUnauthorized
	}
	if !domain.ValidSecretaryRunRequestID(id) {
		return ErrConflict
	}
	return nil
}

func secretaryRunLimit(limit int) int {
	if limit <= 0 || limit > secretaryRunBatchLimit {
		return secretaryRunBatchLimit
	}
	return limit
}

func secretaryRunSourceLess(a, b string) bool {
	ai, ae := strconv.ParseInt(a, 10, 64)
	bi, be := strconv.ParseInt(b, 10, 64)
	if ae == nil && be == nil {
		return ai < bi
	}
	return a < b
}

func (s *MemoryStore) AdvanceSecretaryRuns(ctx context.Context, limit int) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.secretaryLocked()
	var pending []*memorySecretaryRunCommand
	for _, command := range state.runCommands {
		if command.state == "pending" {
			pending = append(pending, command)
		}
	}
	sort.Slice(pending, func(i, j int) bool {
		a, b := pending[i], pending[j]
		if !a.receipt.AcceptedAt.Equal(b.receipt.AcceptedAt) {
			return a.receipt.AcceptedAt.Before(b.receipt.AcceptedAt)
		}
		return a.owner+"\x00"+a.receipt.RequestID < b.owner+"\x00"+b.receipt.RequestID
	})
	if len(pending) == 0 {
		return false, nil
	}
	command := pending[0]
	cfg := state.settings[command.owner]
	if !cfg.Enabled || cfg.Revision != command.settings ||
		command.version != secretaryRunCommandVersion {
		command.state = "canceled"
		return true, nil
	}
	var ids []string
	for id, job := range state.jobs {
		if job.work.OwnerUserID == command.owner && secretaryRunSourceLess(command.after, id) &&
			!secretaryRunSourceLess(command.upper, id) {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return secretaryRunSourceLess(ids[i], ids[j]) })
	limit = secretaryRunLimit(limit)
	if len(ids) > limit {
		ids = ids[:limit]
	}
	for _, id := range ids {
		job := state.jobs[id]
		if s.secretaryRunEligibleLocked(command.owner, id, job) {
			s.retryMemoryOperationLocked(id)
			job.available, job.updated, job.lastError = s.now(), s.now().Add(-30*time.Second), ""
			job.executionBlocked, job.autoRetryKey, job.autoRetryCount = false, "", 0
			job.work.ValidationFeedback = ""
			job.work.RetryEpoch++
			job.work.RetryGeneration++
			command.count++
		}
		command.after = id
	}
	if len(ids) < limit || command.after == command.upper {
		command.state = "completed"
	}
	return true, nil
}

func (s *MemoryStore) secretaryRunEligibleLocked(owner, id string, job *memorySecretaryJob) bool {
	_, session, err := s.secretaryTaskSourceLocked(owner, id)
	if err != nil || job.paused ||
		(job.desired <= job.processed && !s.memoryOperationNeedsAttentionLocked(id)) ||
		strings.HasPrefix(session.SessionID, domain.SecretaryInternalSessionPrefix) ||
		job.work.LeaseToken != "" && job.leaseUntil.After(s.now()) {
		return false
	}
	for _, batch := range s.secretaryLocked().executions {
		if batch.owner == owner && batch.source == id && secretaryRunUnresolved(batch.state) {
			return false
		}
	}
	return true
}

func secretaryRunUnresolved(state string) bool {
	return state == "pending" || state == "running" || state == "output_received" ||
		state == "unknown"
}
