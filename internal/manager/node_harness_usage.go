package manager

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
)

func GetNodeHarnessUsage(c context.Context, ctx *app.RequestContext) {
	ctx.Header("Cache-Control", "no-store")
	status, data, err := serviceFromContext(ctx).userapi.GetNodeHarnessUsage(
		c,
		requestMetadata(ctx),
		ctx.Param("node_id"),
		ctx.Param("harness"),
	)
	writeEndpointResult(ctx, status, data, err)
}
