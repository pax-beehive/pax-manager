package manager

import (
	"context"
	"embed"
	"encoding/json"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
)

//go:generate go run ../../cmd/openapi-gen -in ../../api/pax_manager.thrift -out openapi_generated.go

//go:embed openapi_generated.json
var openAPIFS embed.FS

func OpenAPIUI(c context.Context, ctx *app.RequestContext) {
	serviceFromContext(ctx).handleOpenAPIUI(c, ctx)
}

func OpenAPIJSON(c context.Context, ctx *app.RequestContext) {
	serviceFromContext(ctx).handleOpenAPIJSON(c, ctx)
}

func (s *Service) handleOpenAPIUI(_ context.Context, ctx *app.RequestContext) {
	ctx.Data(http.StatusOK, "text/html; charset=utf-8", []byte(openAPIHTML))
}

func (s *Service) handleOpenAPIJSON(_ context.Context, ctx *app.RequestContext) {
	doc, err := openAPIDocument(requestBaseURL(ctx))
	if err != nil {
		writeError(ctx, http.StatusInternalServerError, "openapi document is invalid")
		return
	}
	ctx.Data(http.StatusOK, "application/json; charset=utf-8", doc)
}

func requestBaseURL(ctx *app.RequestContext) string {
	host := string(ctx.GetHeader("X-Forwarded-Host"))
	if host == "" {
		host = string(ctx.Request.Host())
	}
	if host == "" {
		return "/"
	}
	proto := string(ctx.GetHeader("X-Forwarded-Proto"))
	if proto == "" {
		proto = "http"
	}
	return proto + "://" + host
}

func openAPIDocument(serverURL string) ([]byte, error) {
	raw, err := openAPIFS.ReadFile("openapi_generated.json")
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, err
	}
	doc["servers"] = []map[string]string{{"url": serverURL}}
	addACPWebSocketPaths(doc)
	addAgentFleetPaths(doc)
	addSessionConfigurationPaths(doc)
	addSessionHistoryPath(doc)
	addNodeConversationDeliveryPath(doc)
	addArtifactPublicationPaths(doc)
	addPaxdArtifactPaths(doc)
	return json.MarshalIndent(doc, "", "  ")
}

