package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/apperr"
)

func NodeMobileControl(c context.Context, ctx *app.RequestContext) {
	mediaType, _, err := mime.ParseMediaType(string(ctx.Request.Header.ContentType()))
	if err != nil || mediaType != "application/json" {
		writeEndpointResult(ctx, 0, nil, apperr.Error{
			Status: http.StatusUnsupportedMediaType, Message: "use application/json",
		})
		return
	}
	var req struct {
		Operation string          `json:"operation"`
		Payload   json.RawMessage `json:"payload"`
	}
	decoder := json.NewDecoder(bytes.NewReader(ctx.Request.Body()))
	decoder.DisallowUnknownFields()
	var trailing any
	if len(ctx.Request.Body()) > 32768 || decoder.Decode(&req) != nil ||
		decoder.Decode(&trailing) != io.EOF {
		writeEndpointResult(ctx, 0, nil, apperr.Error{
			Status: http.StatusBadRequest, Message: "invalid mobile control request",
		})
		return
	}
	status, data, err := serviceFromContext(ctx).userapi.NodeMobileControl(
		c, requestMetadata(ctx), ctx.Param("node_id"), req.Operation, req.Payload,
	)
	writeEndpointResult(ctx, status, data, err)
}
