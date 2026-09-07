package manager

import (
	"context"
	"encoding/json"
	"strings"
	"time"

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
		return err
	}
	return next(ctx, frame)
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
