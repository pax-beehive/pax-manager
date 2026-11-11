package manager

import (
	"context"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
)

func CallWorkspaceTool(ctx context.Context, c *app.RequestContext) {
	var request workspaceToolRequest
	if err := decodeSecretaryToolJSON(string(c.Request.Body()), &request); err != nil {
		writeEndpointError(c, err)
		return
	}
	result, err := serviceFromContext(c).callWorkspaceTool(
		ctx, nodeFromContext(c), c.Param("agent_id"), request,
	)
	writeEndpointResult(c, http.StatusOK, result, err)
}
