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

func ListSecretaryTasks(c context.Context, ctx *app.RequestContext) {
	query, ok := secretaryTaskHTTPQuery(ctx)
	if !ok {
		writeError(ctx, http.StatusBadRequest, "Use cursor and a limit between 1 and 100")
		return
	}
	data, err := serviceFromContext(ctx).secretaryTasks.List(c, requestMetadata(ctx), query)
	writeEndpointResult(ctx, http.StatusOK, data, err)
}

func GetSecretaryTask(c context.Context, ctx *app.RequestContext) {
	query, ok := secretaryTaskHTTPQuery(ctx)
	if !ok {
		writeError(ctx, http.StatusBadRequest, "Use cursor and a limit between 1 and 100")
		return
	}
	data, err := serviceFromContext(
		ctx,
	).secretaryTasks.Get(
		c,
		requestMetadata(ctx),
		ctx.Param("task_id"),
		query,
	)
	writeEndpointResult(ctx, http.StatusOK, data, err)
}

func ControlSecretaryTask(c context.Context, ctx *app.RequestContext) {
	if ctx.QueryArgs().Len() != 0 {
		writeError(ctx, http.StatusBadRequest, "Task actions do not accept query parameters")
		return
	}
	action, ok := decodeSecretaryTaskAction(ctx.Request.Body())
	if !ok {
		writeError(
			ctx,
			http.StatusBadRequest,
			"Provide one task action with all observed revision fields",
		)
		return
	}
	data, err := serviceFromContext(
		ctx,
	).secretaryTasks.Control(
		c,
		requestMetadata(ctx),
		ctx.Param("task_id"),
		action,
	)
	writeEndpointResult(ctx, http.StatusOK, data, err)
}

func secretaryTaskHTTPQuery(ctx *app.RequestContext) (domain.SecretaryTaskQuery, bool) {
	query, ok := secretaryProgressQuery(ctx)
	return domain.SecretaryTaskQuery{Cursor: query.Cursor, Limit: query.Limit}, ok
}

func decodeSecretaryTaskAction(raw []byte) (domain.SecretaryTaskAction, bool) {
	var result domain.SecretaryTaskAction
	if len(raw) > 8192 || !uniqueSecretaryTaskJSON(raw) {
		return result, false
	}
	var body struct {
		Action           *string `json:"action"`
		ExpectedRevision *int64  `json:"expected_revision"`
		BatchID          *string `json:"batch_id"`
		AttemptID        *string `json:"attempt_id"`
		RetryEpoch       *int64  `json:"retry_epoch"`
		SettingsRevision *int64  `json:"settings_revision"`
		ConfirmRestart   bool    `json:"confirm_restart"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&body) != nil || body.Action == nil || body.ExpectedRevision == nil ||
		body.BatchID == nil ||
		body.AttemptID == nil ||
		body.RetryEpoch == nil ||
		body.SettingsRevision == nil {
		return result, false
	}
	return domain.SecretaryTaskAction{
		Action:           *body.Action,
		ExpectedRevision: *body.ExpectedRevision,
		BatchID:          *body.BatchID,
		AttemptID:        *body.AttemptID,
		RetryEpoch:       *body.RetryEpoch,
		SettingsRevision: *body.SettingsRevision,
		ConfirmRestart:   body.ConfirmRestart,
	}, true
}

func uniqueSecretaryTaskJSON(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return false
	}
	seen := map[string]bool{}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return false
		}
		name, ok := key.(string)
		if !ok || seen[name] {
			return false
		}
		switch name {
		case "action",
			"expected_revision",
			"batch_id",
			"attempt_id",
			"retry_epoch",
			"settings_revision",
			"confirm_restart":
		default:
			return false
		}
		seen[name] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return false
		}
	}
	closing, err := decoder.Token()
	return err == nil && closing == json.Delim('}') && decoder.Decode(new(any)) == io.EOF
}