func addAgentFleetPaths(doc map[string]any) {
	paths, ok := doc["paths"].(map[string]any)
	if !ok {
		paths = map[string]any{}
		doc["paths"] = paths
	}
	userPathParam := map[string]any{
		"name":        "user_id",
		"in":          "path",
		"required":    true,
		"schema":      map[string]string{"type": "string"},
		"description": "User ID or self.",
	}
	agentPathParam := map[string]any{
		"name":        "agent_id",
		"in":          "path",
		"required":    true,
		"schema":      map[string]string{"type": "string"},
		"description": "Agent identifier.",
	}
	paths[openAPIUserAgentsPath] = map[string]any{
		"get": map[string]any{
			"tags":        []string{"user"},
			"summary":     "List agents",
			"description": "Lists agents visible to the current user.",
			"security":    []map[string][]string{{"cloudflareAccess": {}}},
			"parameters":  []map[string]any{userPathParam},
			"responses": map[string]any{
				"200": map[string]string{"description": "Visible agents."},
				"401": map[string]string{"description": "User authentication failed."},
			},
		},
	}
	paths[openAPIUserAgentPath] = map[string]any{
		"get": map[string]any{
			"tags":        []string{"user"},
			"summary":     "Get agent",
			"description": "Gets one agent visible to the current user.",
			"security":    []map[string][]string{{"cloudflareAccess": {}}},
			"parameters":  []map[string]any{userPathParam, agentPathParam},
			"responses": map[string]any{
				"200": map[string]string{"description": "Agent."},
				"401": map[string]string{"description": "User authentication failed."},
				"404": map[string]string{"description": "Agent not found."},
			},
		},
		"delete": map[string]any{
			"tags":        []string{"user"},
			"summary":     "Delete agent",
			"description": "Soft-deletes an agent owned by the current user, hides it from fleet lists, and revokes its API key.",
			"security":    []map[string][]string{{"cloudflareAccess": {}}},
			"parameters":  []map[string]any{userPathParam, agentPathParam},
			"responses": map[string]any{
				"200": map[string]string{"description": "Deleted agent."},
				"401": map[string]string{"description": "User authentication failed."},
				"404": map[string]string{"description": "Agent not found."},
			},
		},
	}
	paths[openAPIUserAgentPermissionCatalog] = map[string]any{
		"get": map[string]any{
			"tags":        []string{"user"},
			"summary":     "Get agent permission catalog",
			"description": "Returns the owner-scoped permission choices resolved from live observations or versioned server profiles.",
			"security":    []map[string][]string{{"cloudflareAccess": {}}},
			"parameters":  []map[string]any{userPathParam, agentPathParam},
			"responses": map[string]any{
				"200": map[string]string{"description": "Permission catalog."},
				"401": map[string]string{"description": "User authentication failed."},
				"404": map[string]string{"description": "Owned agent not found."},
			},
		},
	}
}
func addSessionConfigurationPaths(doc map[string]any) {
	paths, ok := doc["paths"].(map[string]any)
	if !ok {
		paths = map[string]any{}
		doc["paths"] = paths
	}
	pathParam := func(name, description string) map[string]any {
		return map[string]any{
			"name":        name,
			"in":          "path",
			"required":    true,
			"schema":      map[string]string{"type": "string"},
			"description": description,
		}
	}
	parameters := []map[string]any{
		pathParam("user_id", "User ID or self."),
		pathParam("node_id", "Node identifier."),
		pathParam("agent_id", "Agent identifier."),
		pathParam("session_id", "Session identifier."),
	}
	responses := map[string]any{
		"200": map[string]string{"description": "Current durable ACP session configuration."},
		"401": map[string]string{"description": "User authentication failed."},
		"404": map[string]string{"description": "Session not found."},
		"409": map[string]string{
			"description": "Configuration is unavailable or cannot be changed.",
		},
		"502": map[string]string{
			"description": "Agent rejected or returned an incomplete configuration response.",
		},
	}
	paths[openAPIUserSessionConfig] = map[string]any{
		"get": map[string]any{
			"tags":        []string{"user"},
			"summary":     "Get session configuration",
			"description": "Returns the latest standard ACP config options, legacy read-only model list, and commands snapshot observed for a plaintext session. The optional commands object contains available_commands (the complete ACP command objects, including input and _meta) and observed_at. It is absent until available_commands_update is received; an empty array means the agent cleared the list. No historical backfill or active ACP command query is performed.",
			"security":    []map[string][]string{{"cloudflareAccess": {}}},
			"parameters":  parameters,
			"responses":   responses,
		},
	}
	paths[openAPIUserSessionConfigRefresh] = map[string]any{
		"post": map[string]any{
			"tags":        []string{"user"},
			"summary":     "Refresh session configuration",
			"description": "Re-applies the current non-permission config value so the ACP agent returns a complete fresh configOptions snapshot. This is not a read-only operation.",
			"security":    []map[string][]string{{"cloudflareAccess": {}}},
			"parameters":  parameters,
			"responses":   responses,
		},
	}
	optionParameters := append(
		append([]map[string]any(nil), parameters...),
		pathParam("config_id", "ACP session config option identifier."),
	)
	paths[openAPIUserSessionConfigOption] = map[string]any{
		"patch": map[string]any{
			"tags":        []string{"user"},
			"summary":     "Set session configuration option",
			"description": "Applies one currently advertised non-permission select or boolean option and persists the complete returned configOptions snapshot.",
			"security":    []map[string][]string{{"cloudflareAccess": {}}},
			"parameters":  optionParameters,
			"requestBody": map[string]any{
				"required": true,
				"content": map[string]any{
					"application/json": map[string]any{
						"schema": map[string]any{
							"type":     "object",
							"required": []string{"value"},
							"properties": map[string]any{
								"value": map[string]any{
									"oneOf": []map[string]string{
										{"type": "string"},
										{"type": "boolean"},
									},
								},
							},
						},
					},
				},
			},
			"responses": responses,
		},
	}
}

func addSessionHistoryPath(doc map[string]any) {
	paths, ok := doc["paths"].(map[string]any)
	if !ok {
		paths = map[string]any{}
		doc["paths"] = paths
	}
	paths[openAPISessionHistoryPath] = sessionHistoryPathOperation(true)
	paths[openAPIUserSessionHistoryPath] = sessionHistoryPathOperation(false)
}

