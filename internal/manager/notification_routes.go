package manager

import (
	"bytes"
	"context"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/domain"
	"github.com/pax-beehive/pax-manager/internal/manager/userapi"
)

func ListNotificationSubscriptions(c context.Context, ctx *app.RequestContext) {
	serveNotificationRequest(c, ctx)
}
func ListNotificationReceivers(c context.Context, ctx *app.RequestContext) {
	serveNotificationRequest(c, ctx)
}
func GetNotificationConfiguration(c context.Context, ctx *app.RequestContext) {
	serveNotificationRequest(c, ctx)
}
func RegisterNotificationReceiver(c context.Context, ctx *app.RequestContext) {
	serveNotificationRequest(c, ctx)
}
func RevokeNotificationReceiver(c context.Context, ctx *app.RequestContext) {
	serveNotificationRequest(c, ctx)
}

func serveNotificationRequest(c context.Context, ctx *app.RequestContext) {
	s := serviceFromContext(ctx)
	api := &userapi.NotificationHandler{
		Registry: s.notificationRegistry, Region: s.cfg.Region,
		PublicKey: s.cfg.NotificationPublicKey, KeyID: s.cfg.NotificationKeyID, Now: s.clock,
		Principal: func(r *http.Request) (domain.UserPrincipal, error) {
			return s.auth.Principal(r.Context(), httpRequestMetadata(r))
		},
	}
	request, err := http.NewRequestWithContext(
		c,
		string(ctx.Method()),
		ctx.URI().String(),
		bytes.NewReader(ctx.Request.Body()),
	)
	if err != nil {
		ctx.SetStatusCode(http.StatusBadRequest)
		return
	}
	ctx.Request.Header.VisitAll(
		func(key, value []byte) { request.Header.Add(string(key), string(value)) },
	)
	response := &notificationBufferedResponse{header: make(http.Header), status: http.StatusOK}
	api.ServeHTTP(response, request)
	for key, values := range response.header {
		for _, value := range values {
			ctx.Response.Header.Add(key, value)
		}
	}
	ctx.SetStatusCode(response.status)
	ctx.Response.SetBody(response.body.Bytes())
}

// Registry responses are bounded JSON, so they do not need a streaming connection adapter.
type notificationBufferedResponse struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *notificationBufferedResponse) Header() http.Header            { return r.header }
func (r *notificationBufferedResponse) WriteHeader(status int)         { r.status = status }
func (r *notificationBufferedResponse) Write(body []byte) (int, error) { return r.body.Write(body) }
