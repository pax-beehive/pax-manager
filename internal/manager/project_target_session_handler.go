package manager

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
)

func CreateProjectTargetSession(c context.Context, ctx *app.RequestContext) {
	var req CreateProjectTargetSessionRequest
	decodeBody(ctx, &req)
	status, data, err := serviceFromContext(ctx).userapi.CreateProjectTargetSession(
		c,
		requestMetadata(ctx),
		ctx.Param("project_id"),
		ctx.Param("target_id"),
		req,
	)
	writeEndpointResult(ctx, status, data, err)
}
