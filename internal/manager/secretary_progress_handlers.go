package manager

import (
	"context"
	"net/http"
	"strconv"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
)

func GetSecretaryProgress(c context.Context, ctx *app.RequestContext) {
	query, ok := secretaryProgressQuery(ctx)
	if !ok {
		writeError(ctx, http.StatusBadRequest, "Use cursor and a limit between 1 and 100")
		return
	}
	data, err := serviceFromContext(ctx).secretary.Progress(c, requestMetadata(ctx), query)
	writeEndpointResult(ctx, http.StatusOK, data, err)
}

func secretaryProgressQuery(ctx *app.RequestContext) (domain.SecretaryProgressQuery, bool) {
	query := domain.SecretaryProgressQuery{}
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
			if len(value) == 0 {
				valid = false
			}
			for _, character := range value {
				if character < '0' || character > '9' {
					valid = false
				}
			}
			var err error
			query.Limit, err = strconv.Atoi(string(value))
			if err != nil || query.Limit < 1 || query.Limit > 100 {
				valid = false
			}
		default:
			valid = false
		}
	})
	return query, valid
}
