package manager

import (
	"context"
	"net/http"
	"strconv"

	"github.com/cloudwego/hertz/pkg/app"
)

func GetSessionMessageDetail(c context.Context, ctx *app.RequestContext) {
	offset, validOffset := detailQueryInt(ctx, "offset")
	limit, validLimit := detailQueryInt(ctx, "limit")
	if !validOffset || !validLimit {
		writeError(ctx, http.StatusBadRequest, "offset and limit must be non-negative integers")
		return
	}
	status, data, err := serviceFromContext(
		ctx,
	).userapi.GetSessionMessageDetail(
		c,
		requestMetadata(ctx),
		ctx.Param(
			"session_id",
		),
		ctx.Param("message_id"),
		ctx.Query("section"),
		ctx.Query("revision"),
		offset,
		limit,
	)
	writeEndpointResult(ctx, status, data, err)
}

func detailQueryInt(ctx *app.RequestContext, key string) (int, bool) {
	value := ctx.Query(key)
	if value == "" {
		return 0, true
	}
	number, err := strconv.Atoi(value)
	return number, err == nil && number >= 0
}
