package manager

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func CreateEnvelope(c context.Context, ctx *app.RequestContext) {
	var req CreateEnvelopeRequest
	decodeBody(ctx, &req)
	status, data, err := serviceFromContext(ctx).userapi.CreateEnvelope(
		c,
		requestMetadata(ctx),
		req,
	)
	writeEndpointResult(ctx, status, data, err)
}

func ListEnvelopes(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.ListEnvelopes(
		c,
		requestMetadata(ctx),
		domain.ListEnvelopesFilter{
			Status: string(ctx.QueryArgs().Peek("status")),
			Limit:  queryInt(ctx, "limit"),
			Cursor: string(ctx.QueryArgs().Peek("cursor")),
		},
	)
	writeEndpointResult(ctx, status, data, err)
}

func GetEnvelope(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.GetEnvelope(
		c,
		requestMetadata(ctx),
		ctx.Param("envelope_id"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func AcceptEnvelope(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.AcceptEnvelope(
		c,
		requestMetadata(ctx),
		ctx.Param("envelope_id"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func ArchiveEnvelope(c context.Context, ctx *app.RequestContext) {
	status, data, err := serviceFromContext(ctx).userapi.ArchiveEnvelope(
		c,
		requestMetadata(ctx),
		ctx.Param("envelope_id"),
	)
	writeEndpointResult(ctx, status, data, err)
}
