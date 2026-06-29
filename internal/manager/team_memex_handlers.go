package manager

import (
	"context"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
)

func GetTeamMemexIndex(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.GetTeamMemexIndex(
		c,
		requestMetadata(ctx),
		ctx.Param("team_id"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func ListTeamMemexDocuments(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.ListTeamMemexDocuments(
		c,
		requestMetadata(ctx),
		ctx.Param("team_id"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func GetTeamMemexDocument(c context.Context, ctx *app.RequestContext) {
	documentPath := strings.TrimPrefix(ctx.Param("document_path"), "/")
	status, data, err := serviceFromContext(ctx).userapi.GetTeamMemexDocument(
		c,
		requestMetadata(ctx),
		ctx.Param("team_id"),
		documentPath,
	)
	writeEndpointResult(ctx, status, data, err)
}
