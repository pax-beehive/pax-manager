package storage

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

// MemoryRuntimeConfig is the in-process repository used without DATABASE_URL.
type MemoryRuntimeConfig struct {
	mu       sync.Mutex
	now      func() time.Time
	revision int64
	rows     map[string]domain.RuntimeConfigRow
	audit    []domain.RuntimeConfigAudit
}

func NewMemoryRuntimeConfig(now func() time.Time) *MemoryRuntimeConfig {
	if now == nil {
		now = time.Now
	}
	return &MemoryRuntimeConfig{now: now, rows: map[string]domain.RuntimeConfigRow{}}
}

// RuntimeConfig exposes the repository for the configuration center.
func (s *MemoryStore) RuntimeConfig() domain.RuntimeConfigRepository {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runtimeConfig == nil {
		s.runtimeConfig = NewMemoryRuntimeConfig(s.now)
	}
	return s.runtimeConfig
}

func (m *MemoryRuntimeConfig) RuntimeConfigRevision(context.Context) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.revision, nil
}

func (m *MemoryRuntimeConfig) LoadRuntimeConfig(
	context.Context,
) (int64, []domain.RuntimeConfigRow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rows := make([]domain.RuntimeConfigRow, 0, len(m.rows))
	for _, row := range m.rows {
		rows = append(rows, cloneRuntimeConfigRow(row))
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Key < rows[j].Key })
	return m.revision, rows, nil
}

func (m *MemoryRuntimeConfig) WriteRuntimeConfig(
	_ context.Context,
	request domain.RuntimeConfigWrite,
) (domain.RuntimeConfigWriteResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	current, present := m.rows[request.Key]
	if current.Version != request.ExpectedVersion {
		return domain.RuntimeConfigWriteResult{}, &domain.RuntimeConfigConflictError{
			Current: current.Version,
		}
	}
	if !present && !request.New.Present {
		return domain.RuntimeConfigWriteResult{}, nil
	}
	next := current.Version
	for _, record := range m.audit {
		if record.Key == request.Key && record.Version > next {
			next = record.Version
		}
	}
	next++
	now := m.now().UTC()
	m.revision++
	result := domain.RuntimeConfigWriteResult{Revision: m.revision}
	auditVersion := int64(0)
	if request.New.Present {
		row := domain.RuntimeConfigRow{
			Key: request.Key, ValueJSON: request.New.ValueJSON, Version: next,
			UpdatedBy: request.Actor, UpdatedAt: now,
		}
		m.rows[request.Key] = cloneRuntimeConfigRow(row)
		result.Row, result.Present, auditVersion = row, true, next
	} else {
		delete(m.rows, request.Key)
	}
	var oldDisplay *string
	if present && request.DisplayOld != nil {
		oldDisplay = request.DisplayOld(current)
	}
	m.audit = append(m.audit, domain.RuntimeConfigAudit{
		ID: int64(len(m.audit) + 1), Key: request.Key, Action: request.Action,
		OldDisplay: oldDisplay, NewDisplay: request.NewDisplay,
		Old:     domain.RuntimeConfigValue{Present: present, ValueJSON: current.ValueJSON},
		Version: auditVersion, Actor: request.Actor, Reason: request.Reason, CreatedAt: now,
	})
	return result, nil
}

func (m *MemoryRuntimeConfig) ListRuntimeConfigAudit(
	_ context.Context,
	query domain.RuntimeConfigAuditQuery,
) ([]domain.RuntimeConfigAudit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []domain.RuntimeConfigAudit
	for i := len(m.audit) - 1; i >= 0 && len(out) < query.Limit; i-- {
		record := m.audit[i]
		if (query.Key != "" && record.Key != query.Key) ||
			(query.BeforeID != 0 && record.ID >= query.BeforeID) {
			continue
		}
		out = append(out, record)
	}
	return out, nil
}

func (m *MemoryRuntimeConfig) GetRuntimeConfigAudit(
	_ context.Context,
	id int64,
) (domain.RuntimeConfigAudit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if id <= 0 || id > int64(len(m.audit)) {
		return domain.RuntimeConfigAudit{}, domain.ErrRuntimeConfigAuditNotFound
	}
	return m.audit[id-1], nil
}

func cloneRuntimeConfigRow(row domain.RuntimeConfigRow) domain.RuntimeConfigRow {
	row.ValueJSON = append([]byte(nil), row.ValueJSON...)
	return row
}
