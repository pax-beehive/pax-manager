package manager

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
)

func SearchWorkspace(c context.Context, ctx *app.RequestContext) {
	var request domain.SearchQuery
	if !decodeWorkspaceRequest(ctx, &request) {
		return
	}
	data, err := serviceFromContext(ctx).workspaceSearch.Search(c, requestMetadata(ctx), request)
	writeEndpointResult(ctx, http.StatusOK, data, err)
}

func ReadWorkspaceSearchSource(c context.Context, ctx *app.RequestContext) {
	var request userapi.WorkspaceReadRequest
	if !decodeWorkspaceRequest(ctx, &request) {
		return
	}
	data, err := serviceFromContext(ctx).workspaceSearch.Read(c, requestMetadata(ctx), request)
	writeEndpointResult(ctx, http.StatusOK, data, err)
}

func PublishWorkspaceSummary(c context.Context, ctx *app.RequestContext) {
	var request domain.PublishSummaryRequest
	if !decodeWorkspaceRequest(ctx, &request) {
		return
	}
	data, err := serviceFromContext(ctx).workspaceSearch.Publish(c, requestMetadata(ctx), request)
	writeEndpointResult(ctx, http.StatusOK, data, err)
}

func decodeWorkspaceRequest(ctx *app.RequestContext, target any) bool {
	if err := json.Unmarshal(ctx.Request.Body(), target); err != nil {
		writeError(ctx, http.StatusBadRequest, "invalid JSON request")
		return false
	}
	return true
}
