package manager

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pax-beehive/paxkit/reliablemq"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

type sessionCommandsObservationMiddleware struct{ service *Service }

func (m sessionCommandsObservationMiddleware) HandleACPFrame(
	ctx context.Context,
	frame *acpFrameContext,
	next acpFrameHandler,
) error {
	if frame.direction != acpAgentToUser || frame.agent == nil || frame.managerSessionID == "" ||
		frame.frame.Method != "session/update" {
		return next(ctx, frame)
	}
	commands, ok := parseSessionACPCommands(frame.frame.Params, m.service.clock().UTC())
	if !ok {
		return next(ctx, frame)
	}
	if err := m.service.store.UpdateSessionACPCommands(ctx, frame.agent.agentID, frame.managerSessionID, commands); err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			deferred, deferErr := deferUnboundSessionCommands(ctx, m.service.store, frame)
			if deferErr != nil {
				return deferErr
			}
			if deferred {
				return nil
			}
		}
		return err
	}
	return next(ctx, frame)
}

func deferUnboundSessionCommands(
	ctx context.Context,
	store domain.Store,
	frame *acpFrameContext,
) (bool, error) {
	if frame.transportSeq <= 0 || len(frame.frame.ID) > 0 {
		return false, nil
	}
	if sourceAgent := frame.transportMetadata["agent_id"]; sourceAgent != "" &&
		sourceAgent != frame.agent.agentID {
		return false, domain.ErrConflict
	}
	nativeID := firstNonEmpty(frame.nativeSessionID, frameSessionID(frame.frame))
	if nativeID == "" {
		return false, nil
	}
	_, err := resolveStoreSessionAliases(
		ctx,
		store,
		frame.agent.ownerUserID,
		frame.agent.agentID,
		nativeID,
	)
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, domain.ErrNotFound) {
		return false, err
	}
	metadata := reliablemq.Metadata{}
	for key, value := range frame.transportMetadata {
		metadata[key] = value
	}
	metadata[domain.DeferredSessionCommandsMetadataKey] = "true"
	metadata["native_session_id"] = nativeID
	metadata["agent_id"] = frame.agent.agentID
	key := reliablemq.FrameKey{
		QueueID: frame.agent.queueID(), Stream: reliablemq.StreamACP,
		Direction: reliablemq.DirectionInbound, Seq: frame.transportSeq,
	}
	if err := frame.agent.durableTransportStore().UpdateMetadata(ctx, key, metadata); err != nil {
		return false, err
	}
	return true, nil
}

func hydrateDeferredSessionCommands(
	ctx context.Context,
	store domain.Store,
	frame *acpFrameContext,
	managerID, nativeID string,
) error {
	reader, ok := store.(domain.SessionCommandJournalStore)
	if !ok || frame.transportSeq <= 0 {
		return nil
	}
	update, err := reader.LatestDeferredSessionCommands(
		ctx,
		frame.agent.ownerUserID,
		frame.agent.agentID,
		frame.agent.queueID(),
		nativeID,
		frame.transportSeq,
	)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	commands, valid := parseSessionACPCommands(update.Params, update.ObservedAt)
	if !valid {
		return fmt.Errorf("%w: invalid deferred session commands", domain.ErrConflict)
	}
	return store.UpdateSessionACPCommands(ctx, frame.agent.agentID, managerID, commands)
}

func parseSessionACPCommands(
	params json.RawMessage,
	observedAt time.Time,
) (domain.SessionACPCommands, bool) {
	var value struct {
		Update struct {
			Kind     string            `json:"sessionUpdate"`
			Commands []json.RawMessage `json:"availableCommands"`
		} `json:"update"`
	}
	if json.Unmarshal(params, &value) != nil || value.Update.Kind != "available_commands_update" ||
		value.Update.Commands == nil {
		return domain.SessionACPCommands{}, false
	}
	names := map[string]bool{}
	for _, raw := range value.Update.Commands {
		var command struct {
			Name        string  `json:"name"`
			Description *string `json:"description"`
		}
		if json.Unmarshal(raw, &command) != nil || strings.TrimSpace(command.Name) == "" ||
			command.Description == nil ||
			names[command.Name] {
			return domain.SessionACPCommands{}, false
		}
		names[command.Name] = true
	}
	return domain.SessionACPCommands{
		AvailableCommands: value.Update.Commands,
		ObservedAt:        observedAt,
	}, true
}
