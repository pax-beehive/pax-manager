package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/pax-beehive/paxkit/reliablemq"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func (s *MemoryStore) AppendOutboundData(
	ctx context.Context,
	queueID string,
	stream reliablemq.Stream,
	payload json.RawMessage,
	metadata reliablemq.Metadata,
) (reliablemq.Frame, error) {
	_ = ctx
	if !json.Valid(payload) {
		return reliablemq.Frame{}, fmt.Errorf(
			"%w: payload must be valid JSON",
			reliablemq.ErrInvalidFrame,
		)
	}
	return s.appendReliableOutbound(
		queueID,
		stream,
		reliablemq.FrameKindData,
		payload,
		"",
		metadata,
	)
}

func (s *MemoryStore) AppendOutboundTombstone(
	ctx context.Context,
	queueID string,
	stream reliablemq.Stream,
	errorMessage string,
	metadata reliablemq.Metadata,
) (reliablemq.Frame, error) {
	_ = ctx
	return s.appendReliableOutbound(
		queueID,
		stream,
		reliablemq.FrameKindTombstone,
		nil,
		errorMessage,
		metadata,
	)
}

func (s *MemoryStore) SaveInboundIfAbsent(
	ctx context.Context,
	frame reliablemq.Frame,
) (bool, reliablemq.Frame, error) {
	_ = ctx
	if err := reliablemq.ValidateFrame(frame); err != nil {
		return false, reliablemq.Frame{}, err
	}
	if frame.Key.Direction != reliablemq.DirectionInbound {
		return false, reliablemq.Frame{}, fmt.Errorf(
			"%w: inbound frame direction is %q",
			reliablemq.ErrInvalidFrame,
			frame.Key.Direction,
		)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	applied := s.inboundAppliedThroughLocked(frame.Key.QueueID, frame.Key.Stream)
	if frame.Key.Seq <= applied {
		frame = frame.Clone()
		frame.Status = reliablemq.StatusApplied
		return false, frame, nil
	}
	key := makeReliableTransportFrameKey(frame.Key)
	if existing, ok := s.transportJournal[key]; ok {
		return false, reliableFromTransportFrame(existing), nil
	}
	now := s.now().UTC()
	frame = frame.Clone()
	frame.Status = reliablemq.StatusReceived
	frame.CreatedAt = now
	frame.UpdatedAt = now
	s.nextTransportID++
	compat := transportFrameFromReliable(frame, frame.Metadata["agent_id"])
	compat.ID = s.nextTransportID
	compat.ReceivedAt = &now
	s.transportJournal[key] = compat
	return true, frame, nil
}

func (s *MemoryStore) ListOutboundReplay(
	ctx context.Context,
	queueID string,
	stream reliablemq.Stream,
	limit int,
) ([]reliablemq.Frame, error) {
	return s.listReliableFrames(
		ctx,
		queueID,
		stream,
		reliablemq.DirectionOutbound,
		[]reliablemq.Status{
			reliablemq.StatusPending,
			reliablemq.StatusSent,
		},
		limit,
	)
}

func (s *MemoryStore) ListInboundReplay(
	ctx context.Context,
	queueID string,
	stream reliablemq.Stream,
	limit int,
) ([]reliablemq.Frame, error) {
	return s.listReliableFrames(
		ctx,
		queueID,
		stream,
		reliablemq.DirectionInbound,
		[]reliablemq.Status{
			reliablemq.StatusReceived,
		},
		limit,
	)
}

func (s *MemoryStore) LoadQueueState(
	ctx context.Context,
	queueID string,
	stream reliablemq.Stream,
) (reliablemq.QueueState, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	key := transportQueueStateKey{QueueID: queueID, Stream: string(stream)}
	state := s.transportQueueState[key]
	if state.NextOutboundSeq <= 0 {
		state.NextOutboundSeq = s.nextOutboundSeqFromJournalLocked(queueID, stream)
	}
	return reliablemq.QueueState{
		NextOutboundSeq:       state.NextOutboundSeq,
		InboundAppliedThrough: state.InboundAppliedThrough,
	}, nil
}

func (s *MemoryStore) ConsumerAckedThrough(
	ctx context.Context,
	queueID string,
	stream reliablemq.Stream,
) (int64, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	var through int64
	next := int64(1)
	for {
		frame, ok := s.transportJournal[transportFrameKey{
			QueueID:   queueID,
			Stream:    string(stream),
			Seq:       next,
			Direction: string(reliablemq.DirectionInbound),
		}]
		if !ok || !isReliableConsumerAckedStatus(frame.Status) {
			break
		}
		through = next
		next++
	}
	return through, nil
}

func (s *MemoryStore) ApplyBatch(ctx context.Context, batch reliablemq.StoreBatch) error {
	_ = ctx
	for _, frame := range batch.Frames {
		if err := s.applyReliableBatchFrame(frame); err != nil {
			return err
		}
	}
	for _, patch := range batch.Patches {
		if patch.HasMetadata {
			if err := s.UpdateMetadata(ctx, patch.Key, patch.Metadata); err != nil {
				return err
			}
		}
		switch patch.Status {
		case "":
		case reliablemq.StatusSent:
			if err := s.MarkSent(ctx, patch.Key); err != nil {
				return err
			}
		case reliablemq.StatusAcked:
			if err := s.AckOutboundThrough(ctx, patch.Key.QueueID, patch.Key.Stream, patch.Key.Seq); err != nil {
				return err
			}
		case reliablemq.StatusApplied:
			if err := s.MarkApplied(ctx, patch.Key); err != nil {
				return err
			}
		case reliablemq.StatusRejected:
			if err := s.MarkRejected(ctx, patch.Key, patch.ErrorMessage); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: invalid status %q", reliablemq.ErrInvalidFrame, patch.Status)
		}
		if patch.ErrorMessage != "" && patch.Status != reliablemq.StatusRejected {
			if patch.Key.Direction == reliablemq.DirectionOutbound {
				if err := s.RecordSendFailure(ctx, patch.Key, patch.ErrorMessage); err != nil {
					return err
				}
			} else if err := s.RecordDispatchFailure(ctx, patch.Key, patch.ErrorMessage); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *MemoryStore) MarkSent(ctx context.Context, key reliablemq.FrameKey) error {
	_ = ctx
	return s.updateReliableStatus(key, reliablemq.StatusSent, "")
}

func (s *MemoryStore) AckOutboundThrough(
	ctx context.Context,
	queueID string,
	stream reliablemq.Stream,
	throughSeq int64,
) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	for key, frame := range s.transportJournal {
		if key.QueueID != queueID ||
			key.Stream != string(stream) ||
			key.Direction != string(reliablemq.DirectionOutbound) ||
			key.Seq > throughSeq ||
			frame.Status == string(reliablemq.StatusAcked) {
			continue
		}
		frame.Status = string(reliablemq.StatusAcked)
		frame.Error = ""
		frame.ErrorMessage = ""
		frame.UpdatedAt = now
		frame.AckedAt = &now
		s.transportJournal[key] = frame
	}
	return nil
}

func (s *MemoryStore) MarkApplied(ctx context.Context, key reliablemq.FrameKey) error {
	_ = ctx
	return s.updateReliableStatus(key, reliablemq.StatusApplied, "")
}

func (s *MemoryStore) MarkRejected(
	ctx context.Context,
	key reliablemq.FrameKey,
	errorMessage string,
) error {
	_ = ctx
	return s.updateReliableStatus(key, reliablemq.StatusRejected, errorMessage)
}

func (s *MemoryStore) RecordSendFailure(
	ctx context.Context,
	key reliablemq.FrameKey,
	errorMessage string,
) error {
	_ = ctx
	return s.updateReliableError(key, errorMessage)
}

func (s *MemoryStore) RecordDispatchFailure(
	ctx context.Context,
	key reliablemq.FrameKey,
	errorMessage string,
) error {
	_ = ctx
	return s.updateReliableError(key, errorMessage)
}

func (s *MemoryStore) UpdateMetadata(
	ctx context.Context,
	key reliablemq.FrameKey,
	metadata reliablemq.Metadata,
) error {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	mapKey := makeReliableTransportFrameKey(key)
	frame, ok := s.transportJournal[mapKey]
	if !ok {
		return nil
	}
	frame.Metadata = map[string]string(metadata.Clone())
	if frame.Metadata["agent_id"] != "" {
		frame.AgentID = frame.Metadata["agent_id"]
	}
	frame.UpdatedAt = s.now().UTC()
	s.transportJournal[mapKey] = frame
	return nil
}

func (s *MemoryStore) SaveTransportFrame(ctx context.Context, frame *TransportFrame) error {
	_ = ctx
	reliableFrame, err := transportFrameToReliable(frame)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := makeReliableTransportFrameKey(reliableFrame.Key)
	if _, exists := s.transportJournal[key]; exists {
		return ErrConflict
	}
	s.prepareReliableFrameLocked(&reliableFrame)
	compat := transportFrameFromReliable(reliableFrame, frame.AgentID)
	compat.ID = s.nextTransportID
	setTransportStatusTimestamp(&compat, compat.Status, reliableFrame.UpdatedAt)
	s.transportJournal[key] = cloneTransportFrame(compat)
	*frame = compat
	return nil
}

func (s *MemoryStore) SaveTransportFrameIfAbsent(
	ctx context.Context,
	frame *TransportFrame,
) (bool, error) {
	reliableFrame, err := transportFrameToReliable(frame)
	if err != nil {
		return false, err
	}
	inserted, stored, err := s.SaveInboundIfAbsent(ctx, reliableFrame)
	if err != nil {
		return false, err
	}
	*frame = transportFrameFromReliable(stored, frame.AgentID)
	return inserted, nil
}

func (s *MemoryStore) NextTransportSeq(
	ctx context.Context,
	agentID string,
	stream string,
	direction string,
) (int64, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nextReliableSeqLocked(
		agentID,
		normalizeReliableStream(stream),
		reliablemq.Direction(direction),
	), nil
}

func (s *MemoryStore) GetTransportFrame(
	ctx context.Context,
	agentID string,
	stream string,
	seq int64,
	direction string,
) (*TransportFrame, error) {
	_ = ctx
	s.mu.Lock()
	defer s.mu.Unlock()
	key := transportFrameKey{
		QueueID:   agentID,
		Stream:    string(normalizeReliableStream(stream)),
		Seq:       seq,
		Direction: direction,
	}
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
	reliableStatuses := make([]reliablemq.Status, 0, len(statuses))
	for _, status := range statuses {
		reliableStatuses = append(reliableStatuses, reliablemq.Status(status))
	}
	frames, err := s.listReliableFrames(
		ctx,
		agentID,
		normalizeReliableStream(stream),
		reliablemq.Direction(direction),
		reliableStatuses,
		limit,
	)
	if err != nil {
		return nil, err
	}
	compat := make([]TransportFrame, 0, len(frames))
	for _, frame := range frames {
		compat = append(compat, transportFrameFromReliable(frame, frame.Metadata["agent_id"]))
	}
	return compat, nil
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
	_ = ctx
	return s.updateReliableStatus(reliablemq.FrameKey{
		QueueID:   agentID,
		Stream:    normalizeReliableStream(stream),
		Seq:       seq,
		Direction: reliablemq.Direction(direction),
	}, reliablemq.Status(status), errMsg)
}

func (s *MemoryStore) AckOutboundTransportFrames(
	ctx context.Context,
	agentID string,
	stream string,
	throughSeq int64,
) error {
	return s.AckOutboundThrough(ctx, agentID, normalizeReliableStream(stream), throughSeq)
}

func (s *MemoryStore) DeleteCompletedTransportFrames(
	ctx context.Context,
	cutoff time.Time,
	limit int,
) (int64, error) {
	_ = ctx
	if limit <= 0 || limit > 10000 {
		limit = 1000
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	keys := make([]transportFrameKey, 0)
	for key, frame := range s.transportJournal {
		if frame.UpdatedAt.Before(cutoff.UTC()) &&
			(frame.Status == string(reliablemq.StatusAcked) ||
				frame.Status == string(reliablemq.StatusApplied)) {
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

func (s *MemoryStore) appendReliableOutbound(
	queueID string,
	stream reliablemq.Stream,
	kind reliablemq.FrameKind,
	payload json.RawMessage,
	errorMessage string,
	metadata reliablemq.Metadata,
) (reliablemq.Frame, error) {
	if queueID == "" {
		return reliablemq.Frame{}, fmt.Errorf(
			"%w: queue_id is required",
			reliablemq.ErrInvalidFrame,
		)
	}
	if stream == "" {
		return reliablemq.Frame{}, fmt.Errorf("%w: stream is required", reliablemq.ErrInvalidFrame)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	frame := reliablemq.Frame{
		Key: reliablemq.FrameKey{
			QueueID:   queueID,
			Stream:    stream,
			Seq:       s.allocateReliableOutboundSeqLocked(queueID, stream),
			Direction: reliablemq.DirectionOutbound,
		},
		Kind:         kind,
		Payload:      append(json.RawMessage(nil), payload...),
		Metadata:     metadata.Clone(),
		Status:       reliablemq.StatusPending,
		ErrorMessage: errorMessage,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err := reliablemq.ValidateFrame(frame); err != nil {
		return reliablemq.Frame{}, err
	}
	s.nextTransportID++
	compat := transportFrameFromReliable(frame, frame.Metadata["agent_id"])
	compat.ID = s.nextTransportID
	s.transportJournal[makeReliableTransportFrameKey(frame.Key)] = compat
	return frame, nil
}

func (s *MemoryStore) listReliableFrames(
	ctx context.Context,
	queueID string,
	stream reliablemq.Stream,
	direction reliablemq.Direction,
	statuses []reliablemq.Status,
	limit int,
) ([]reliablemq.Frame, error) {
	_ = ctx
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	statusSet := make(map[string]bool, len(statuses))
	for _, status := range statuses {
		statusSet[string(status)] = true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	frames := make([]reliablemq.Frame, 0)
	for key, frame := range s.transportJournal {
		if key.QueueID != queueID || key.Stream != string(stream) ||
			key.Direction != string(direction) {
			continue
		}
		if len(statusSet) > 0 && !statusSet[frame.Status] {
			continue
		}
		frames = append(frames, reliableFromTransportFrame(frame))
	}
	sort.Slice(frames, func(i, j int) bool {
		return frames[i].Key.Seq < frames[j].Key.Seq
	})
	if len(frames) > limit {
		frames = frames[:limit]
	}
	return frames, nil
}

func (s *MemoryStore) updateReliableStatus(
	key reliablemq.FrameKey,
	status reliablemq.Status,
	errMsg string,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	mapKey := makeReliableTransportFrameKey(key)
	frame, ok := s.transportJournal[mapKey]
	if !ok {
		return nil
	}
	now := s.now().UTC()
	frame.Status = string(status)
	frame.Error = errMsg
	frame.ErrorMessage = errMsg
	frame.UpdatedAt = now
	setTransportStatusTimestamp(&frame, string(status), now)
	s.transportJournal[mapKey] = frame
	if (status == reliablemq.StatusApplied || status == reliablemq.StatusRejected) &&
		key.Direction == reliablemq.DirectionInbound {
		s.updateInboundAppliedThroughLocked(key.QueueID, key.Stream, key.Seq, now)
	}
	return nil
}

func (s *MemoryStore) applyReliableBatchFrame(frame reliablemq.Frame) error {
	if err := reliablemq.ValidateFrame(frame); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	frame = frame.Clone()
	if frame.CreatedAt.IsZero() {
		frame.CreatedAt = now
	}
	if frame.UpdatedAt.IsZero() {
		frame.UpdatedAt = frame.CreatedAt
	}
	if frame.Key.Direction == reliablemq.DirectionOutbound {
		s.updateNextOutboundSeqAtLeastLocked(frame.Key.QueueID, frame.Key.Stream, frame.Key.Seq+1, now)
	}
	key := makeReliableTransportFrameKey(frame.Key)
	compat := transportFrameFromReliable(frame, frame.Metadata["agent_id"])
	if existing, ok := s.transportJournal[key]; ok {
		compat.ID = existing.ID
		if compat.CreatedAt.IsZero() {
			compat.CreatedAt = existing.CreatedAt
		}
	} else {
		s.nextTransportID++
		compat.ID = s.nextTransportID
	}
	setTransportStatusTimestamp(&compat, compat.Status, frame.UpdatedAt)
	s.transportJournal[key] = cloneTransportFrame(compat)
	if frame.Key.Direction == reliablemq.DirectionInbound && frame.Status == reliablemq.StatusApplied {
		s.updateInboundAppliedThroughLocked(frame.Key.QueueID, frame.Key.Stream, frame.Key.Seq, now)
	}
	return nil
}

func (s *MemoryStore) updateReliableError(key reliablemq.FrameKey, errorMessage string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	mapKey := makeReliableTransportFrameKey(key)
	frame, ok := s.transportJournal[mapKey]
	if !ok {
		return nil
	}
	frame.Error = errorMessage
	frame.ErrorMessage = errorMessage
	frame.UpdatedAt = s.now().UTC()
	s.transportJournal[mapKey] = frame
	return nil
}

func (s *MemoryStore) nextReliableSeqLocked(
	queueID string,
	stream reliablemq.Stream,
	direction reliablemq.Direction,
) int64 {
	if direction == reliablemq.DirectionOutbound {
		return s.nextOutboundSeqLocked(queueID, stream)
	}
	var maxSeq int64
	for key := range s.transportJournal {
		if key.QueueID == queueID &&
			key.Stream == string(stream) &&
			key.Direction == string(direction) &&
			key.Seq > maxSeq {
			maxSeq = key.Seq
		}
	}
	return maxSeq + 1
}

func (s *MemoryStore) allocateReliableOutboundSeqLocked(
	queueID string,
	stream reliablemq.Stream,
) int64 {
	key := transportQueueStateKey{QueueID: queueID, Stream: string(stream)}
	state, ok := s.transportQueueState[key]
	if !ok {
		state = transportQueueState{NextOutboundSeq: s.nextOutboundSeqFromJournalLocked(queueID, stream)}
	}
	seq := state.NextOutboundSeq
	if seq <= 0 {
		seq = 1
	}
	state.NextOutboundSeq = seq + 1
	state.UpdatedAt = s.now().UTC()
	if state.CreatedAt.IsZero() {
		state.CreatedAt = state.UpdatedAt
	}
	s.transportQueueState[key] = state
	return seq
}

func (s *MemoryStore) nextOutboundSeqLocked(queueID string, stream reliablemq.Stream) int64 {
	key := transportQueueStateKey{QueueID: queueID, Stream: string(stream)}
	if state, ok := s.transportQueueState[key]; ok && state.NextOutboundSeq > 0 {
		return state.NextOutboundSeq
	}
	return s.nextOutboundSeqFromJournalLocked(queueID, stream)
}

func (s *MemoryStore) nextOutboundSeqFromJournalLocked(queueID string, stream reliablemq.Stream) int64 {
	var maxSeq int64
	for key := range s.transportJournal {
		if key.QueueID == queueID &&
			key.Stream == string(stream) &&
			key.Direction == string(reliablemq.DirectionOutbound) &&
			key.Seq > maxSeq {
			maxSeq = key.Seq
		}
	}
	return maxSeq + 1
}

func (s *MemoryStore) updateNextOutboundSeqAtLeastLocked(
	queueID string,
	stream reliablemq.Stream,
	nextSeq int64,
	now time.Time,
) {
	key := transportQueueStateKey{QueueID: queueID, Stream: string(stream)}
	state := s.transportQueueState[key]
	if state.CreatedAt.IsZero() {
		state.CreatedAt = now
	}
	if state.NextOutboundSeq < nextSeq {
		state.NextOutboundSeq = nextSeq
	}
	state.UpdatedAt = now
	s.transportQueueState[key] = state
}

func (s *MemoryStore) inboundAppliedThroughLocked(queueID string, stream reliablemq.Stream) int64 {
	key := transportQueueStateKey{QueueID: queueID, Stream: string(stream)}
	return s.transportQueueState[key].InboundAppliedThrough
}

func (s *MemoryStore) updateInboundAppliedThroughLocked(
	queueID string,
	stream reliablemq.Stream,
	seq int64,
	now time.Time,
) {
	key := transportQueueStateKey{QueueID: queueID, Stream: string(stream)}
	state := s.transportQueueState[key]
	if state.CreatedAt.IsZero() {
		state.CreatedAt = now
	}
	if state.NextOutboundSeq == 0 {
		state.NextOutboundSeq = s.nextOutboundSeqFromJournalLocked(queueID, stream)
	}
	if state.InboundAppliedThrough < seq {
		state.InboundAppliedThrough = seq
	}
	state.UpdatedAt = now
	s.transportQueueState[key] = state
}

type transportQueueStateKey struct {
	QueueID string
	Stream  string
}

type transportQueueState struct {
	NextOutboundSeq       int64
	InboundAppliedThrough int64
	CreatedAt             time.Time
	UpdatedAt             time.Time
}

func (s *MemoryStore) prepareReliableFrameLocked(frame *reliablemq.Frame) {
	now := s.now().UTC()
	s.nextTransportID++
	if frame.Status == "" {
		frame.Status = reliablemq.DefaultStatus(frame.Key.Direction)
	}
	if frame.CreatedAt.IsZero() {
		frame.CreatedAt = now
	}
	if frame.UpdatedAt.IsZero() {
		frame.UpdatedAt = now
	}
}

func makeReliableTransportFrameKey(key reliablemq.FrameKey) transportFrameKey {
	return transportFrameKey{
		QueueID:   key.QueueID,
		Stream:    string(key.Stream),
		Seq:       key.Seq,
		Direction: string(key.Direction),
	}
}

func reliableFromTransportFrame(frame TransportFrame) reliablemq.Frame {
	metadata := reliablemq.Metadata(frame.Metadata).Clone()
	if metadata == nil {
		metadata = reliablemq.Metadata{}
	}
	if frame.AgentID != "" && metadata["agent_id"] == "" {
		metadata["agent_id"] = frame.AgentID
	}
	return reliablemq.Frame{
		Key: reliablemq.FrameKey{
			QueueID:   firstNonEmpty(frame.QueueID, frame.AgentID),
			Stream:    normalizeReliableStream(frame.Stream),
			Seq:       frame.Seq,
			Direction: reliablemq.Direction(firstNonEmpty(frame.Direction, frame.LocalDirection)),
		},
		Kind: reliablemq.FrameKind(
			firstNonEmpty(frame.Kind, string(reliablemq.FrameKindData)),
		),
		Payload:      append(json.RawMessage(nil), frame.PayloadJSON...),
		Metadata:     metadata,
		Status:       reliablemq.Status(frame.Status),
		ErrorMessage: firstNonEmpty(frame.ErrorMessage, frame.Error),
		CreatedAt:    frame.CreatedAt,
		UpdatedAt:    frame.UpdatedAt,
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
	frame.Metadata = cloneStringMap(frame.Metadata)
	frame.SentAt = cloneTimePtr(frame.SentAt)
	frame.ReceivedAt = cloneTimePtr(frame.ReceivedAt)
	frame.AckedAt = cloneTimePtr(frame.AckedAt)
	frame.AppliedAt = cloneTimePtr(frame.AppliedAt)
	return frame
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func cloneTimePtr(ts *time.Time) *time.Time {
	if ts == nil {
		return nil
	}
	cloned := *ts
	return &cloned
}

func isReliableConsumerAckedStatus(status string) bool {
	return status == string(reliablemq.StatusReceived) ||
		status == string(reliablemq.StatusApplied) ||
		status == string(reliablemq.StatusRejected)
}
