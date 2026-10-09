package manager

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGivenRegionalManagerWhenReleaseChecksHealthThenCapabilitiesIdentifyRegion(t *testing.T) {
	for _, region := range []string{"us", "hk"} {
		t.Run(region, func(t *testing.T) {
			srv, _ := testServer(t, "owner@example.com")
			srv.cfg.Region = region
			rec := httptest.NewRecorder()
			srv.routes().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
			assert.Equal(t, http.StatusOK, rec.Code)
			data := decodeData[struct {
				Status       string   `json:"status"`
				Region       string   `json:"region"`
				Capabilities []string `json:"region_directory_capabilities"`
			}](t, rec.Body.Bytes())
			assert.Equal(t, "ok", data.Status)
			assert.Equal(t, region, data.Region)
			assert.ElementsMatch(
				t,
				[]string{"provision-v1", "browser-v1", "paxl-login-v1", "customer-analytics-v1"},
				data.Capabilities,
			)
		})
	}
}
