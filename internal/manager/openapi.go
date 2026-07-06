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
	addSessionHistoryPath(doc)
	addNodeConversationDeliveryPath(doc)
	addPaxdArtifactPaths(doc)
	return json.MarshalIndent(doc, "", "  ")
}

func addSessionHistoryPath(doc map[string]any) {
	paths, ok := doc["paths"].(map[string]any)
	if !ok {
		paths = map[string]any{}
		doc["paths"] = paths
	}
	paths[openAPISessionHistoryPath] = map[string]any{
		"get": map[string]any{
			"tags":        []string{"user"},
			"summary":     "List agent session history",
			"description": "Lists durable session history messages with their message parts for a specific session under an agent visible to the current user.",
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
					"description": "Agent identifier.",
				},
				{
					"name":        "session_id",
					"in":          "path",
					"required":    true,
					"schema":      map[string]string{"type": "string"},
					"description": "Session identifier.",
				},
				{
					"name":        "limit",
					"in":          "query",
					"required":    false,
					"schema":      map[string]any{"type": "integer", "maximum": 1000},
					"description": "Maximum number of history messages to return. Defaults to 1000.",
				},
			},
			"responses": map[string]any{
				"200": map[string]string{"description": "Session history messages."},
				"401": map[string]string{"description": "User authentication failed."},
				"404": map[string]string{"description": "Agent or session not found."},
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
										"agent_id":                map[string]string{"type": "string"},
										"representative_agent_id": map[string]string{"type": "string"},
										"session_id":              map[string]string{"type": "string"},
									},
								},
								"target": map[string]any{
									"type":        "object",
									"description": "Delivery target. kind=representative asks another representative agent; kind=active_invocation replies to the current session's active invocation.",
									"required":    []string{"kind"},
									"properties": map[string]any{
										"kind":                    map[string]any{"type": "string", "enum": []string{"representative", "active_invocation"}},
										"representative_agent_id": map[string]string{"type": "string"},
										"session_id":              map[string]string{"type": "string"},
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
}

func addPaxdArtifactPaths(doc map[string]any) {
	paths, ok := doc["paths"].(map[string]any)
	if !ok {
		paths = map[string]any{}
		doc["paths"] = paths
	}
	ensureSecurityScheme(doc, "googleIam", map[string]any{
		"type":         "http",
		"scheme":       "bearer",
		"bearerFormat": "Google ID token",
		"description":  "Google-signed identity token for an allowed IAM principal.",
	})
	paths[routeDownloadGenericArtifact] = map[string]any{
		"get": map[string]any{
			"tags":        []string{"artifacts"},
			"summary":     "Get signed artifact download URL",
			"description": "Returns a short-lived signed GCS URL for the newest binary matching the requested product, platform, and tags.",
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
			"description": "Redirects to a short-lived signed GCS URL for the latest stable paxd installer shell script.",
			"responses": map[string]any{
				"302": map[string]string{"description": "Redirect to signed installer URL."},
			},
		},
	}
	paths[routeDownloadPaxlInstaller] = map[string]any{
		"get": map[string]any{
			"tags":        []string{"paxl"},
			"summary":     "Redirect to paxl installer script",
			"description": "Redirects to a short-lived signed GCS URL for the latest stable paxl installer shell script.",
			"responses": map[string]any{
				"302": map[string]string{"description": "Redirect to signed installer URL."},
			},
		},
	}
	paths[routePublishPaxdArtifact] = map[string]any{
		"post": map[string]any{
			"tags":        []string{"paxd"},
			"summary":     "Publish paxd artifact metadata",
			"description": "Compatibility alias for the generic artifact publish endpoint with product=paxd. The caller must present a Google-signed identity token for an allowed IAM principal.",
			"security":    []map[string][]string{{"googleIam": {}}},
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
				"401": map[string]string{
					"description": "Missing or invalid Google identity token.",
				},
				"403": map[string]string{"description": "IAM principal is not allowed."},
			},
		},
	}
	paths[routePublishGenericArtifact] = map[string]any{
		"post": map[string]any{
			"tags":        []string{"artifacts"},
			"summary":     "Publish artifact metadata",
			"description": "Records metadata for a product binary already uploaded to GCS. The caller must present a Google-signed identity token for an allowed IAM principal.",
			"security":    []map[string][]string{{"googleIam": {}}},
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
				"403": map[string]string{"description": "Principal is not allowed."},
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
