package manager

import (
	"context"
	"net/http"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
	generated "github.com/pax-beehive/pax-manager/internal/transport/http/handler"
)

func TestGeneratedHandlersGivenWorkspaceMethodsThenRequireAuthenticatedOwner(t *testing.T) {
	svc := &Service{
		workspaceSearch: userapi.NewWorkspaceSearchService(nil, nil, nil, nil, nil),
		secretary:       userapi.NewSecretaryService(nil, nil, nil, nil),
	}
	for _, test := range []struct {
		name    string
		handler func(context.Context, *app.RequestContext)
		body    string
	}{
		{"SearchWorkspace", generated.SearchWorkspace, `{}`},
		{"ReadWorkspaceSearchSource", generated.ReadWorkspaceSearchSource, `{}`},
		{"PublishWorkspaceSummary", generated.PublishWorkspaceSummary, `{}`},
		{"GetSecretarySettings", generated.GetSecretarySettings, ""},
		{"UpdateSecretarySettings", generated.UpdateSecretarySettings,
			`{"agent_id":"","enabled":false,"expected_revision":0}`},
		{"RunSecretaryNow", generated.RunSecretaryNow, ""},
		{"GetSecretaryProgress", generated.GetSecretaryProgress, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := app.NewContext(0)
			ctx.Set("service", svc)
			ctx.Request.SetBodyString(test.body)
			test.handler(t.Context(), ctx)
			require.Equal(t, http.StatusUnauthorized, ctx.Response.StatusCode(),
				string(ctx.Response.Body()))
		})
	}
}

func TestAgentIDFromPath(t *testing.T) {
	t.Run("Given legacy agent path then it returns agent ID", func(t *testing.T) {
		require.Equal(t, "agent_1", agentIDFromPath("/api/user/agents/agent_1"))
	})

	t.Run("Given v1 agent path then it returns agent ID", func(t *testing.T) {
		require.Equal(
			t,
			"agent_1",
			agentIDFromPath("/api/v1/user/usr_1/agents/agent_1"),
		)
	})

	t.Run("Given proxied v1 agent path then it returns agent ID", func(t *testing.T) {
		require.Equal(
			t,
			"agent_75a010ca36ea7543f1268d6e42609a6203df383d0bc5648c",
			agentIDFromPath(
				"/api/pax/api/v1/user/usr_a2c75d7240c95f7ac0531dbcab9cb942f187af00788dfd68/agents/agent_75a010ca36ea7543f1268d6e42609a6203df383d0bc5648c",
			),
		)
	})
}
