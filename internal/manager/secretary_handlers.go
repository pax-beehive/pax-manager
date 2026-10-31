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

func GetSecretarySettings(c context.Context, ctx *app.RequestContext) {
	data, err := serviceFromContext(ctx).secretary.Get(c, requestMetadata(ctx))
	writeEndpointResult(ctx, http.StatusOK, data, err)
}

func UpdateSecretarySettings(c context.Context, ctx *app.RequestContext) {
	var body struct {
		AgentID          *string `json:"agent_id"`
		Enabled          *bool   `json:"enabled"`
		ExpectedRevision *int64  `json:"expected_revision"`
	}
	decoder := json.NewDecoder(bytes.NewReader(ctx.Request.Body()))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil || body.AgentID == nil || body.Enabled == nil ||
		body.ExpectedRevision == nil {
		writeError(
			ctx,
			http.StatusBadRequest,
			"agent_id, enabled and expected_revision are required",
		)
		return
	}
	if decoder.Decode(new(any)) != io.EOF {
		writeError(ctx, http.StatusBadRequest, "invalid JSON request")
		return
	}
	request := domain.SecretarySettingsUpdate{
		AgentID:          *body.AgentID,
		Enabled:          *body.Enabled,
		ExpectedRevision: *body.ExpectedRevision,
	}
	data, err := serviceFromContext(ctx).secretary.Update(c, requestMetadata(ctx), request)
	writeEndpointResult(ctx, http.StatusOK, data, err)
}

func RunSecretaryNow(c context.Context, ctx *app.RequestContext) {
	data, err := serviceFromContext(ctx).secretary.Run(c, requestMetadata(ctx))
	writeEndpointResult(ctx, http.StatusAccepted, data, err)
}