func sessionHistoryPathOperation(agentScoped bool) map[string]any {
	parameters := []map[string]any{
		{
			"name":        "user_id",
			"in":          "path",
			"required":    true,
			"schema":      map[string]string{"type": "string"},
			"description": "User ID or self.",
		},
	}
	if agentScoped {
		parameters = append(parameters, map[string]any{
			"name":        "agent_id",
			"in":          "path",
			"required":    true,
			"schema":      map[string]string{"type": "string"},
			"description": "Agent identifier.",
		})
	}
	parameters = append(parameters,
		map[string]any{
			"name":        "session_id",
			"in":          "path",
			"required":    true,
			"schema":      map[string]string{"type": "string"},
			"description": "Session identifier.",
		},
		map[string]any{
			"name":        "limit",
			"in":          "query",
			"required":    false,
			"schema":      map[string]any{"type": "integer", "maximum": 1000},
			"description": "Maximum number of history messages to return. Defaults to 1000.",
		},
		map[string]any{
			"name":        "before_id",
			"in":          "query",
			"required":    false,
			"schema":      map[string]any{"type": "integer", "format": "int64", "minimum": 1},
			"description": "Returns messages with a database ID lower than this value. Use pagination.next_before_id to load older history.",
		},
	)
	summary := "List session history"
	notFoundDescription := "Session not found."
	if agentScoped {
		summary = "List agent session history"
		notFoundDescription = "Agent or session not found."
	}
	return map[string]any{
		"get": map[string]any{
			"tags":        []string{"user"},
			"summary":     summary,
			"description": "Lists a page of durable session history messages with their message parts. The first page contains the latest messages; responses are ordered chronologically.",
			"security":    []map[string][]string{{"cloudflareAccess": {}}},
			"parameters":  parameters,
			"responses": map[string]any{
				"200": map[string]string{"description": "Session history messages."},
				"401": map[string]string{"description": "User authentication failed."},
				"404": map[string]string{"description": notFoundDescription},
			},
		},
	}
}

func addNodeConversationDeliveryPath(doc map[string]any) {
	paths, ok := doc["paths"].(map[string]any)
	if !ok {
		paths = map[string]any{}
		doc["paths"] = paths
	}
	paths[openAPINodeConversationDeliver] = map[string]any{
		"post": map[string]any{
			"tags":        []string{"node"},
			"summary":     "Deliver conversation context to another agent",
			"description": "Node-scoped command endpoint used by paxd conversation MCP. The target can be a representative agent or the current session's active invocation.",
			"security":    []map[string][]string{{"nodeBearer": {}}},
			"requestBody": map[string]any{
				"required": true,
				"content": map[string]any{
					"application/json": map[string]any{
						"schema": map[string]any{
							"type":     "object",
							"required": []string{"target"},
							"properties": map[string]any{
								"source": map[string]any{
									"type":        "object",
									"description": "Required for representative and active_invocation delivery. For model-facing MCP calls, paxd should fill this from local context.",
									"properties": map[string]any{
										"agent_id": map[string]string{
											"type": "string",
										},
										"representative_agent_id": map[string]string{
											"type": "string",
										},
										"session_id": map[string]string{
											"type": "string",
										},
									},
								},
								"target": map[string]any{
									"type":        "object",
									"description": "Delivery target. kind=representative asks another representative agent; kind=active_invocation replies to the current session's active invocation.",
									"required":    []string{"kind"},
									"properties": map[string]any{
										"kind": map[string]any{
											"type": "string",
											"enum": []string{"representative", "active_invocation"},
										},
										"representative_agent_id": map[string]string{
											"type": "string",
										},
										"session_id": map[string]string{
											"type": "string",
										},
									},
								},
								"context": map[string]any{
									"type":        "object",
									"description": "Context inclusion policy. Omitted fields use server defaults; latest_response defaults to true.",
									"properties": map[string]any{
										"latest_response":   map[string]string{"type": "boolean"},
										"tool_calls":        map[string]string{"type": "boolean"},
										"reasoning_summary": map[string]string{"type": "boolean"},
										"artifacts":         map[string]string{"type": "boolean"},
									},
								},
								"instruction": map[string]string{"type": "string"},
								"reason":      map[string]string{"type": "string"},
							},
						},
					},
				},
			},
			"responses": map[string]any{
				"202": map[string]string{"description": "Delivery accepted."},
				"400": map[string]string{"description": "Invalid target or source."},
				"401": map[string]string{"description": "Node authentication failed."},
			},
		},
	}
}

