package storage

import (
	"context"
	"sort"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

var _ domain.SecretaryStore = (*MemoryStore)(nil)

type memorySecretaryState struct {
	settings  map[string]domain.SecretarySettings
	jobs      map[string]*memorySecretaryJob
	internal  map[string]bool
	summaries map[string][]domain.SummaryVersion
	snapshots map[string]domain.SearchDocument
	requests  map[string]memorySecretaryRequest
}
type memorySecretaryRequest struct {
	hash string
	ref  domain.SummaryRef
}
type memorySecretaryJob struct {
	work                           domain.SecretaryJob
	desired, processed             int64
	changes                        map[string]int64
	seen, checkpoint               map[string]string
	updated, available, leaseUntil time.Time
	completed                      *time.Time
	lastError                      string
	resetRevision                  int64
}

func (s *MemoryStore) secretaryLocked() *memorySecretaryState {
	if s.secretaryState == nil {
		s.secretaryState = &memorySecretaryState{
			settings:  map[string]domain.SecretarySettings{},
			jobs:      map[string]*memorySecretaryJob{},
			internal:  map[string]bool{},
			summaries: map[string][]domain.SummaryVersion{},
			snapshots: map[string]domain.SearchDocument{},
			requests:  map[string]memorySecretaryRequest{},
		}
	}
	return s.secretaryState
}

func (s *MemoryStore) GetSecretarySettings(
	_ context.Context,
	owner string,
) (domain.SecretarySettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if owner == "" {
		return domain.SecretarySettings{}, ErrUnauthorized
	}
	result := s.secretaryLocked().settings[owner]
	result.OwnerUserID = owner
	return result, nil
}

func (s *MemoryStore) secretaryAgentOwnedLocked(owner, id string) bool {
	agent, ok := s.agents[id]
	return ok && agent.OwnerUserID == owner && s.searchNodesVisibleLocked(agent.NodeID)
}

func (s *MemoryStore) UpdateSecretarySettings(
	_ context.Context,
	owner string,
	update domain.SecretarySettingsUpdate,
) (domain.SecretarySettings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if owner == "" {
		return domain.SecretarySettings{}, ErrUnauthorized
	}
	state := s.secretaryLocked()
	old := state.settings[owner]
	if update.ExpectedRevision != old.Revision || update.ExpectedRevision < 0 ||
		update.Enabled && update.AgentID == "" {
		return domain.SecretarySettings{}, ErrConflict
	}
	if update.AgentID != "" && (update.Enabled || update.AgentID != old.AgentID) &&
		!s.secretaryAgentOwnedLocked(owner, update.AgentID) {
		return domain.SecretarySettings{}, ErrNotFound
	}
	result := domain.SecretarySettings{
		OwnerUserID: owner,
		AgentID:     update.AgentID,
		Enabled:     update.Enabled,
		Revision:    old.Revision + 1,
		UpdatedAt:   s.now().UTC(),
	}
	state.settings[owner] = result
	for _, job := range state.jobs {
		if job.work.OwnerUserID == owner {
			job.work.LeaseToken = ""
			job.leaseUntil = time.Time{}
		}
	}
	if update.Enabled {
		s.refreshSecretaryOwnerLocked(owner)
	}
	return result, nil
}

func (s *MemoryStore) GetSecretaryStatus(
	_ context.Context,
	owner string,
) (domain.SecretaryStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if owner == "" {
		return domain.SecretaryStatus{}, ErrUnauthorized
	}
	state := s.secretaryLocked()
	cfg := state.settings[owner]
	cfg.OwnerUserID = owner
	result := domain.SecretaryStatus{SecretarySettings: cfg}
	var latest time.Time
	for _, job := range state.jobs {
		if job.work.OwnerUserID != owner {
			continue
		}
		if job.desired > job.processed {
			result.PendingCount++
		}
		result.Processing = result.Processing ||
			job.work.LeaseToken != "" && job.leaseUntil.After(s.now())
		if job.completed != nil &&
			(result.LastCompletedAt == nil || job.completed.After(*result.LastCompletedAt)) {
			value := *job.completed
			result.LastCompletedAt = &value
		}
		if job.lastError != "" && job.updated.After(latest) {
			result.LastError = job.lastError
			latest = job.updated
		}
	}
	return result, nil
}

func (s *MemoryStore) BackfillSecretaryJobs(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshSecretaryLocked()
	return nil
}

func (s *MemoryStore) RetrySecretaryJobs(_ context.Context, owner string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if owner == "" {
		return ErrUnauthorized
	}
	state := s.secretaryLocked()
	if !state.settings[owner].Enabled {
		return ErrConflict
	}
	s.refreshSecretaryOwnerLocked(owner)
	for _, job := range state.jobs {
		if job.work.OwnerUserID == owner {
			job.available = s.now()
			job.updated = s.now().Add(-30 * time.Second)
			job.lastError = ""
			if job.work.LeaseToken == "" || !job.leaseUntil.After(s.now()) {
				job.work.RetryGeneration++
			}
		}
	}
	return nil
}

func (s *MemoryStore) refreshSecretaryLocked() {
	s.refreshSecretaryOwnerLocked("")
}

func (s *MemoryStore) refreshSecretaryOwnerLocked(owner string) {
	state := s.secretaryLocked()
	for _, session := range s.sessions {
		agent := s.agents[session.AgentID]
		if owner != "" && agent.OwnerUserID != owner {
			continue
		}
		cfg := state.settings[agent.OwnerUserID]
		if !cfg.Enabled || session.ArchivedAt != nil ||
			session.Source == domain.SecretaryInternalSource ||
			state.internal[sessionKey(session.AgentID, session.SessionID)] ||
			!s.secretaryAgentOwnedLocked(agent.OwnerUserID, session.AgentID) {
			continue
		}
		id := secretarySourceID(session.ID)
		if session.ID == 0 {
			id = "memory:" + domain.SearchContentHash(
				sessionKey(session.AgentID, session.SessionID),
			)
		}
		job := state.jobs[id]
		if job == nil {
			job = &memorySecretaryJob{
				work: domain.SecretaryJob{
					SourceID:        id,
					OwnerUserID:     agent.OwnerUserID,
					SourceAgentID:   session.AgentID,
					SourceSessionID: session.SessionID,
				},
				changes:    map[string]int64{},
				seen:       map[string]string{},
				checkpoint: map[string]string{},
			}
			state.jobs[id] = job
		}
		if job.work.OwnerUserID != agent.OwnerUserID {
			job.work.OwnerUserID = agent.OwnerUserID
			job.work.LeaseToken = ""
			job.leaseUntil = time.Time{}
			job.checkpoint = map[string]string{}
			job.seen = map[string]string{}
		}
		s.refreshSecretaryMessagesLocked(job)
		if job.processed > 0 && job.desired == job.processed && job.resetRevision == 0 &&
			len(state.summaries[secretarySummaryID(agent.OwnerUserID, id)]) == 0 {
			job.desired++
			job.available = s.now()
		}
	}
}

func (s *MemoryStore) refreshSecretaryMessagesLocked(job *memorySecretaryJob) {
	seen := map[string]bool{}
	var ids []string
	for id, message := range s.messages {
		if message.AgentID != job.work.SourceAgentID ||
			message.SessionID != job.work.SourceSessionID {
			continue
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := s.messages[ids[i]], s.messages[ids[j]]
		if a.SessionSeq == b.SessionSeq {
			return ids[i] < ids[j]
		}
		return a.SessionSeq < b.SessionSeq
	})
	for _, id := range ids {
		seen[id] = true
		hash := "hidden"
		if doc, err := s.readSearchMessageLocked(job.work.OwnerUserID, id); err == nil {
			hash = doc.ContentHash
		}
		if job.seen[id] != hash {
			s.markSecretaryMemoryDirtyLocked(job, id, hash)
		}
	}
	for id, hash := range job.seen {
		if !seen[id] && hash != "hidden" {
			s.markSecretaryMemoryDirtyLocked(job, id, "hidden")
		}
	}
}

func (s *MemoryStore) markSecretaryMemoryDirtyLocked(job *memorySecretaryJob, id, hash string) {
	job.desired++
	job.changes[id] = job.desired
	job.seen[id] = hash
	job.updated = s.now()
	job.available = s.now()
}

func (s *MemoryStore) SecretaryJobActive(
	_ context.Context,
	work domain.SecretaryJob,
) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.secretaryJobActiveLocked(work), nil
}

