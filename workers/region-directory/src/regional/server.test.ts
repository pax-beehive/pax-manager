import { afterEach, expect, it } from "vitest";
import { createServer, request as httpRequest, type Server } from "node:http";
import { once } from "node:events";
import { WebSocket, WebSocketServer } from "ws";
import { gatewayServer } from "./server";
import type { GatewayConfig } from "./config";
import type { ProxyPlan } from "./gateway";
import { originIdentityHeaders } from "../browser-proxy";

const servers: Server[] = [];
const sockets: WebSocket[] = [];
async function listen(server: Server) {
  servers.push(server);
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  return `http://127.0.0.1:${(server.address() as { port: number }).port}`;
}
afterEach(async () => {
  for (const socket of sockets.splice(0)) socket.terminate();
  await Promise.all(
    servers.splice(0).map(
      (server) =>
        new Promise<void>((resolve) => {
          server.closeAllConnections();
          server.close(() => resolve());
        }),
    ),
  );
});
function config(managerURL: string): GatewayConfig {
  return {
    region: "hk",
    origins: { hk: "https://hk.example", us: "https://us.example" },
    managerURL,
  } as GatewayConfig;
}
function plan(
  target: string,
  path: string,
  headers = new Headers({ "x-pax-identity-assertion": "trusted" }),
): ProxyPlan {
  return { target, path, headers, cookies: ["refreshed=1; Secure"] };
}
it("preserves streamed request bodies, status, queries and separate response cookies", async () => {
  const upstream = await listen(
    createServer(async (req, res) => {
      let body = "";
      for await (const chunk of req) body += chunk;
      res.writeHead(201, {
        "Content-Type": "application/json",
        "Set-Cookie": "upstream=1",
      });
      res.end(
        JSON.stringify({
          body,
          path: req.url,
          proof: req.headers["x-pax-identity-assertion"],
          forged: req.headers["x-user-email"],
        }),
      );
    }),
  );
  const url = await listen(
    gatewayServer(config(upstream), async (request) =>
      plan(upstream, "/destination" + new URL(request.url).search),
    ),
  );
  const response = await fetch(url + "/source?cursor=2", {
    method: "POST",
    body: "actual upload",
    headers: { "x-user-email": "attacker" },
  });
  expect(response.status).toBe(201);
  expect(await response.json()).toEqual({
    body: "actual upload",
    path: "/destination?cursor=2",
    proof: "trusted",
  });
  expect(response.headers.getSetCookie()).toEqual(["refreshed=1; Secure"]);
  expect(response.headers.get("Cache-Control")).toBe("no-store");
});
it("delivers the first SSE event before the upstream ends", async () => {
  let finish: (() => void) | undefined;
  const upstream = await listen(
    createServer((_req, res) => {
      res.writeHead(200, { "Content-Type": "text/event-stream" });
      res.write("data: first\n\n");
      finish = () => res.end("data: last\n\n");
    }),
  );
  const url = await listen(
    gatewayServer(config(upstream), async () => plan(upstream, "/events")),
  );
  const response = await fetch(url);
  const reader = response.body!.getReader();
  const first = await reader.read();
  expect(new TextDecoder().decode(first.value)).toBe("data: first\n\n");
  finish!();
  expect(new TextDecoder().decode((await reader.read()).value)).toBe(
    "data: last\n\n",
  );
  expect((await reader.read()).done).toBe(true);
});
it("upgrades authenticated WebSockets and retains refresh cookies and duplex frames", async () => {
  const upstreamServer = createServer();
  const wsServer = new WebSocketServer({ server: upstreamServer });
  wsServer.on("connection", (socket, req) => {
    sockets.push(socket);
    expect(req.headers["x-pax-identity-assertion"]).toBe("trusted");
    socket.on("message", (data) => socket.send(data.toString()));
  });
  const upstream = await listen(upstreamServer);
  const url = await listen(
    gatewayServer(config(upstream), async (request) => {
      const headers = originIdentityHeaders(request);
      headers.set("x-pax-identity-assertion", "trusted");
      return plan(upstream, "/tunnel", headers);
    }),
  );
  const socket = new WebSocket(url.replace("http:", "ws:"));
  sockets.push(socket);
  const upgrade = once(socket, "upgrade");
  await once(socket, "open");
  expect((await upgrade)[0].headers["set-cookie"]).toEqual([
    "refreshed=1; Secure",
  ]);
  socket.send("frame");
  expect((await once(socket, "message"))[0].toString()).toBe("frame");
  socket.close();
  await once(socket, "close");
  wsServer.close();
});
it("buffers only control JSON and strips untrusted Console identity headers", async () => {
  const upstream = await listen(
    createServer((req, res) => res.end(JSON.stringify(req.headers))),
  );
  const url = await listen(
    gatewayServer(config(upstream), async (request) => {
      if (new URL(request.url).pathname === "/api/v1/region/bootstrap")
        return new Response(await request.text(), {
          headers: {
            "Set-Cookie": "one=1",
            "Content-Type": "application/json",
          },
        });
      return { target: upstream, path: "/", cookies: [] };
    }),
  );
  const control = await fetch(url + "/api/v1/region/bootstrap", {
    method: "POST",
    body: '{"preferred_region":"hk"}',
  });
  expect(await control.json()).toEqual({ preferred_region: "hk" });
  expect(control.headers.getSetCookie()).toEqual(["one=1"]);
  const consoleResponse = await fetch(url, {
    headers: {
      "x-pax-identity-assertion": "forged",
      "cf-access-jwt-assertion": "forged",
      "x-forwarded-host": "evil",
      "x-user-email": "evil",
      forwarded: "evil",
      cookie: "session=valid",
    },
  });
  const headers = (await consoleResponse.json()) as Record<string, string>;
  expect(headers.cookie).toBe("session=valid");
  expect(headers["x-pax-identity-assertion"]).toBeUndefined();
  expect(headers["x-forwarded-host"]).toBeUndefined();
});
it("rejects malformed targets, oversized control bodies and unavailable upstreams", async () => {
  const url = await listen(
    gatewayServer(config("http://127.0.0.1:1"), async () =>
      plan("http://127.0.0.1:1", "/"),
    ),
  );
  expect((await fetch(url)).status).toBe(502);
  const status = await new Promise((resolve) => {
    const req = httpRequest(url, { path: "//evil.example/" }, (res) => {
      res.resume();
      resolve(res.statusCode);
    });
    req.end();
  });
  expect(status).toBe(400);
  const large = await fetch(url + "/api/v1/region/bootstrap", {
    method: "POST",
    body: "x".repeat(65537),
  });
  expect(large.status).toBe(400);
});
it("rejects WebSocket upgrades without an authenticated Manager plan", async () => {
  for (const outcome of [
    new Response(null, { status: 401 }),
    { target: "http://127.0.0.1:1", path: "/", cookies: [] },
    new Error("bad"),
  ]) {
    const url = await listen(
      gatewayServer(config("http://127.0.0.1:1"), async () => {
        if (outcome instanceof Error) throw outcome;
        return outcome;
      }),
    );
    const socket = new WebSocket(url.replace("http:", "ws:"));
    sockets.push(socket);
    socket.on("error", () => {});
    const status = await new Promise((resolve) =>
      socket.on("unexpected-response", (_req, res) => {
        res.resume();
        resolve(res.statusCode);
        socket.terminate();
      }),
    );
    socket.on("error", () => {});
    expect([401, 404, 400]).toContain(status);
  }
});