func addACPWebSocketPaths(doc map[string]any) {
	paths, ok := doc["paths"].(map[string]any)
	if !ok {
		paths = map[string]any{}
		doc["paths"] = paths
	}

	agentTunnelPath := map[string]any{
		"get": map[string]any{
			"tags":        []string{"ACP"},
			"summary":     "Connect ACP agent tunnel",
			"description": "Upgrades to a WebSocket used by paxd to forward ACP JSON-RPC frames between pax-manager and a local ACP server.",
			"x-websocket": true,
			"security":    []map[string][]string{{"nodeBearer": {}}},
			"parameters": []map[string]any{
				{
					"name":        "agent_id",
					"in":          "query",
					"required":    false,
					"schema":      map[string]string{"type": "string"},
					"description": "Agent ID served by this paxd node.",
				},
				{
					"name":        "instance_id",
					"in":          "query",
					"required":    false,
					"schema":      map[string]string{"type": "string"},
					"description": "Local runtime instance ID. Defaults to default.",
				},
				{
					"name":        "session_id",
					"in":          "query",
					"required":    false,
					"schema":      map[string]string{"type": "string"},
					"description": "ACP session ID served by this tunnel.",
				},
			},
			"responses": map[string]any{
				"101": map[string]string{"description": "WebSocket tunnel established."},
				"401": map[string]string{"description": "Node or agent authentication failed."},
			},
		},
	}
	paths[routeAgentACPTunnel] = agentTunnelPath

	v1UserTunnelGET := map[string]any{
		"get": map[string]any{
			"tags":        []string{"ACP"},
			"summary":     "Connect ACP user tunnel",
			"description": "Upgrades to a WebSocket used by an ACP client to exchange JSON-RPC frames with a connected paxd agent tunnel.",
			"x-websocket": true,
			"security":    []map[string][]string{{"cloudflareAccess": {}}},
			"parameters": []map[string]any{
				{
					"name":        "user_id",
					"in":          "path",
					"required":    true,
					"schema":      map[string]string{"type": "string"},
					"description": "User ID or self.",
				},
				{
					"name":        "agent_id",
					"in":          "path",
					"required":    true,
					"schema":      map[string]string{"type": "string"},
					"description": "Agent ID to connect to.",
				},
				{
					"name":        "session_id",
					"in":          "query",
					"required":    false,
					"schema":      map[string]string{"type": "string"},
					"description": "ACP session ID to connect to. Omit only for tunnels registered without a session ID.",
				},
			},
			"responses": map[string]any{
				"101": map[string]string{"description": "WebSocket tunnel established."},
				"401": map[string]string{"description": "User authentication failed."},
				"404": map[string]string{"description": "Agent tunnel is not connected."},
			},
		},
	}
	paths[openAPIUserACPTunnelPath] = v1UserTunnelGET

	v1UserSessionTunnelGET := map[string]any{
		"get": map[string]any{
			"tags":        []string{"ACP"},
			"summary":     "Connect ACP user tunnel for a session",
			"description": "Upgrades to a WebSocket used by an ACP client to exchange JSON-RPC frames with a connected paxd agent tunnel for one ACP session.",
			"x-websocket": true,
			"security":    []map[string][]string{{"cloudflareAccess": {}}},
			"parameters": []map[string]any{
				{
					"name":        "user_id",
					"in":          "path",
					"required":    true,
					"schema":      map[string]string{"type": "string"},
					"description": "User ID or self.",
				},
				{
					"name":        "agent_id",
					"in":          "path",
					"required":    true,
					"schema":      map[string]string{"type": "string"},
					"description": "Agent ID to connect to.",
				},
				{
					"name":        "session_id",
					"in":          "path",
					"required":    true,
					"schema":      map[string]string{"type": "string"},
					"description": "ACP session ID to connect to.",
				},
			},
			"responses": map[string]any{
				"101": map[string]string{"description": "WebSocket tunnel established."},
				"401": map[string]string{"description": "User authentication failed."},
				"404": map[string]string{"description": "Agent tunnel is not connected."},
			},
		},
	}
	paths[openAPIUserSessionACPTunnelPath] = v1UserSessionTunnelGET

	turnControlParameters := []map[string]any{
		{
			"name":        "user_id",
			"in":          "path",
			"required":    true,
			"schema":      map[string]string{"type": "string"},
			"description": "User ID or self.",
		},
		{
			"name":        "agent_id",
			"in":          "path",
			"required":    true,
			"schema":      map[string]string{"type": "string"},
			"description": "Agent ID to control.",
		},
		{
			"name":        "session_id",
			"in":          "path",
			"required":    true,
			"schema":      map[string]string{"type": "string"},
			"description": "Manager session ID to control.",
		},
	}
	turnCommandParameters := append([]map[string]any{}, turnControlParameters...)
	turnCommandParameters = append(turnCommandParameters, map[string]any{
		"name":        "Idempotency-Key",
		"in":          "header",
		"required":    false,
		"schema":      map[string]string{"type": "string"},
		"description": "Client supplied command id for request tracing.",
	})
	turnPromptBody := map[string]any{
		"required": true,
		"content": map[string]any{
			"application/json": map[string]any{
				"schema": map[string]any{
					"type":     "object",
					"required": []string{"input"},
					"properties": map[string]any{
						"input": map[string]string{"type": "string"},
					},
				},
			},
		},
	}
	paths[openAPIUserSessionTurnStopPath] = map[string]any{
		"post": map[string]any{
			"tags":        []string{"ACP"},
			"summary":     "Stop the active ACP turn for a session",
			"description": "Sends ACP session/cancel for the current active prompt turn.",
			"security":    []map[string][]string{{"cloudflareAccess": {}}},
			"parameters":  turnCommandParameters,
			"responses": map[string]any{
				"200": map[string]string{
					"description": "Stop request accepted or no active turn was present.",
				},
				"401": map[string]string{"description": "User authentication failed."},
				"404": map[string]string{"description": "Agent, session, or tunnel was not found."},
				"409": map[string]string{"description": "Session belongs to a different agent."},
			},
		},
	}
	paths[openAPIUserSessionTurnQueuePath] = map[string]any{
		"get": map[string]any{
			"tags":        []string{"ACP"},
			"summary":     "Get the queued ACP prompt turn",
			"description": "Returns the replaceable pending prompt for the session, or null data when no prompt is queued.",
			"security":    []map[string][]string{{"cloudflareAccess": {}}},
			"parameters":  turnControlParameters,
			"responses": map[string]any{
				"200": map[string]string{"description": "Current pending prompt or null data."},
				"401": map[string]string{"description": "User authentication failed."},
				"404": map[string]string{"description": "Agent or session was not found."},
			},
		},
		"post": map[string]any{
			"tags":        []string{"ACP"},
			"summary":     "Queue the next ACP prompt turn",
			"description": "Stores one replaceable pending prompt for the active session.",
			"security":    []map[string][]string{{"cloudflareAccess": {}}},
			"parameters":  turnCommandParameters,
			"requestBody": turnPromptBody,
			"responses": map[string]any{
				"200": map[string]string{"description": "Pending prompt queued or replaced."},
				"401": map[string]string{"description": "User authentication failed."},
				"404": map[string]string{"description": "Agent or session was not found."},
				"409": map[string]string{"description": "Session has no active turn."},
			},
		},
		"patch": map[string]any{
			"tags":        []string{"ACP"},
			"summary":     "Update the queued ACP prompt turn",
			"description": "Updates the existing process-local pending prompt.",
			"security":    []map[string][]string{{"cloudflareAccess": {}}},
			"parameters":  turnCommandParameters,
			"requestBody": turnPromptBody,
			"responses": map[string]any{
				"200": map[string]string{"description": "Pending prompt updated."},
				"401": map[string]string{"description": "User authentication failed."},
				"404": map[string]string{
					"description": "Agent, session, or queued turn was not found.",
				},
			},
		},
		"delete": map[string]any{
			"tags":        []string{"ACP"},
			"summary":     "Delete the queued ACP prompt turn",
			"description": "Idempotently removes the process-local pending prompt for the session.",
			"security":    []map[string][]string{{"cloudflareAccess": {}}},
			"parameters":  turnCommandParameters,
			"responses": map[string]any{
				"200": map[string]string{
					"description": "Pending prompt deleted or already absent.",
				},
				"401": map[string]string{"description": "User authentication failed."},
				"404": map[string]string{"description": "Agent or session was not found."},
			},
		},
	}
	paths[openAPIUserSessionTurnSteerPath] = map[string]any{
		"post": map[string]any{
			"tags":        []string{"ACP"},
			"summary":     "Steer the active ACP turn",
			"description": "Sends ACP session/cancel for the active prompt and stores one pending prompt to send after cancellation completes.",
			"security":    []map[string][]string{{"cloudflareAccess": {}}},
			"parameters":  turnCommandParameters,
			"requestBody": turnPromptBody,
			"responses": map[string]any{
				"200": map[string]string{"description": "Steer request accepted."},
				"401": map[string]string{"description": "User authentication failed."},
				"404": map[string]string{"description": "Agent, session, or tunnel was not found."},
				"409": map[string]string{"description": "Session has no active turn."},
			},
		},
	}
	sessionObserverParameters := append([]map[string]any{}, turnControlParameters...)
	sessionObserverParameters = append(sessionObserverParameters, map[string]any{
		"name":        "after_message_id",
		"in":          "query",
		"required":    false,
		"schema":      map[string]string{"type": "string"},
		"description": "Global message_id used as a trim hint for replay buffers.",
	})
	paths[openAPIUserSessionEventsPath] = map[string]any{
		"get": map[string]any{
			"tags":        []string{"ACP"},
			"summary":     "Observe the active ACP turn for a session",
			"description": "Streams live active-turn ACP events as SSE, or no_running_turn when the session is idle.",
			"security":    []map[string][]string{{"cloudflareAccess": {}}},
			"parameters":  sessionObserverParameters,
			"responses": map[string]any{
				"200": map[string]string{
					"description": "SSE stream of active turn observer events.",
				},
				"401": map[string]string{"description": "User authentication failed."},
				"404": map[string]string{"description": "Agent or session was not found."},
			},
		},
	}
	paths[openAPIUserSessionRuntimeResetPath] = map[string]any{
		"post": map[string]any{
			"tags":        []string{"ACP"},
			"summary":     "Reset a stale displayed session runtime status",
			"description": "Requests a compare-and-reset projection suppression in paxd. This corrects display state and does not cancel the underlying task.",
			"security":    []map[string][]string{{"cloudflareAccess": {}}},
			"parameters":  turnControlParameters,
			"requestBody": map[string]any{
				"required": true,
				"content": map[string]any{
					"application/json": map[string]any{
						"schema": map[string]any{
							"type":     "object",
							"required": []string{"expected_turn_instance_id"},
							"properties": map[string]any{
								"expected_turn_instance_id": map[string]string{"type": "string"},
							},
						},
					},
				},
			},
			"responses": map[string]any{
				"202": map[string]string{
					"description": "Reset accepted; a complete runtime snapshot is pending.",
				},
				"400": map[string]string{"description": "Expected turn identity is missing."},
				"401": map[string]string{"description": "User authentication failed."},
				"404": map[string]string{"description": "Agent or session was not found."},
				"409": map[string]string{
					"description": "The active turn changed or runtime mapping is unavailable.",
				},
				"503": map[string]string{"description": "Node control tunnel is unavailable."},
			},
		},
	}
}

