package manager

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func NodeHarnessAuth(ctx context.Context, c *app.RequestContext) {
	c.Header("Cache-Control", "no-store")
	if len(c.Request.Body()) > 32*1024 {
		writeError(c, http.StatusRequestEntityTooLarge, "authentication request exceeds limit")
		return
	}
	var req domain.NodeHarnessAuthRequest
	if err := json.Unmarshal(c.Request.Body(), &req); err != nil {
		writeError(c, http.StatusBadRequest, "invalid authentication request")
		return
	}
	req.UserID = c.Param("user_id")
	req.NodeID = c.Param("node_id")
	status, data, err := serviceFromContext(c).userapi.NodeHarnessAuth(ctx, requestMetadata(c), req)
	writeEndpointResult(c, status, data, err)
}
func GetNodeHarnessAuth(ctx context.Context, c *app.RequestContext) {
	c.Header("Cache-Control", "no-store")
	req := domain.GetNodeHarnessAuthRequest{
		NodeID:    c.Param("node_id"),
		Harness:   c.Query("harness"),
		SessionID: c.Query("session_id"),
		CommandID: c.Query("command_id"),
		Provider:  c.Query("provider"),
		Operation: c.Query("operation"),
	}
	status, data, err := serviceFromContext(
		c,
	).userapi.GetNodeHarnessAuth(
		ctx,
		requestMetadata(c),
		req,
	)
	writeEndpointResult(c, status, data, err)
}
