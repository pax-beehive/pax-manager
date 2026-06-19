package manager

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func CreateKnowledgeCapsule(c context.Context, ctx *app.RequestContext) {
	var req CreateKnowledgeCapsuleRequest
	decodeBody(ctx, &req)
	status, data, err := serviceFromContext(ctx).userapi.CreateKnowledgeCapsule(
		c,
		requestMetadata(ctx),
		ctx.Param("session_id"),
		req,
	)
	writeEndpointResult(ctx, status, data, err)
}

func ListKnowledgeCapsules(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.ListKnowledgeCapsules(
		c,
		requestMetadata(ctx),
		domain.ListKnowledgeCapsulesFilter{
			Status:          string(ctx.QueryArgs().Peek("status")),
			Keyword:         string(ctx.QueryArgs().Peek("keyword")),
			SourceSessionID: string(ctx.QueryArgs().Peek("source_session_id")),
			Limit:           queryInt(ctx, "limit"),
			Cursor:          string(ctx.QueryArgs().Peek("cursor")),
		},
	)
	writeEndpointResult(ctx, status, data, err)
}

func GetKnowledgeCapsule(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.GetKnowledgeCapsule(
		c,
		requestMetadata(ctx),
		ctx.Param("capsule_id"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func ArchiveKnowledgeCapsule(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.ArchiveKnowledgeCapsule(
		c,
		requestMetadata(ctx),
		ctx.Param("capsule_id"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func InjectKnowledgeCapsule(c context.Context, ctx *app.RequestContext) {
	var req InjectKnowledgeCapsuleRequest
	decodeBody(ctx, &req)
	status, data, err := serviceFromContext(ctx).userapi.InjectKnowledgeCapsule(
		c,
		requestMetadata(ctx),
		ctx.Param("session_id"),
		req,
	)
	writeEndpointResult(ctx, status, data, err)
}

func ListKnowledgeInjections(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.ListKnowledgeInjections(
		c,
		requestMetadata(ctx),
		ctx.Param("session_id"),
		domain.ListKnowledgeInjectionsFilter{
			Limit:  queryInt(ctx, "limit"),
			Cursor: string(ctx.QueryArgs().Peek("cursor")),
		},
	)
	writeEndpointResult(ctx, status, data, err)
}