func addPaxdArtifactPaths(doc map[string]any) {
	paths, ok := doc["paths"].(map[string]any)
	if !ok {
		paths = map[string]any{}
		doc["paths"] = paths
	}
	ensureSecurityScheme(doc, "adminUserAPIKey", map[string]any{
		"type":         "http",
		"scheme":       "bearer",
		"bearerFormat": "pax user API key",
		"description":  "Platform user API key whose owner is a configured administrator.",
	})
	paths[routeDownloadGenericArtifact] = map[string]any{
		"get": map[string]any{
			"tags":        []string{"artifacts"},
			"summary":     "Get signed artifact download URL",
			"description": "Returns a short-lived presigned S3-compatible URL for the newest binary matching the requested product, platform, and tags.",
			"parameters": []map[string]any{
				{
					"name":        "product",
					"in":          "query",
					"required":    true,
					"schema":      map[string]string{"type": "string"},
					"description": "Product such as paxd or paxl.",
				},
				{
					"name":        "platform",
					"in":          "query",
					"required":    true,
					"schema":      map[string]string{"type": "string"},
					"description": "Platform such as linux/amd64 or darwin/arm64.",
				},
				{
					"name":     "tags",
					"in":       "query",
					"required": false,
					"schema": map[string]any{
						"type":  "array",
						"items": map[string]string{"type": "string"},
					},
					"description": "Required tags. Multiple values use AND semantics.",
				},
			},
			"responses": map[string]any{
				"200": map[string]string{"description": "Signed download URL."},
				"404": map[string]string{"description": "No matching artifact."},
			},
		},
	}
	paths[routeDownloadPaxdArtifact] = map[string]any{
		"get": map[string]any{
			"tags":        []string{"paxd"},
			"summary":     "Get signed paxd download URL",
			"description": "Compatibility alias for the generic artifact resolver with product=paxd.",
			"parameters": []map[string]any{
				{
					"name":        "platform",
					"in":          "query",
					"required":    true,
					"schema":      map[string]string{"type": "string"},
					"description": "Platform such as linux/amd64 or darwin/arm64.",
				},
				{
					"name":     "tags",
					"in":       "query",
					"required": false,
					"schema": map[string]any{
						"type":  "array",
						"items": map[string]string{"type": "string"},
					},
					"description": "Required tags. Multiple values use AND semantics.",
				},
			},
			"responses": map[string]any{
				"200": map[string]string{"description": "Signed download URL."},
				"404": map[string]string{"description": "No matching paxd artifact."},
			},
		},
	}
	paths[routeDownloadPaxlArtifact] = map[string]any{
		"get": map[string]any{
			"tags":        []string{"paxl"},
			"summary":     "Get signed paxl download URL",
			"description": "Friendly alias for the generic artifact resolver with product=paxl.",
			"parameters": []map[string]any{
				{
					"name":        "platform",
					"in":          "query",
					"required":    true,
					"schema":      map[string]string{"type": "string"},
					"description": "Platform such as linux/amd64 or darwin/arm64.",
				},
				{
					"name":     "tags",
					"in":       "query",
					"required": false,
					"schema": map[string]any{
						"type":  "array",
						"items": map[string]string{"type": "string"},
					},
					"description": "Required tags. Multiple values use AND semantics.",
				},
			},
			"responses": map[string]any{
				"200": map[string]string{"description": "Signed download URL."},
				"404": map[string]string{"description": "No matching paxl artifact."},
			},
		},
	}
	paths[routeDownloadPaxdInstaller] = map[string]any{
		"get": map[string]any{
			"tags":        []string{"paxd"},
			"summary":     "Redirect to paxd installer script",
			"description": "Redirects to a short-lived presigned S3-compatible URL for the latest stable paxd installer shell script.",
			"responses": map[string]any{
				"302": map[string]string{"description": "Redirect to signed installer URL."},
			},
		},
	}
	paths[routeDownloadPaxlInstaller] = map[string]any{
		"get": map[string]any{
			"tags":        []string{"paxl"},
			"summary":     "Redirect to paxl installer script",
			"description": "Redirects to a short-lived presigned S3-compatible URL for the latest stable paxl installer shell script.",
			"responses": map[string]any{
				"302": map[string]string{"description": "Redirect to signed installer URL."},
			},
		},
	}
	paths[routePublishPaxdArtifact] = map[string]any{
		"post": map[string]any{
			"tags":        []string{"paxd"},
			"summary":     "Publish paxd artifact metadata",
			"description": "Compatibility alias for the generic artifact publish endpoint with product=paxd. The caller must present an admin user API key as a Bearer token.",
			"security":    []map[string][]string{{"adminUserAPIKey": {}}},
			"requestBody": map[string]any{
				"required": true,
				"content": map[string]any{
					"application/json": map[string]any{
						"schema": map[string]any{
							"type": "object",
							"required": []string{
								"platform", "version", "bucket", "object", "sha256",
							},
						},
					},
				},
			},
			"responses": map[string]any{
				"200": map[string]string{"description": "Artifact metadata recorded."},
				"401": map[string]string{"description": "Missing or invalid user API key."},
				"403": map[string]string{"description": "API key owner is not an admin."},
			},
		},
	}
	paths[routePublishGenericArtifact] = map[string]any{
		"post": map[string]any{
			"tags":        []string{"artifacts"},
			"summary":     "Publish artifact metadata",
			"description": "Records metadata for a product binary already uploaded to the configured S3-compatible bucket. The caller must present an admin user API key as a Bearer token.",
			"security":    []map[string][]string{{"adminUserAPIKey": {}}},
			"requestBody": map[string]any{
				"required": true,
				"content": map[string]any{
					"application/json": map[string]any{
						"schema": map[string]any{
							"type": "object",
							"required": []string{
								"product", "platform", "version", "bucket", "object", "sha256",
							},
						},
					},
				},
			},
			"responses": map[string]any{
				"200": map[string]string{"description": "Artifact metadata recorded."},
				"401": map[string]string{"description": "Missing or invalid bearer token."},
				"403": map[string]string{"description": "API key owner is not an admin."},
			},
		},
	}
}