func (s *MemoryStore) secretaryJobActiveLocked(work domain.SecretaryJob) bool {
	state := s.secretaryLocked()
	cfg := state.settings[work.OwnerUserID]
	job := state.jobs[work.SourceID]
	if job == nil || work.LeaseToken == "" || job.work.LeaseToken != work.LeaseToken ||
		!job.leaseUntil.After(s.now()) ||
		!cfg.Enabled ||
		cfg.Revision != work.SettingsRevision ||
		cfg.AgentID != work.AgentID {
		return false
	}
	session, ok := s.sessions[sessionKey(work.SourceAgentID, work.SourceSessionID)]
	if !ok || session.ArchivedAt != nil || session.Source == domain.SecretaryInternalSource ||
		state.internal[sessionKey(work.SourceAgentID, work.SourceSessionID)] {
		return false
	}
	return s.searchNodesVisibleLocked(session.NodeID) &&
		s.secretaryAgentOwnedLocked(work.OwnerUserID, work.AgentID) &&
		s.secretaryAgentOwnedLocked(work.OwnerUserID, work.SourceAgentID)
}

func (s *MemoryStore) RegisterSecretaryInternalSession(
	_ context.Context,
	work domain.SecretaryJob,
	agentID, sessionID string,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if agentID != work.AgentID || sessionID == "" {
		return ErrConflict
	}
	if !s.secretaryJobActiveLocked(work) {
		return domain.ErrSecretaryFenced
	}
	s.secretaryLocked().internal[sessionKey(agentID, sessionID)] = true
	if session, ok := s.sessions[sessionKey(agentID, sessionID)]; ok {
		session.Source = domain.SecretaryInternalSource
		s.sessions[sessionKey(agentID, sessionID)] = session
	}
	return nil
}

