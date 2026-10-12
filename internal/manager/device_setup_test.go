package manager

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pax-beehive/pax-manager/internal/manager/paxd"
)

func TestDeviceSetupMetadataAndPublicScriptUseConfiguredRegion(t *testing.T) {
	for _, region := range []string{"us", "hk", "new-region"} {
		t.Run(region, func(t *testing.T) {
			srv, _ := testServer(t, "owner@example.com")
			srv.cfg.PaxdSetupDownloadURL = "https://" + region + "-artifacts.example.test"
			srv.cfg.PaxdSetupMachineURL = "https://" + region + "-machine.example.test"
			req := httptest.NewRequest(http.MethodGet, "/api/v1/user/self/device-setup", nil)
			req.Header.Set("X-User-Email", "owner@example.com")
			req.Host = "attacker.example"
			rec := httptest.NewRecorder()
			srv.routes().ServeHTTP(rec, req)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			info := decodeData[paxd.SetupInfo](t, rec.Body.Bytes())
			require.Equal(t, srv.cfg.PaxdSetupMachineURL, info.MachineURL)
			require.Equal(
				t,
				srv.cfg.PaxdSetupDownloadURL+"/api/v1/public/paxd/install.sh?setup=1",
				info.ScriptURL,
			)
			require.Equal(t, "no-store", rec.Header().Get("Cache-Control"))
			req = httptest.NewRequest(http.MethodGet, "/api/v1/public/paxd/install.sh?setup=1", nil)
			rec = httptest.NewRecorder()
			srv.routes().ServeHTTP(rec, req)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Contains(t, rec.Header().Get("Content-Type"), "text/x-shellscript")
			require.Contains(t, rec.Body.String(), srv.cfg.PaxdSetupMachineURL)
			require.NotContains(t, rec.Body.String(), "attacker.example")
		})
	}
}

func TestDeviceSetupFailsClosedWhenNotConfigured(t *testing.T) {
	srv, _ := testServer(t, "owner@example.com")
	for _, path := range []string{"/api/v1/user/self/device-setup", "/api/v1/public/paxd/install.sh?setup=1"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("X-User-Email", "owner@example.com")
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, req)
		require.Equal(t, http.StatusServiceUnavailable, rec.Code)
	}
}
