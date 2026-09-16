package manager

func sessionMessageDetailOperation() map[string]any {
	parameters := make([]map[string]any, 0, 8)
	for _, name := range []string{"user_id", "session_id", "message_id"} {
		parameters = append(parameters, map[string]any{"name": name, "in": "path", "required": true,
			"schema": map[string]string{"type": "string"}})
	}
	parameters = append(
		parameters,
		map[string]any{
			"name": "section",
			"in":   "query",
			"schema": map[string]any{
				"type":    "string",
				"enum":    []string{"input", "output"},
				"default": "output",
			},
		},
		map[string]any{
			"name":   "offset",
			"in":     "query",
			"schema": map[string]any{"type": "integer", "minimum": 0, "maximum": 1000000000},
		},
		map[string]any{
			"name": "limit",
			"in":   "query",
			"schema": map[string]any{
				"type":    "integer",
				"minimum": 1,
				"maximum": 16384,
				"default": 16384,
			},
		},
		map[string]any{
			"name":        "revision",
			"in":          "query",
			"schema":      map[string]string{"type": "string"},
			"description": "Required for nonzero offset. Use the revision from the first page.",
		},
	)
	return map[string]any{"get": map[string]any{
		"tags": []string{"user"}, "summary": "Read message details on demand",
		"description": "Authorizes the session and its message. Returns message_id, section, format (text or json), text, revision, next_offset and has_more in data. Offsets and limits count Unicode code points. JSON pages must be concatenated before decoding. Output includes ordered text parts when present, otherwise tool output/content; input is the stored tool input. Maximum 16384 code points per response, including legacy oversized JSON. A changed revision returns 409; restart from offset zero. No raw ACP frame or duplicate payload is returned.",
		"security":    []map[string][]string{{"cloudflareAccess": {}}}, "parameters": parameters,
		"responses": map[string]any{
			"200": map[string]string{"description": "One bounded detail page."},
			"400": map[string]string{"description": "Invalid cursor or section."},
			"401": map[string]string{"description": "Authentication failed."},
			"404": map[string]string{
				"description": "Session or message not found or inaccessible.",
			},
			"409": map[string]string{"description": "Message changed; reload details."},
		},
	}}
}
