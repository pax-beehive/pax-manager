package manager

import (
	"context"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
)

//go:generate go run ../../cmd/openapi-gen -in ../../api/pax_manager.thrift -out openapi_generated.go

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
	writeJSON(ctx, http.StatusOK, openAPIDocument(requestBaseURL(ctx)))
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

func openAPIDocument(serverURL string) map[string]any {
	doc := openAPIBaseDocument()
	doc["servers"] = []map[string]string{{"url": serverURL}}
	return doc
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
      <a class="button" href="/openapi.json">Open JSON</a>
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

    fetch("/openapi.json")
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
