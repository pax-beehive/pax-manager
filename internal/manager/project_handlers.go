package manager

import (
	"context"
	"net/http"
	"strconv"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
)

func CreateProject(c context.Context, ctx *app.RequestContext) {
	var req CreateProjectRequest
	decodeBody(ctx, &req)
	status, data, err := serviceFromContext(ctx).userapi.CreateProject(
		c,
		requestMetadata(ctx),
		req,
	)
	writeEndpointResult(ctx, status, data, err)
}

func ListProjects(c context.Context, ctx *app.RequestContext) {
	includeArchived, err := projectIncludeArchived(ctx)
	if err != nil {
		writeEndpointError(ctx, err)
		return
	}
	status, data, err := serviceFromContext(ctx).userapi.ListProjects(
		c,
		requestMetadata(ctx),
		includeArchived,
	)
	writeEndpointResult(ctx, status, data, err)
}

func GetProject(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.GetProject(
		c,
		requestMetadata(ctx),
		ctx.Param("project_id"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func UpdateProject(c context.Context, ctx *app.RequestContext) {
	var req UpdateProjectRequest
	decodeBody(ctx, &req)
	status, data, err := serviceFromContext(ctx).userapi.UpdateProject(
		c,
		requestMetadata(ctx),
		ctx.Param("project_id"),
		req,
	)
	writeEndpointResult(ctx, status, data, err)
}

func ArchiveProject(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.ArchiveProject(
		c,
		requestMetadata(ctx),
		ctx.Param("project_id"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func projectIncludeArchived(ctx *app.RequestContext) (bool, error) {
	raw := string(ctx.QueryArgs().Peek("include_archived"))
	if raw == "" {
		return false, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, apperr.Error{
			Status:  http.StatusBadRequest,
			Message: "include_archived must be a boolean",
		}
	}
	return value, nil
}
