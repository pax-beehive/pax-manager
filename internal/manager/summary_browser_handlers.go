package manager

import (
	"context"
	"net/http"
	"strconv"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func ListDeveloperSummaries(c context.Context, ctx *app.RequestContext) {
	query, ok := developerSummaryListQuery(ctx)
	if !ok {
		writeError(ctx, http.StatusBadRequest, "Use cursor and a limit between 1 and 100")
		return
	}
	data, err := serviceFromContext(
		ctx,
	).workspaceSearch.ListDeveloperSummaries(
		c,
		requestMetadata(ctx),
		query,
	)
	writeEndpointResult(ctx, http.StatusOK, data, err)
}

func ReadDeveloperSummary(c context.Context, ctx *app.RequestContext) {
	revision, ok := developerSummaryRevision(ctx)
	if !ok {
		writeError(ctx, http.StatusBadRequest, "Use a nonnegative integer revision")
		return
	}
	data, err := serviceFromContext(
		ctx,
	).workspaceSearch.ReadDeveloperSummary(
		c,
		requestMetadata(ctx),
		ctx.Param("summary_id"),
		revision,
	)
	writeEndpointResult(ctx, http.StatusOK, data, err)
}

func developerSummaryListQuery(ctx *app.RequestContext) (domain.SummaryListQuery, bool) {
	query := domain.SummaryListQuery{}
	seen := map[string]bool{}
	valid := true
	ctx.QueryArgs().VisitAll(func(key, value []byte) {
		name := string(key)
		if seen[name] {
			valid = false
		}
		seen[name] = true
		switch name {
		case "cursor":
			query.Cursor = string(value)
		case "limit":
			number, ok := developerUnsignedInteger(string(value))
			if !ok || number < 1 || number > 100 {
				valid = false
			}
			query.Limit = int(number)
		default:
			valid = false
		}
	})
	return query, valid
}

func developerSummaryRevision(ctx *app.RequestContext) (int64, bool) {
	var revision int64
	valid, seen := true, false
	ctx.QueryArgs().VisitAll(func(key, value []byte) {
		if string(key) != "revision" || seen {
			valid = false
			return
		}
		seen = true
		var ok bool
		revision, ok = developerUnsignedInteger(string(value))
		if !ok {
			valid = false
		}
	})
	return revision, valid
}

func developerUnsignedInteger(value string) (int64, bool) {
	if value == "" {
		return 0, false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, false
		}
	}
	number, err := strconv.ParseInt(value, 10, 64)
	return number, err == nil
}
