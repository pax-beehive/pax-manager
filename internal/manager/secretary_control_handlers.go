package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func GetSecretaryGuidance(c context.Context, ctx *app.RequestContext) {
	scope, source, ok := secretaryGuidanceQuery(ctx)
	if !ok {
		writeError(ctx, http.StatusBadRequest, "Use scope and optional source_id only")
		return
	}
	data, err := serviceFromContext(
		ctx,
	).secretaryControl.GetGuidance(
		c,
		requestMetadata(ctx),
		scope,
		source,
	)
	writeEndpointResult(ctx, http.StatusOK, data, err)
}

func UpdateSecretaryGuidance(c context.Context, ctx *app.RequestContext) {
	scope, source, ok := secretaryGuidanceQuery(ctx)
	var request domain.SecretaryGuidanceUpdate
	if !ok ||
		!decodeSecretaryControl(
			ctx.Request.Body(),
			&request,
			16*1024,
			"instruction",
			"expected_revision",
		) {
		writeError(
			ctx,
			http.StatusBadRequest,
			"Provide instruction, expected_revision and valid guidance scope",
		)
		return
	}
	data, err := serviceFromContext(
		ctx,
	).secretaryControl.UpdateGuidance(
		c,
		requestMetadata(ctx),
		scope,
		source,
		request,
	)
	writeEndpointResult(ctx, http.StatusOK, data, err)
}

func PrepareSecretaryOperation(c context.Context, ctx *app.RequestContext) {
	var request domain.SecretaryOperationPrepare
	if ctx.QueryArgs().Len() != 0 ||
		!decodeSecretaryControl(ctx.Request.Body(), &request, 1024*1024, "source_id", "kind") {
		writeError(ctx, http.StatusBadRequest, "Provide a session and operation kind")
		return
	}
	data, err := serviceFromContext(ctx).secretaryControl.Prepare(c, requestMetadata(ctx), request)
	writeEndpointResult(ctx, http.StatusCreated, data, err)
}

func GetSecretaryOperation(c context.Context, ctx *app.RequestContext) {
	if ctx.QueryArgs().Len() != 0 {
		writeError(ctx, http.StatusBadRequest, "Operation reads do not accept query parameters")
		return
	}
	data, err := serviceFromContext(
		ctx,
	).secretaryControl.GetOperation(
		c,
		requestMetadata(ctx),
		ctx.Param("operation_id"),
	)
	writeEndpointResult(ctx, http.StatusOK, data, err)
}

func SubmitSecretaryOperation(c context.Context, ctx *app.RequestContext) {
	var request domain.SecretaryOperationSubmit
	if ctx.QueryArgs().Len() != 0 ||
		!decodeSecretaryControl(
			ctx.Request.Body(),
			&request,
			1024*1024,
			"request_id",
			"expected_revision",
		) {
		writeError(
			ctx,
			http.StatusBadRequest,
			"Provide request_id, expected_revision and operation content",
		)
		return
	}
	data, err := serviceFromContext(
		ctx,
	).secretaryControl.Submit(
		c,
		requestMetadata(ctx),
		ctx.Param("operation_id"),
		request,
	)
	writeEndpointResult(ctx, http.StatusOK, data, err)
}

func secretaryGuidanceQuery(ctx *app.RequestContext) (string, string, bool) {
	valid := true
	seen := map[string]bool{}
	ctx.QueryArgs().VisitAll(func(key, _ []byte) {
		name := string(key)
		if seen[name] || name != "scope" && name != "source_id" {
			valid = false
		}
		seen[name] = true
	})
	return ctx.Query("scope"), ctx.Query("source_id"), valid
}

func decodeSecretaryControl(raw []byte, target any, maxBytes int, required ...string) bool {
	if len(raw) > maxBytes || !utf8.Valid(raw) {
		return false
	}
	fields := secretaryControlJSONFields(target)
	reader := json.NewDecoder(bytes.NewReader(raw))
	token, err := reader.Token()
	if err != nil || token != json.Delim('{') {
		return false
	}
	seen := map[string]bool{}
	for reader.More() {
		key, keyErr := reader.Token()
		name, ok := key.(string)
		if keyErr != nil || !ok || seen[name] || !fields[name] {
			return false
		}
		seen[name] = true
		var value json.RawMessage
		if reader.Decode(&value) != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return false
		}
	}
	if token, err = reader.Token(); err != nil || token != json.Delim('}') ||
		reader.Decode(new(any)) != io.EOF {
		return false
	}
	for _, name := range required {
		if !seen[name] {
			return false
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target) == nil && decoder.Decode(new(any)) == io.EOF
}

func secretaryControlJSONFields(target any) map[string]bool {
	fields := map[string]bool{}
	typeOf := reflect.TypeOf(target)
	if typeOf == nil || typeOf.Kind() != reflect.Pointer || typeOf.Elem().Kind() != reflect.Struct {
		return fields
	}
	typeOf = typeOf.Elem()
	for index := range typeOf.NumField() {
		name := strings.Split(typeOf.Field(index).Tag.Get("json"), ",")[0]
		if name != "" && name != "-" {
			fields[name] = true
		}
	}
	return fields
}
