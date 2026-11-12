package manager

func addVoicePaths(doc map[string]any) {
	paths, ok := doc["paths"].(map[string]any)
	if !ok {
		paths = map[string]any{}
		doc["paths"] = paths
	}
	userParameter := []map[string]any{{
		"name":        "user_id",
		"in":          "path",
		"required":    true,
		"schema":      map[string]string{"type": "string"},
		"description": "User ID or self. The authenticated principal is always used.",
	}}
	security := []map[string][]string{{"cloudflareAccess": {}}}
	paths[openAPIUserVoiceConfigPath] = map[string]any{"get": map[string]any{
		"tags":    []string{"voice"},
		"summary": "Read voice input availability",
		"description": "Returns whether this manager instance offers voice input to the caller. " +
			"When enabled, data carries provider, sample_rate, encoding (pcm_s16le) and " +
			"max_duration_seconds. Each instance selects its own provider.",
		"security":   security,
		"parameters": userParameter,
		"responses": map[string]any{
			"200": map[string]string{"description": "Voice input availability."},
			"401": map[string]string{"description": "Authentication failed."},
		},
	}}
	paths[openAPIUserVoiceStreamPath] = map[string]any{"get": map[string]any{
		"tags":    []string{"voice"},
		"summary": "Stream dictation audio",
		"description": "Upgrades to a WebSocket for one dictation session. The client sends " +
			"{\"type\":\"start\",\"keywords\":[...],\"context\":[...]} (context is recent " +
			"conversation text, oldest first), then binary frames of mono pcm_s16le audio " +
			"at the advertised sample rate, then {\"type\":\"stop\"} or " +
			"{\"type\":\"cancel\"}. The server sends ready {provider, sample_rate, " +
			"encoding}, transcript {final, partial} snapshots that replace the whole " +
			"transcript, then done {final} or error {code, message}, and closes. Error " +
			"codes are bad_request, provider_unavailable, provider_failed and timeout.",
		"x-websocket": true,
		"security":    security,
		"parameters":  userParameter,
		"responses": map[string]any{
			"101": map[string]string{"description": "Dictation session established."},
			"401": map[string]string{"description": "Authentication failed."},
			"403": map[string]string{"description": "Voice input is limited to administrators."},
			"503": map[string]string{"description": "Voice input is not configured."},
		},
	}}
}
