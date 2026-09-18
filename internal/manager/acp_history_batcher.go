package manager

import (
	"context"
	"sync"
	"time"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/logging"
)

const (
	defaultACPHistoryTextBatchFlushInterval = 75 * time.Millisecond
	defaultACPHistoryTextBatchMaxChars      = 1024
)

type acpHistoryTextSink interface {
	EnsureMessage(ctx context.Context, msg *domain.Message) error
	AppendText(ctx context.Context, messageID string, partIndex int, delta string) error
	// AppendTerminalText appends an unbounded terminal-output delta, rolling it
	// across bounded part_index chunks so no single message part grows without
	// limit.
	AppendTerminalText(ctx context.Context, messageID string, delta string) error
	Flush(ctx context.Context) error
}

type immediateACPHistoryTextSink struct {
	store domain.Store
}

func (s immediateACPHistoryTextSink) EnsureMessage(ctx context.Context, msg *domain.Message) error {
	return s.store.UpsertMessage(ctx, msg)
}

func (s immediateACPHistoryTextSink) AppendText(
	ctx context.Context,
	messageID string,
	partIndex int,
	delta string,
) error {
	return s.store.AppendMessagePartText(ctx, messageID, partIndex, delta, nil)
}

func (s immediateACPHistoryTextSink) AppendTerminalText(
	ctx context.Context,
	messageID string,
	delta string,
) error {
	if delta == "" {
		return nil
	}
	chunk := terminalChunkStateFromStore(ctx, s.store, messageID)
	target, _ := terminalChunkTarget(
		chunk.index,
		chunk.size,
		len(delta),
		defaultACPTerminalChunkMaxBytes,
	)
	return s.store.AppendMessagePartText(ctx, messageID, target, delta, nil)
}

func (s immediateACPHistoryTextSink) Flush(ctx context.Context) error {
	_ = ctx
	return nil
}

type acpHistoryTextBatcher struct {
	flushMu       sync.Mutex
	mu            sync.Mutex
	store         domain.Store
	flushInterval time.Duration
	maxChars      int
	pending       map[acpHistoryTextBatchKey]*acpHistoryTextBatch
	timer         *time.Timer
}

type acpHistoryTextBatchKey struct {
	messageID string
	partIndex int
}

type acpHistoryTextBatch struct {
	key  acpHistoryTextBatchKey
	text string
}

func newACPHistoryTextBatcher(
	store domain.Store,
	flushInterval time.Duration,
	maxChars int,
) *acpHistoryTextBatcher {
	if flushInterval <= 0 {
		flushInterval = defaultACPHistoryTextBatchFlushInterval
	}
	if maxChars <= 0 {
		maxChars = defaultACPHistoryTextBatchMaxChars
	}
	return &acpHistoryTextBatcher{
		store:         store,
		flushInterval: flushInterval,
		maxChars:      maxChars,
		pending:       make(map[acpHistoryTextBatchKey]*acpHistoryTextBatch),
	}
}

func (b *acpHistoryTextBatcher) AppendText(
	ctx context.Context,
	messageID string,
	partIndex int,
	delta string,
) error {
	_ = ctx
	if b == nil || delta == "" {
		return nil
	}
	key := acpHistoryTextBatchKey{messageID: messageID, partIndex: partIndex}

	b.mu.Lock()
	batch := b.pending[key]
	if batch == nil {
		batch = &acpHistoryTextBatch{key: key}
		b.pending[key] = batch
	}
	batch.text += delta
	flushNow := len(batch.text) >= b.maxChars
	b.ensureTimerLocked()
	b.mu.Unlock()

	if flushNow {
		return b.Flush(context.Background())
	}
	return nil
}

func (b *acpHistoryTextBatcher) Flush(ctx context.Context) error {
	if b == nil {
		return nil
	}
	// Serialize extraction and persistence together, including failed-batch requeue.
	// A completion flush must also wait for a batch already being written.
	b.flushMu.Lock()
	defer b.flushMu.Unlock()
	batches := b.takeAll()
	return b.flushBatches(ctx, batches)
}

func (b *acpHistoryTextBatcher) ensureTimerLocked() {
	if b.timer != nil {
		return
	}
	b.timer = time.AfterFunc(b.flushInterval, func() {
		if err := b.Flush(context.Background()); err != nil {
			logging.Error(
				context.Background(),
				"acp history text batch flush failed",
				logging.Err(err),
			)
		}
	})
}