func ensureSecurityScheme(doc map[string]any, name string, scheme map[string]any) {
	components, ok := doc["components"].(map[string]any)
	if !ok {
		components = map[string]any{}
		doc["components"] = components
	}
	securitySchemes, ok := components["securitySchemes"].(map[string]any)
	if !ok {
		securitySchemes = map[string]any{}
		components["securitySchemes"] = securitySchemes
	}
	securitySchemes[name] = scheme
}

const openAPIHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>pax-manager API</title>
  <style>
    :root {
      color-scheme: light;
      font-family: Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      color: #172026;
      background: #f7f8fa;
    }
    body {
      margin: 0;
    }
    header {
      background: #ffffff;
      border-bottom: 1px solid #d9dee4;
      padding: 20px 28px;
      position: sticky;
      top: 0;
      z-index: 1;
    }
    h1 {
      font-size: 22px;
      line-height: 1.2;
      margin: 0 0 6px;
    }
    .meta {
      color: #56616d;
      font-size: 14px;
    }
    main {
      max-width: 1120px;
      margin: 0 auto;
      padding: 24px;
    }
    .toolbar {
      align-items: center;
      display: flex;
      gap: 12px;
      margin-bottom: 18px;
    }
    input {
      border: 1px solid #c6ccd3;
      border-radius: 6px;
      flex: 1;
      font: inherit;
      padding: 10px 12px;
    }
    a.button {
      border: 1px solid #9aa4af;
      border-radius: 6px;
      color: #172026;
      font-size: 14px;
      padding: 10px 12px;
      text-decoration: none;
      white-space: nowrap;
    }
    .endpoint {
      background: #ffffff;
      border: 1px solid #d9dee4;
      border-radius: 8px;
      margin-bottom: 12px;
      overflow: hidden;
    }
    .summary {
      align-items: center;
      cursor: pointer;
      display: grid;
      gap: 12px;
      grid-template-columns: 88px 1fr;
      padding: 14px 16px;
    }
    .method {
      border-radius: 5px;
      color: #ffffff;
      display: inline-block;
      font-size: 12px;
      font-weight: 700;
      letter-spacing: 0;
      padding: 5px 8px;
      text-align: center;
    }
    .GET { background: #1269b0; }
    .POST { background: #1b7f4b; }
    .DELETE { background: #b33630; }
    .path {
      font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
      font-size: 14px;
      overflow-wrap: anywhere;
    }
    .details {
      border-top: 1px solid #edf0f2;
      display: none;
      padding: 0 16px 16px;
    }
    .endpoint.open .details {
      display: block;
    }
    .description {
      color: #56616d;
      margin: 0 0 12px;
    }
    pre {
      background: #172026;
      border-radius: 6px;
      color: #edf2f7;
      font-size: 12px;
      overflow: auto;
      padding: 12px;
    }
    .empty, .error {
      background: #ffffff;
      border: 1px solid #d9dee4;
      border-radius: 8px;
      padding: 18px;
    }
    @media (max-width: 640px) {
      header { padding: 16px; }
      main { padding: 16px; }
      .toolbar { align-items: stretch; flex-direction: column; }
      .summary { grid-template-columns: 72px 1fr; }
    }
  </style>
</head>
<body>
  <header>
    <h1>pax-manager API</h1>
    <div class="meta" id="meta">Loading OpenAPI document</div>
  </header>
  <main>
    <div class="toolbar">
      <input id="filter" type="search" placeholder="Filter endpoints" autocomplete="off">
      <a class="button" href="openapi.json">Open JSON</a>
    </div>
    <div id="content" class="empty">Loading</div>
  </main>
  <script>
    const content = document.getElementById("content");
    const filter = document.getElementById("filter");
    const meta = document.getElementById("meta");
    let endpoints = [];

    function methodNames(pathItem) {
      return Object.keys(pathItem).filter((key) => ["get", "post", "put", "patch", "delete"].includes(key));
    }

    function renderEndpoint(endpoint) {
      const item = document.createElement("section");
      item.className = "endpoint";
      item.dataset.search = (endpoint.method + " " + endpoint.path + " " + endpoint.summary + " " + endpoint.description).toLowerCase();

      const summary = document.createElement("div");
      summary.className = "summary";
      summary.innerHTML = '<span class="method ' + endpoint.method + '">' + endpoint.method + '</span><span class="path">' + endpoint.path + '</span>';
      summary.addEventListener("click", () => item.classList.toggle("open"));

      const details = document.createElement("div");
      details.className = "details";
      const description = document.createElement("p");
      description.className = "description";
      description.textContent = endpoint.summary + (endpoint.description ? " - " + endpoint.description : "");
      const raw = document.createElement("pre");
      raw.textContent = JSON.stringify(endpoint.operation, null, 2);
      details.append(description, raw);
      item.append(summary, details);
      return item;
    }

    function applyFilter() {
      const query = filter.value.trim().toLowerCase();
      content.innerHTML = "";
      const visible = endpoints.filter((endpoint) => endpoint.dataset.search.includes(query));
      if (visible.length === 0) {
        content.className = "empty";
        content.textContent = "No endpoints match the filter.";
        return;
      }
      content.className = "";
      visible.forEach((endpoint) => content.appendChild(endpoint));
    }

    fetch("openapi.json")
      .then((response) => {
        if (!response.ok) throw new Error("OpenAPI request failed: " + response.status);
        return response.json();
      })
      .then((doc) => {
        meta.textContent = doc.info.title + " " + doc.info.version + " - " + (doc.servers && doc.servers[0] ? doc.servers[0].url : location.origin);
        endpoints = [];
        Object.entries(doc.paths).forEach(([path, pathItem]) => {
          methodNames(pathItem).forEach((method) => {
            const operation = pathItem[method];
            endpoints.push(renderEndpoint({
              method: method.toUpperCase(),
              path,
              summary: operation.summary || "",
              description: operation.description || "",
              operation,
            }));
          });
        });
        applyFilter();
      })
      .catch((error) => {
        content.className = "error";
        content.textContent = error.message;
      });

    filter.addEventListener("input", applyFilter);
  </script>
</body>
</html>
`
