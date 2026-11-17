package manager

import (
	"context"
	"net/http"
	"net/url"

	"github.com/cloudwego/hertz/pkg/app"
)

func GetHostingModelCatalog(c context.Context, ctx *app.RequestContext) {
	s, _, ok := hostingAdminPrincipal(c, ctx)
	if !ok {
		return
	}
	query, err := url.ParseQuery(string(ctx.URI().QueryString()))
	providers := query["provider"]
	if err != nil || len(query) != 1 || len(providers) != 1 || providers[0] != "openrouter" {
		writeError(ctx, http.StatusBadRequest, "invalid_model_catalog_provider")
		return
	}
	catalog, err := s.modelCatalog.Read(c)
	if err != nil {
		writeError(ctx, http.StatusServiceUnavailable, "model_catalog_unavailable")
		return
	}
	writeData(ctx, http.StatusOK, catalog)
}