func (b *acpHistoryTextBatcher) takeAll() []acpHistoryTextBatch {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
	}
	if len(b.pending) == 0 {
		return nil
	}
	batches := make([]acpHistoryTextBatch, 0, len(b.pending))
	for key, batch := range b.pending {
		if batch.text == "" {
			delete(b.pending, key)
			continue
		}
		batches = append(batches, *batch)
		delete(b.pending, key)
	}
	return batches
}

func (b *acpHistoryTextBatcher) flushBatches(
	ctx context.Context,
	batches []acpHistoryTextBatch,
) error {
	var firstErr error
	failed := make([]acpHistoryTextBatch, 0)
	for _, batch := range batches {
		if batch.text == "" {
			continue
		}
		if err := b.store.AppendMessagePartText(
			ctx,
			batch.key.messageID,
			batch.key.partIndex,
			batch.text,
			nil,
		); err != nil {
			failed = append(failed, batch)
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	if len(failed) > 0 {
		b.requeue(failed)
	}
	return firstErr
}

func (b *acpHistoryTextBatcher) requeue(batches []acpHistoryTextBatch) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, batch := range batches {
		current := b.pending[batch.key]
		if current == nil {
			b.pending[batch.key] = &acpHistoryTextBatch{
				key:  batch.key,
				text: batch.text,
			}
			continue
		}
		current.text = batch.text + current.text
	}
	b.ensureTimerLocked()
}

type acpAgentHistoryTextSink struct {
	agent *ACPTunnelAgent
}

func (s acpAgentHistoryTextSink) EnsureMessage(ctx context.Context, msg *domain.Message) error {
	return s.agent.ensureHistoryMessage(ctx, msg)
}

func (s acpAgentHistoryTextSink) AppendText(
	ctx context.Context,
	messageID string,
	partIndex int,
	delta string,
) error {
	return s.agent.appendHistoryText(ctx, messageID, partIndex, delta)
}

func (s acpAgentHistoryTextSink) AppendTerminalText(
	ctx context.Context,
	messageID string,
	delta string,
) error {
	return s.agent.appendTerminalHistoryText(ctx, messageID, delta)
}

func (s acpAgentHistoryTextSink) Flush(ctx context.Context) error {
	return s.agent.flushHistoryText(ctx)
}

func (a *ACPTunnelAgent) ensureHistoryMessage(ctx context.Context, msg *domain.Message) error {
	if a == nil {
		return nil
	}
	messageID := msg.MessageID
	state := a.liveState()
	state.mu.Lock()
	if state.projectedHistoryMessages == nil {
		state.projectedHistoryMessages = make(map[string]struct{})
	}
	if _, ok := state.projectedHistoryMessages[messageID]; ok {
		state.mu.Unlock()
		return nil
	}
	state.mu.Unlock()

	if err := a.store.UpsertMessage(ctx, msg); err != nil {
		return err
	}

	state.mu.Lock()
	state.projectedHistoryMessages[messageID] = struct{}{}
	state.mu.Unlock()
	return nil
}

func (a *ACPTunnelAgent) appendHistoryText(
	ctx context.Context,
	messageID string,
	partIndex int,
	delta string,
) error {
	if a == nil {
		return nil
	}
	batcher := a.historyTextBatcher()
	return batcher.AppendText(ctx, messageID, partIndex, delta)
}

func (a *ACPTunnelAgent) flushHistoryText(ctx context.Context) error {
	if a == nil {
		return nil
	}
	state := a.liveState()
	state.mu.Lock()
	batcher := state.historyTextBatcher
	state.mu.Unlock()
	if batcher == nil {
		return nil
	}
	return batcher.Flush(ctx)
}

func (a *ACPTunnelAgent) flushHistoryTextBoundary(ctx context.Context) error {
	if a == nil {
		return nil
	}
	if err := a.flushHistoryText(ctx); err != nil {
		return err
	}
	state := a.liveState()
	state.mu.Lock()
	state.historyGroups.groups = nil
	state.mu.Unlock()
	return nil
}

func (a *ACPTunnelAgent) historyTextBatcher() *acpHistoryTextBatcher {
	state := a.liveState()
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.historyTextBatcher == nil {
		state.historyTextBatcher = newACPHistoryTextBatcher(
			a.store,
			defaultACPHistoryTextBatchFlushInterval,
			defaultACPHistoryTextBatchMaxChars,
		)
	}
	return state.historyTextBatcher
}
