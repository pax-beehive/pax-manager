package secretary

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

var (
	ErrExecutionIncomplete = errors.New(
		"secretary execution is incomplete; waiting for its existing turn",
	)
	ErrApprovalRequired = errors.New("secretary execution requires approval in its session")
	ErrNoOutput         = errors.New("secretary agent returned no visible response")
)

type ExecutionRequest struct {
	Job          domain.SecretaryJob
	OwnerID      string
	AgentID      string
	RunID        string
	Prompt       string
	BeforePrompt func(context.Context, string, string) error
}

type ExecutionResult struct {
	SessionID string
	Text      string
	Reads     domain.SecretaryToolReads
}

type Executor interface {
	Execute(context.Context, ExecutionRequest) (ExecutionResult, error)
}

type ExecutorFunc func(context.Context, ExecutionRequest) (ExecutionResult, error)

func (f ExecutorFunc) Execute(
	ctx context.Context,
	request ExecutionRequest,
) (ExecutionResult, error) {
	return f(ctx, request)
}

func executionIdentity(ownerID, runID string) string {
	sum := sha256.Sum256([]byte(ownerID + "\x00" + runID))
	return hex.EncodeToString(sum[:16])
}

func ExecutionSessionID(ownerID, runID string) string {
	return domain.SecretaryInternalSessionPrefix + executionIdentity(ownerID, runID)
}

func ExecutionTurnID(ownerID, runID string) string {
	return "turn_secretary_" + executionIdentity(ownerID, runID)
}

func IsExecutionSession(sessionID string) bool {
	return strings.HasPrefix(sessionID, domain.SecretaryInternalSessionPrefix)
}
