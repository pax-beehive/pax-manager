package runtimeconfig

import (
	"context"
	"fmt"
	"net/http"

	"github.com/pax-beehive/pax-manager/internal/manager/search"
)

// testSearch checks reachability and credentials. The API key comes from
// SEARCH_API_KEY; messages never include it.
func testSearch(ctx context.Context, values ResolvedGroup) TestResult {
	cfg := search.Config{
		URL:    values.String("search.url"),
		Index:  values.String("search.index"),
		APIKey: values.String("SEARCH_API_KEY"),
	}
	if cfg.URL == "" {
		return TestResult{Message: "search.url is empty; workspace search is disabled"}
	}
	if cfg.Index == "" {
		cfg.Index = "pax-search-v1"
	}
	client, err := search.NewClient(cfg)
	if err != nil {
		return TestResult{Message: err.Error()}
	}
	exists, err := client.Ping(ctx)
	switch status := search.StatusCode(err); {
	case err == nil && exists:
		return TestResult{OK: true, Message: "connected; index exists"}
	case err == nil:
		return TestResult{
			OK:      true,
			Message: "connected; index does not exist yet and will be created",
		}
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return TestResult{Message: fmt.Sprintf("search rejected the credentials (HTTP %d)", status)}
	case status != 0:
		return TestResult{Message: fmt.Sprintf("search returned HTTP %d", status)}
	case ctx.Err() != nil:
		return TestResult{Message: "search did not respond within 10 seconds"}
	default:
		return TestResult{Message: "search is unreachable"}
	}
}
