package manager

import (
	"context"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

// defaultACPTerminalChunkMaxBytes bounds how large any single terminal-output
// message part is allowed to grow. Terminal output is an unbounded append-only
// stream, so keeping every delta in one part_index makes each flush detoast,
// concatenate and rewrite the whole accumulated value — quadratic write
// amplification. Rolling to a fresh part_index once the active chunk reaches
// this cap keeps every AppendMessagePartText rewrite bounded by the chunk size
// regardless of total output, and the read path already reassembles parts in
// part_index order.
const defaultACPTerminalChunkMaxBytes = 64 * 1024

// acpTerminalChunk is the cached allocation state for one terminal-output
// message: the active part_index and how many bytes have been routed to it.
type acpTerminalChunk struct {
	index int
	size  int
}

// terminalChunkTarget decides which part_index a terminal delta of deltaLen
// bytes appends to, rolling to the next part once the active chunk would exceed
// maxBytes. A chunk always accepts at least one delta, so a single oversized
// delta still lands in its own bounded part instead of being dropped or split.
func terminalChunkTarget(index, size, deltaLen, maxBytes int) (nextIndex, nextSize int) {
	if maxBytes <= 0 {
		maxBytes = defaultACPTerminalChunkMaxBytes
	}
	if size > 0 && size+deltaLen > maxBytes {
		return index + 1, deltaLen
	}
	return index, size + deltaLen
}

// terminalChunkStateFromStore recovers the active chunk (highest text
// part_index and its current byte length) for a terminal-output message. It is
// the seed used after a restart so appends continue on the last chunk and only
// roll forward, never clobbering earlier chunks.
func terminalChunkStateFromStore(
	ctx context.Context,
	store domain.Store,
	messageID string,
) acpTerminalChunk {
	parts, err := store.ListMessageParts(ctx, messageID)
	if err != nil {
		return acpTerminalChunk{}
	}
	chunk := acpTerminalChunk{}
	found := false
	for _, part := range parts {
		if part.PartType != domain.MessagePartText {
			continue
		}
		if !found || part.PartIndex > chunk.index {
			chunk.index = part.PartIndex
			chunk.size = len(part.Text)
			found = true
		}
	}
	return chunk
}

// appendTerminalHistoryText routes a terminal-output delta to the active
// bounded chunk for messageID, rolling part_index when the chunk fills. The
// allocation state is cached per message (seeded once from the store) so the
// hot path performs no per-delta store read, and the batched write only ever
// rewrites the current sub-cap chunk.
func (a *ACPTunnelAgent) appendTerminalHistoryText(
	ctx context.Context,
	messageID string,
	delta string,
) error {
	if a == nil || delta == "" {
		return nil
	}
	target := a.reserveTerminalChunk(ctx, messageID, len(delta))
	return a.historyTextBatcher().AppendText(ctx, messageID, target, delta)
}

// reserveTerminalChunk returns the part_index the next terminal delta must be
// written to, updating the cached allocation state.
func (a *ACPTunnelAgent) reserveTerminalChunk(
	ctx context.Context,
	messageID string,
	deltaLen int,
) int {
	state := a.liveState()

	state.mu.Lock()
	chunk := state.terminalChunks[messageID]
	state.mu.Unlock()

	if chunk == nil {
		// Seed from the store outside the lock so the (one-time) read never
		// blocks unrelated live-state work.
		seed := terminalChunkStateFromStore(ctx, a.store, messageID)
		chunk = &seed
	}

	state.mu.Lock()
	defer state.mu.Unlock()
	if state.terminalChunks == nil {
		state.terminalChunks = make(map[string]*acpTerminalChunk)
	}
	if existing := state.terminalChunks[messageID]; existing != nil {
		chunk = existing
	} else {
		state.terminalChunks[messageID] = chunk
	}
	target, nextSize := terminalChunkTarget(
		chunk.index,
		chunk.size,
		deltaLen,
		defaultACPTerminalChunkMaxBytes,
	)
	chunk.index = target
	chunk.size = nextSize
	return target
}
