package manager

// addRuntimeConfigPaths documents the runtime config admin API and the user
// features endpoint. Errors use the standard envelope with data.error set to
// one of unknown_key, validation_failed, version_conflict, not_admin, or
// feature_unavailable. Credentials are environment-only and never appear here.
func addRuntimeConfigPaths(doc map[string]any) {
	paths, ok := doc["paths"].(map[string]any)
	if !ok {
		paths = map[string]any{}
		doc["paths"] = paths
	}
	security := []map[string][]string{{"cloudflareAccess": {}}}
	adminErrors := func(extra map[string]any) map[string]any {
		responses := map[string]any{
			"401": map[string]string{"description": "Authentication failed."},
			"403": map[string]string{"description": "data.error=not_admin."},
		}
		for code, response := range extra {
			responses[code] = response
		}
		return responses
	}
	keyParameter := []map[string]any{{
		"name": "key", "in": "path", "required": true,
		"schema":      map[string]string{"type": "string"},
		"description": "Registered key such as search.url or feature.secretary.",
	}}
	writeErrors := map[string]any{
		"400": map[string]string{
			"description": "data.error=validation_failed with data.field_errors.",
		},
		"404": map[string]string{"description": "data.error=unknown_key."},
		"409": map[string]string{"description": "data.error=version_conflict with data.version."},
	}
	paths[routeAdminConfig] = map[string]any{"get": map[string]any{
		"tags": []string{"runtime-config"}, "summary": "List runtime config entries",
		"description": "Returns region, revision, refresh health, and every registered " +
			"entry with value, display, source (db, env, default), version, and apply " +
			"mode. API keys are environment-only and are never listed.",
		"security": security,
		"responses": adminErrors(map[string]any{
			"200": map[string]string{"description": "Entries for this region."},
		}),
	}}
	paths["/api/v1/admin/config/{key}"] = map[string]any{
		"put": map[string]any{
			"tags": []string{"runtime-config"}, "summary": "Set a runtime config override",
			"description": "Body: {value, expected_version, reason}. expected_version is 0 when " +
				"the key has no override. reason is required, at most 500 characters.",
			"security": security, "parameters": keyParameter,
			"responses": adminErrors(mergeResponses(writeErrors, map[string]any{
				"200": map[string]string{"description": "The updated entry."},
			})),
		},
		"delete": map[string]any{
			"tags": []string{"runtime-config"}, "summary": "Remove a runtime config override",
			"description": "Query: expected_version, reason. The entry falls back to its " +
				"environment value or default.",
			"security": security, "parameters": keyParameter,
			"responses": adminErrors(mergeResponses(writeErrors, map[string]any{
				"200": map[string]string{"description": "The entry after reset."},
			})),
		},
	}
	paths[routeAdminConfigTest] = map[string]any{"post": map[string]any{
		"tags": []string{"runtime-config"}, "summary": "Test a config group",
		"description": "Body: {group, overrides}. Tests the effective values plus unsaved " +
			"overrides with a 10 second timeout. Never writes. Returns {ok, latency_ms, message}.",
		"security": security,
		"responses": adminErrors(map[string]any{
			"200": map[string]string{"description": "Test result."},
			"400": map[string]string{"description": "Unknown group or invalid override."},
		}),
	}}
	paths[routeAdminConfigAudit] = map[string]any{"get": map[string]any{
		"tags": []string{"runtime-config"}, "summary": "List runtime config audit records",
		"description": "Query: key, cursor, limit (default 50, max 200). Returns " +
			"{items, next_cursor}.",
		"security": security,
		"responses": adminErrors(map[string]any{
			"200": map[string]string{"description": "Audit page, newest first."},
		}),
	}}
	paths["/api/v1/admin/config/audit/{id}/revert"] = map[string]any{"post": map[string]any{
		"tags": []string{"runtime-config"}, "summary": "Revert to an audit record's old value",
		"description": "Body: {expected_version, reason}. Restores the recorded old value, " +
			"and records a revert audit entry.",
		"security": security,
		"responses": adminErrors(mergeResponses(writeErrors, map[string]any{
			"200": map[string]string{"description": "The entry after revert."},
		})),
	}}
	paths[routeUserFeatures] = map[string]any{"get": map[string]any{
		"tags": []string{"runtime-config"}, "summary": "Read feature availability",
		"description": "Returns {revision, features} with booleans only. as=user evaluates " +
			"an administrator as an ordinary user; it is ignored for other callers. " +
			"Served from the in-memory snapshot, so it does not fail on database errors.",
		"security": security,
		"responses": map[string]any{
			"200": map[string]string{"description": "Feature decisions for the caller."},
			"401": map[string]string{"description": "Authentication failed."},
		},
	}}
}

func mergeResponses(base, extra map[string]any) map[string]any {
	out := map[string]any{}
	for code, response := range base {
		out[code] = response
	}
	for code, response := range extra {
		out[code] = response
	}
	return out
}
