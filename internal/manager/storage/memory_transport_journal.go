package storage

import (
	"context"
	"sort"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *MemoryStore) SaveTransportFrame(ctx context.Context, frame *TransportFrame) error {
	if err := validateTransportFrame(frame); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prepareTransportFrameLocked(frame)
	key := makeTransportFrameKey(frame.AgentID, frame.Stream, frame.Seq, frame.LocalDirection)
	if _, exists := s.transportJournal[key]; exists {
		return ErrConflict
	}
	s.transportJournal[key] = cloneTransportFrame(*frame)
	return nil
}

func (s *MemoryStore) SaveTransportFrameIfAbsent(
	ctx context.Context,
	frame *TransportFrame,
) (bool, error) {
	if err := validateTransportFrame(frame); err != nil {
		return false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := makeTransportFrameKey(frame.AgentID, frame.Stream, frame.Seq, frame.LocalDirection)
	if existing, exists := s.transportJournal[key]; exists {
		*frame = cloneTransportFrame(existing)
		return false, nil
	}
	s.prepareTransportFrameLocked(frame)
	s.transportJournal[key] = cloneTransportFrame(*frame)
	return true, nil
}

func (s *MemoryStore) NextTransportSeq(
	ctx context.Context,
	agentID string,
	stream string,
	direction string,
) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var maxSeq int64
	for key := range s.transportJournal {
		if key.AgentID == agentID &&
			key.Stream == stream &&
			key.LocalDirection == direction &&
			key.Seq > maxSeq {
			maxSeq = key.Seq
		}
	}
	return maxSeq + 1, nil
}

func (s *MemoryStore) GetTransportFrame(
	ctx context.Context,
	agentID string,
	stream string,
	seq int64,
	direction string,
) (*TransportFrame, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := makeTransportFrameKey(agentID, stream, seq, direction)
	frame, ok := s.transportJournal[key]
	if !ok {
		return nil, nil
	}
	cloned := cloneTransportFrame(frame)
	return &cloned, nil
}

func (s *MemoryStore) ListTransportFrames(
	ctx context.Context,
	agentID string,
	stream string,
	direction string,
	statuses []string,
	limit int,
) ([]TransportFrame, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	statusSet := make(map[string]bool, len(statuses))
	for _, status := range statuses {
		statusSet[status] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	frames := make([]TransportFrame, 0)
	for key, frame := range s.transportJournal {
		if key.AgentID != agentID || key.Stream != stream || key.LocalDirection != direction {
			continue
		}
		if len(statusSet) > 0 && !statusSet[frame.Status] {
			continue
		}
		frames = append(frames, cloneTransportFrame(frame))
	}
	sort.Slice(frames, func(i, j int) bool {
		return frames[i].Seq < frames[j].Seq
	})
	if len(frames) > limit {
		frames = frames[:limit]
	}
	return frames, nil
}

func (s *MemoryStore) UpdateTransportFrameStatus(
	ctx context.Context,
	agentID string,
	stream string,
	seq int64,
	direction string,
	status string,
	errMsg string,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := makeTransportFrameKey(agentID, stream, seq, direction)
	frame, ok := s.transportJournal[key]
	if !ok {
		return nil
	}
	now := s.now().UTC()
	frame.Status = status
	frame.Error = errMsg
	frame.UpdatedAt = now
	setTransportStatusTimestamp(&frame, status, now)
	s.transportJournal[key] = frame
	return nil
}

func (s *MemoryStore) AckOutboundTransportFrames(
	ctx context.Context,
	agentID string,
	stream string,
	throughSeq int64,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	for key, frame := range s.transportJournal {
		if key.AgentID != agentID ||
			key.Stream != stream ||
			key.LocalDirection != domain.TransportDirectionOutbound ||
			key.Seq > throughSeq ||
			frame.Status == domain.TransportStatusAcked {
			continue
		}
		frame.Status = domain.TransportStatusAcked
		frame.UpdatedAt = now
		frame.AckedAt = &now
		s.transportJournal[key] = frame
	}
	return nil
}

func (s *MemoryStore) DeleteCompletedTransportFrames(
	ctx context.Context,
	cutoff time.Time,
	limit int,
) (int64, error) {
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]transportFrameKey, 0)
	for key, frame := range s.transportJournal {
		if frame.UpdatedAt.Before(cutoff.UTC()) &&
			(frame.Status == domain.TransportStatusAcked ||
				frame.Status == domain.TransportStatusApplied) {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool {
		left := s.transportJournal[keys[i]]
		right := s.transportJournal[keys[j]]
		if left.ID == right.ID {
			return left.Seq < right.Seq
		}
		return left.ID < right.ID
	})
	if len(keys) > limit {
		keys = keys[:limit]
	}
	for _, key := range keys {
		delete(s.transportJournal, key)
	}
	return int64(len(keys)), nil
}

func (s *MemoryStore) prepareTransportFrameLocked(frame *TransportFrame) {
	now := s.now().UTC()
	if frame.ID == 0 {
		s.nextTransportID++
		frame.ID = s.nextTransportID
	}
	if frame.Status == "" {
		frame.Status = defaultTransportStatus(frame.LocalDirection)
	}
	if frame.CreatedAt.IsZero() {
		frame.CreatedAt = now
	}
	if frame.UpdatedAt.IsZero() {
		frame.UpdatedAt = now
	}
	setTransportStatusTimestamp(frame, frame.Status, now)
}

func makeTransportFrameKey(agentID, stream string, seq int64, direction string) transportFrameKey {
	return transportFrameKey{
		AgentID:        agentID,
		Stream:         stream,
		Seq:            seq,
		LocalDirection: direction,
	}
}

func setTransportStatusTimestamp(frame *TransportFrame, status string, ts time.Time) {
	switch status {
	case domain.TransportStatusSent:
		if frame.SentAt == nil {
			frame.SentAt = &ts
		}
	case domain.TransportStatusAcked:
		if frame.AckedAt == nil {
			frame.AckedAt = &ts
		}
	case domain.TransportStatusReceived:
		if frame.ReceivedAt == nil {
			frame.ReceivedAt = &ts
		}
	case domain.TransportStatusApplied:
		if frame.AppliedAt == nil {
			frame.AppliedAt = &ts
		}
	}
}

func cloneTransportFrame(frame TransportFrame) TransportFrame {
	frame.PayloadJSON = append([]byte(nil), frame.PayloadJSON...)
	frame.SentAt = cloneTimePtr(frame.SentAt)
	frame.ReceivedAt = cloneTimePtr(frame.ReceivedAt)
	frame.AckedAt = cloneTimePtr(frame.AckedAt)
	frame.AppliedAt = cloneTimePtr(frame.AppliedAt)
	return frame
}

func cloneTimePtr(ts *time.Time) *time.Time {
	if ts == nil {
		return nil
	}
	cloned := *ts
	return &cloned
}
