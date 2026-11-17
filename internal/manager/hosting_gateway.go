package manager

import (
	"context"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
)

func ProxyHostingModel(_ context.Context, ctx *app.RequestContext) {
	ctx.Header("Cache-Control", "no-store")
	writeError(ctx, http.StatusGone, "model_gateway_moved")
}
