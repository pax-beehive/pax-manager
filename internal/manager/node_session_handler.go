package manager

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func RegisterNodeSession(ctx context.Context, c *app.RequestContext) {
	var req domain.RegisterNodeSessionRequest
	decodeBody(c, &req)
	status, data, err := serviceFromContext(
		c,
	).paxd.RegisterNodeSession(
		ctx,
		nodeFromContext(c),
		c.Param("agent_id"),
		&req,
	)
	writeEndpointResult(c, status, data, err)
}
