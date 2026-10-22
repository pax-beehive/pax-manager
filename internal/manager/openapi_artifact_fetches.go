package manager

func addArtifactFetchPaths(doc map[string]any) {
	paths, ok := doc["paths"].(map[string]any)
	if !ok {
		paths = map[string]any{}
		doc["paths"] = paths
	}
	pathParams := []map[string]any{
		{
			"name":        "user_id",
			"in":          "path",
			"required":    true,
			"schema":      map[string]string{"type": "string"},
			"description": "User identifier or self.",
		},
		{
			"name":     "session_id",
			"in":       "path",
			"required": true,
			"schema":   map[string]string{"type": "string"},
		},
	}
	sharedErrors := map[string]any{
		"400": map[string]string{"description": "Missing, relative, or malformed path."},
		"401": map[string]string{"description": "User authentication failed."},
		"404": map[string]string{"description": "Session not found for this user."},
		"409": map[string]string{"description": "Session is end-to-end encrypted."},
		"502": map[string]string{"description": "paxd returned an invalid response."},
		"503": map[string]string{"description": "Session node control tunnel is not connected."},
		"504": map[string]string{"description": "paxd did not answer in time."},
	}
	withResponses := func(extra map[string]any) map[string]any {
		responses := map[string]any{}
		for code, response := range sharedErrors {
			responses[code] = response
		}
		for code, response := range extra {
			responses[code] = response
		}
		return responses
	}
	paths[openAPIUserArtifactFetchCheck] = map[string]any{
		"get": map[string]any{
			"tags":    []string{"user"},
			"summary": "Check whether a local session file can be fetched",
			"description": "Relays an artifact_fetch.check query to the session node. " +
				"Policy denials return HTTP 200 with allowed=false and a denial.",
			"security": []map[string][]string{{"cloudflareAccess": {}}},
			"parameters": append(append([]map[string]any{}, pathParams...), map[string]any{
				"name": "path", "in": "query", "required": true,
				"schema":      map[string]string{"type": "string"},
				"description": "Absolute path on the session node.",
			}),
			"responses": withResponses(map[string]any{
				"200": map[string]string{
					"description": "Check result: allowed, path, filename, size_bytes or denial.",
				},
			}),
		},
	}
	paths[openAPIUserArtifactFetches] = map[string]any{
		"post": map[string]any{
			"tags":    []string{"user"},
			"summary": "Fetch a local session file as an artifact publication",
			"description": "Sends an artifact.fetch command to the session node. Accepted fetches " +
				"return HTTP 202 with the queued publication; policy denials return HTTP 200 " +
				"with accepted=false and a denial.",
			"security":   []map[string][]string{{"cloudflareAccess": {}}},
			"parameters": pathParams,
			"requestBody": map[string]any{
				"required": true,
				"content": map[string]any{
					"application/json": map[string]any{
						"schema": map[string]any{
							"type":     "object",
							"required": []string{"path"},
							"properties": map[string]any{
								"path": map[string]string{"type": "string"},
							},
						},
					},
				},
			},
			"responses": withResponses(map[string]any{
				"200": map[string]string{"description": "Denied by node policy."},
				"202": map[string]string{"description": "Accepted; publication is queued."},
			}),
		},
	}
}
