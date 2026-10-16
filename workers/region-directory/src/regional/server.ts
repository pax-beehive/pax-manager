import {
  createServer,
  type IncomingMessage,
  type ServerResponse,
} from "node:http";
import type { Duplex } from "node:stream";
import httpProxy from "http-proxy";
import type { GatewayConfig } from "./config";
import type { ProxyPlan } from "./gateway";

type Handler = (request: Request) => Promise<Response | ProxyPlan>;
async function requestFor(
  req: IncomingMessage,
  origin: string,
): Promise<Request> {
  const path = req.url ?? "/";
  if (
    !path.startsWith("/") ||
    path.startsWith("//") ||
    /[\\\x00-\x20]/.test(path)
  )
    throw new Error("Invalid request target");
  const url = new URL(path, origin);
  const headers = new Headers();
  for (const [name, value] of Object.entries(req.headers))
    if (value !== undefined)
      headers.set(name, Array.isArray(value) ? value.join(", ") : value);
  let body: Buffer | undefined;
  if (
    req.method !== "GET" &&
    req.method !== "HEAD" &&
    (url.pathname === "/internal/account-directory" ||
      url.pathname === "/api/v1/region/bootstrap" ||
      url.pathname === "/api/v1/region/paxl-login/approve" ||
      url.pathname === "/api/v1/native/client/bootstrap" ||
      url.pathname === "/api/v1/native/client/logout")
  ) {
    const chunks: Buffer[] = [];
    let bytes = 0;
    for await (const chunk of req) {
      bytes += chunk.length;
      if (bytes > 65536) throw new Error("Request too large");
      chunks.push(chunk);
    }
    body = Buffer.concat(chunks);
  }
  return new Request(url, {
    method: req.method,
    headers,
    body: body ? new Uint8Array(body) : undefined,
  });
}
async function reply(res: ServerResponse, response: Response) {
  res.statusCode = response.status;
  response.headers.forEach((value, key) => {
    if (key !== "set-cookie") res.setHeader(key, value);
  });
  const cookies = response.headers.getSetCookie();
  if (cookies.length) res.setHeader("Set-Cookie", cookies);
  res.end(Buffer.from(await response.arrayBuffer()));
}
function applyPlan(req: IncomingMessage, plan: ProxyPlan) {
  req.url = plan.path;
  if (plan.headers) {
    const length = req.headers["content-length"];
    req.headers = Object.fromEntries(plan.headers.entries());
    if (length) req.headers["content-length"] = length;
  } else {
    // Console is never allowed to trust identity or origin supplied by callers.
    for (const key of Object.keys(req.headers))
      if (
        key.startsWith("cf-") ||
        key.startsWith("x-pax-") ||
        key.startsWith("x-forwarded-") ||
        ["x-user-email", "x-user-id", "forwarded"].includes(key)
      )
        delete req.headers[key];
  }
  req.headers.host = new URL(plan.target).host;
}
function rejectUpgrade(socket: Duplex, status: number) {
  socket.end(
    `HTTP/1.1 ${status} Rejected\r\nConnection: close\r\nContent-Length: 0\r\n\r\n`,
  );
}
export function gatewayServer(config: GatewayConfig, handle: Handler) {
  const proxy = httpProxy.createProxyServer({ xfwd: false, ws: true });
  const refresh = new WeakMap<IncomingMessage, string[]>();
  proxy.on("proxyRes", (upstream, req) => {
    if (req.headers["x-pax-identity-assertion"]) {
      upstream.headers["cache-control"] = "no-store";
      upstream.headers.vary = "Cookie, Authorization";
      delete upstream.headers["set-cookie"];
      delete upstream.headers["access-control-allow-origin"];
      delete upstream.headers["access-control-allow-credentials"];
    }
    const cookies = refresh.get(req);
    if (cookies?.length)
      upstream.headers["set-cookie"] = [
        ...(upstream.headers["set-cookie"] ?? []),
        ...cookies,
      ];
  });
  proxy.on("proxyReqWs", (outgoing, req) => {
    outgoing.on("upgrade", (upstream) => {
      delete upstream.headers["set-cookie"];
      const cookies = refresh.get(req);
      if (cookies?.length)
        upstream.headers["set-cookie"] = [
          ...(upstream.headers["set-cookie"] ?? []),
          ...cookies,
        ];
    });
  });
  proxy.on("error", (_error, _req, response) => {
    if ("writeHead" in response) {
      const res = response as ServerResponse;
      if (!res.headersSent)
        res.writeHead(502, {
          "Content-Type": "application/json",
          "Cache-Control": "no-store",
        });
      res.end('{"error":"regional_upstream_unavailable"}');
    } else response.destroy();
  });
  const server = createServer(async (req, res) => {
    try {
      const result = await handle(
        await requestFor(req, config.origins[config.region]),
      );
      if (result instanceof Response) return await reply(res, result);
      refresh.set(req, result.cookies);
      applyPlan(req, result);
      proxy.web(req, res, { target: result.target });
    } catch {
      if (!res.headersSent)
        res.writeHead(400, { "Content-Type": "application/json" });
      res.end('{"error":"invalid_request"}');
    }
  });
  server.on("upgrade", async (req, socket, head) => {
    try {
      const result = await handle(
        await requestFor(req, config.origins[config.region]),
      );
      if (result instanceof Response)
        return rejectUpgrade(socket, result.status);
      if (!result.headers || result.target !== config.managerURL)
        return rejectUpgrade(socket, 404);
      refresh.set(req, result.cookies);
      applyPlan(req, result);
      req.headers.connection = "Upgrade";
      proxy.ws(req, socket, head, { target: result.target });
    } catch {
      rejectUpgrade(socket, 400);
    }
  });
  server.on("close", () => proxy.close());
  return server;
}
