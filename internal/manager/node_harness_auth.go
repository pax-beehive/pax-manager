package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func GetNodeHarnessAuthStatus(c context.Context, ctx *app.RequestContext) {
	ctx.Header("Cache-Control", "no-store")
	status, data, err := serviceFromContext(ctx).userapi.GetNodeHarnessAuthStatus(
		c,
		requestMetadata(ctx),
		ctx.Param("node_id"),
		ctx.Param("harness"),
		ctx.Query("session_id"),
	)
	writeEndpointResult(ctx, status, data, err)
}

func LoginNodeHarness(c context.Context, ctx *app.RequestContext) {
	ctx.Header("Cache-Control", "no-store")
	body := ctx.Request.Body()
	var req domain.LoginNodeHarnessRequest
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if len(body) == 0 || len(body) > 32*1024 || decoder.Decode(&req) != nil ||
		decoder.Decode(new(any)) != io.EOF {
		writeError(ctx, http.StatusBadRequest, "invalid harness login request")
		return
	}
	req.UserID, req.NodeID, req.Harness = ctx.Param(
		"user_id",
	), ctx.Param(
		"node_id",
	), ctx.Param(
		"harness",
	)
	status, data, err := serviceFromContext(
		ctx,
	).userapi.LoginNodeHarness(
		c,
		requestMetadata(ctx),
		req,
	)
	writeEndpointResult(ctx, status, data, err)
}
