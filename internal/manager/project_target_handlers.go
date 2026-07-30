package manager

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
)

func CreateProjectTarget(c context.Context, ctx *app.RequestContext) {
	var req CreateProjectTargetRequest
	decodeBody(ctx, &req)
	status, data, err := serviceFromContext(ctx).userapi.CreateProjectTarget(
		c,
		requestMetadata(ctx),
		ctx.Param("project_id"),
		req,
	)
	writeEndpointResult(ctx, status, data, err)
}

func ListProjectTargets(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.ListProjectTargets(
		c,
		requestMetadata(ctx),
		ctx.Param("project_id"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func GetProjectTarget(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.GetProjectTarget(
		c,
		requestMetadata(ctx),
		ctx.Param("project_id"),
		ctx.Param("target_id"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func UpdateProjectTarget(c context.Context, ctx *app.RequestContext) {
	var req UpdateProjectTargetRequest
	decodeBody(ctx, &req)
	status, data, err := serviceFromContext(ctx).userapi.UpdateProjectTarget(
		c,
		requestMetadata(ctx),
		ctx.Param("project_id"),
		ctx.Param("target_id"),
		req,
	)
	writeEndpointResult(ctx, status, data, err)
}
