// Package nodepolicy records the reviewed public machine API surface.
package nodepolicy

import (
	_ "embed"
	"encoding/json"
	"strings"
)

// Route describes a method and router template, not a prefix authorization grant.
type Route struct {
	Method        string `json:"method"`
	Path          string `json:"path"`
	Auth          string `json:"auth"`
	OwnerBoundary string `json:"owner_boundary"`
	RateClass     string `json:"rate_class"`
	Caller        string `json:"caller"`
}

//go:embed routes.json
var manifest []byte

var routes = readRoutes()

func readRoutes() []Route {
	var result []Route
	if err := json.Unmarshal(manifest, &result); err != nil {
		panic(err)
	}
	return result
}

// Routes returns a copy so callers cannot change the approved surface.
func Routes() []Route {
	return append([]Route(nil), routes...)
}

// Managed includes unknown paths in machine namespaces, which fail closed.
func Managed(path string) bool {
	return path == "/api/v1/node" || strings.HasPrefix(path, "/api/v1/node/") ||
		path == "/api/v1/agent" || strings.HasPrefix(path, "/api/v1/agent/")
}

// Allowed matches the router's method and full template after route resolution.
// Authentication and resource ownership remain the endpoint's responsibility.
func Allowed(method, template string) bool {
	for _, route := range routes {
		if method == route.Method && template == route.Path {
			return true
		}
	}
	return false
}
