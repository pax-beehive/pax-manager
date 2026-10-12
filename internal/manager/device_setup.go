package manager

import (
	"context"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/pax-beehive/pax-manager/internal/manager/paxd"
)

func (s *Service) deviceSetupConfig() paxd.SetupConfig {
	return paxd.SetupConfig{
		DownloadURL: s.cfg.PaxdSetupDownloadURL,
		MachineURL:  s.cfg.PaxdSetupMachineURL,
	}
}

// GetDeviceSetup is routed to the authenticated account's regional Manager.
func GetDeviceSetup(c context.Context, ctx *app.RequestContext) {
	s := serviceFromContext(ctx)
	if _, _, err := s.userapi.CurrentUser(c, requestMetadata(ctx)); err != nil {
		writeEndpointError(ctx, err)
		return
	}
	info, err := s.deviceSetupConfig().Info()
	ctx.Header("Cache-Control", "no-store")
	if err != nil {
		writeError(
			ctx,
			http.StatusServiceUnavailable,
			"Device setup is not configured for this region.",
		)
		return
	}
	writeData(ctx, http.StatusOK, info)
}

func (s *Service) handlePaxdSetupScript(ctx *app.RequestContext) {
	script, err := s.deviceSetupConfig().Script()
	ctx.Header("Cache-Control", "no-store")
	if err != nil {
		writeError(
			ctx,
			http.StatusServiceUnavailable,
			"Device setup is not configured for this region.",
		)
		return
	}
	ctx.Header("Content-Type", "text/x-shellscript; charset=utf-8")
	ctx.Header("X-Content-Type-Options", "nosniff")
	ctx.SetStatusCode(http.StatusOK)
	ctx.SetBodyString(script)
}
