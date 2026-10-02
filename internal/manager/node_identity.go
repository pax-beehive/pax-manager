package manager

import (
	"context"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
)

// GetNodeIdentity repairs routing hints using only the authenticated Node Key.
func GetNodeIdentity(_ context.Context, ctx *app.RequestContext) {
	node := nodeFromContext(ctx)
	ctx.Header("Cache-Control", "no-store")
	writeData(ctx, http.StatusOK, map[string]string{
		"node_id": node.NodeID,
		"user_id": node.OwnerUserID,
		"region":  serviceFromContext(ctx).cfg.Region,
	})
}
