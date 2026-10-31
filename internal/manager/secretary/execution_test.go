package secretary

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExecutionIdentityGivenRepeatedRunThenStableAndOwnerScoped(t *testing.T) {
	assert.Equal(t, ExecutionSessionID("owner", "run"), ExecutionSessionID("owner", "run"))
	assert.NotEqual(t, ExecutionSessionID("owner", "run"), ExecutionSessionID("other", "run"))
	assert.NotEqual(t, ExecutionSessionID("owner", "run"), ExecutionSessionID("owner", "other"))
	assert.True(t, IsExecutionSession(ExecutionSessionID("owner", "run")))
	assert.False(t, IsExecutionSession("sess_normal"))
	assert.NotEmpty(t, ExecutionTurnID("owner", "run"))
}

func TestExecutorFuncGivenAdapterThenForwardsContextAndRequest(t *testing.T) {
	type contextKey struct{}
	ctx := context.WithValue(t.Context(), contextKey{}, "marker")
	want := ExecutionRequest{OwnerID: "owner", AgentID: "agent", RunID: "run", Prompt: "summarize"}
	adapter := ExecutorFunc(
		func(received context.Context, request ExecutionRequest) (ExecutionResult, error) {
			assert.Same(t, ctx, received)
			assert.Equal(t, want, request)
			return ExecutionResult{SessionID: "session", Text: "result"}, nil
		},
	)
	result, err := adapter.Execute(ctx, want)
	require.NoError(t, err)
	assert.Equal(t, "result", result.Text)
}