func (s *MemoryStore) FailSecretaryJob(
	_ context.Context,
	work domain.SecretaryJob,
	failure error,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	job := s.secretaryLocked().jobs[work.SourceID]
	if job != nil && job.work.LeaseToken == work.LeaseToken {
		job.work.LeaseToken = ""
		job.leaseUntil = time.Time{}
		job.available = s.now().Add(5 * time.Second)
		job.lastError = secretaryErrorText(failure)
	}
	return nil
}

func (s *MemoryStore) ClaimSecretaryJobs(
	_ context.Context,
	limit int,
	lease, settle time.Duration,
) ([]domain.SecretaryJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshSecretaryLocked()
	if limit <= 0 || limit > 10 {
		limit = 1
	}
	if lease <= 0 || lease > 30*time.Minute {
		lease = 10 * time.Minute
	}
	if settle < 0 {
		settle = 30 * time.Second
	}
	state := s.secretaryLocked()
	var ids []string
	for id := range state.jobs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var result []domain.SecretaryJob
	for _, id := range ids {
		if len(result) >= limit {
			break
		}
		job := state.jobs[id]
		cfg := state.settings[job.work.OwnerUserID]
		if !s.memorySecretaryClaimableLocked(job, settle) {
			continue
		}
		work, err := s.loadMemorySecretaryJobLocked(job)
		if err != nil {
			job.lastError = secretaryErrorText(err)
			job.available = s.now().Add(30 * time.Second)
			continue
		}
		if len(work.Messages) == 0 && len(work.RemovedMessageIDs) == 0 && !work.ResetRequired {
			job.processed = work.ThroughRevision
			continue
		}
		work.AgentID = cfg.AgentID
		work.RetryGeneration = job.work.RetryGeneration
		work.SettingsRevision = cfg.Revision
		work.LeaseToken, err = newSecret("secretary")
		if err != nil {
			return nil, err
		}
		secretaryPrepareJob(&work)
		job.work = work
		job.leaseUntil = s.now().Add(lease)
		result = append(result, work)
	}
	return result, nil
}

func (s *MemoryStore) memorySecretaryClaimableLocked(
	job *memorySecretaryJob,
	settle time.Duration,
) bool {
	state := s.secretaryLocked()
	cfg := state.settings[job.work.OwnerUserID]
	session, ok := s.sessions[sessionKey(job.work.SourceAgentID, job.work.SourceSessionID)]
	if !ok || !cfg.Enabled || job.desired <= job.processed || job.available.After(s.now()) ||
		job.leaseUntil.After(s.now()) ||
		job.updated.Add(settle).After(s.now()) {
		return false
	}
	if session.ArchivedAt != nil ||
		state.internal[sessionKey(session.AgentID, session.SessionID)] ||
		session.Source == domain.SecretaryInternalSource {
		return false
	}
	if session.RuntimeStatus != "" && session.RuntimeStatus != domain.RuntimeStatusIdle {
		return false
	}
	return s.secretaryAgentOwnedLocked(job.work.OwnerUserID, cfg.AgentID) &&
		s.secretaryAgentOwnedLocked(job.work.OwnerUserID, job.work.SourceAgentID)
}
