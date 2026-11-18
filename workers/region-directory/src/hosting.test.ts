import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { createHostingWorker, type HostingEnv } from "./hosting";

const env: HostingEnv = {
  HOSTING_SERVICE_REGION: "us",
  PUBLIC_ORIGIN: "https://pax.example",
  US_MANAGER_URL: "https://manager.example",
  US_ACCESS_CLIENT_ID: "origin-client",
  US_ACCESS_CLIENT_SECRET: "origin-secret",
};
const accounting = "/api/v1/hosting/gateway/accounting";
const hostRoutes = ["claim", "heartbeat", "complete"].map(
  (name) => `/api/v1/hosting/jobs/${name}`,
);
const gatewayKey = "gateway-" + "g".repeat(40);
const hostKey = "host-" + "h".repeat(40);
const headers = (key = gatewayKey) => ({
  Authorization: `Bearer ${key}`,
  "Content-Type": "application/json",
});
const request = (path = accounting, options: RequestInit = {}) =>
  new Request(`https://pax.example${path}`, {
    method: "POST",
    headers: headers(),
    body: '{ "action": "models", "token": "limited-model-token" }\n',
    ...options,
  });
let upstream: ReturnType<typeof vi.fn<typeof fetch>>;
beforeEach(() => {
  upstream = vi.fn<typeof fetch>(async () =>
    Response.json({ code: 200, data: { models: [] } }),
  );
});
afterEach(() => vi.restoreAllMocks());

it("forwards one exact accounting request with service auth and no user identity", async () => {
  const app = createHostingWorker({ fetch: upstream });
  const input = request(accounting, {
    headers: {
      ...headers(),
      Cookie: "private-browser-cookie",
      "X-User-Email": "forged@example.invalid",
      "X-Pax-Key": "node-key",
      "X-Pax-Identity-Assertion": "forged",
      "X-Pax-User-ID": "usr_forged",
      "Cf-Access-Authenticated-User-Email": "forged@example.invalid",
      "Cf-Access-Jwt-Assertion": "forged",
      "CF-Access-Client-Id": "attacker-client",
      "CF-Access-Client-Secret": "attacker-secret",
      "X-Forwarded-Host": "attacker.example",
    },
  });
  const result = await app.fetch(input, env);
  expect(result.status).toBe(200);
  expect(upstream).toHaveBeenCalledTimes(1);
  const [target, options] = upstream.mock.calls[0];
  expect(target).toBe("https://manager.example" + accounting);
  expect(options!.method).toBe("POST");
  expect(options!.redirect).toBe("manual");
  expect(new TextDecoder().decode(options!.body as Uint8Array)).toBe(
    '{ "action": "models", "token": "limited-model-token" }\n',
  );
  const forwarded = new Headers(options!.headers);
  expect(forwarded.get("Authorization")).toBe(`Bearer ${gatewayKey}`);
  expect(forwarded.get("CF-Access-Client-Id")).toBe("origin-client");
  expect(forwarded.get("CF-Access-Client-Secret")).toBe("origin-secret");
  for (const name of [
    "Cookie",
    "X-User-Email",
    "X-Pax-Key",
    "X-Pax-Identity-Assertion",
    "X-Pax-User-ID",
    "Cf-Access-Authenticated-User-Email",
    "Cf-Access-Jwt-Assertion",
    "X-Forwarded-Host",
  ])
    expect(forwarded.has(name)).toBe(false);
  expect(await result.json()).toEqual({ code: 200, data: { models: [] } });
  expect(result.headers.get("Cache-Control")).toBe("no-store");
});

it.each(hostRoutes)(
  "forwards only the exact trusted-host route %s",
  async (path) => {
    const result = await createHostingWorker({ fetch: upstream }).fetch(
      request(path, { headers: headers(hostKey) }),
      env,
    );
    expect(result.status).toBe(200);
    expect(upstream.mock.calls[0][0]).toBe("https://manager.example" + path);
    expect(
      new Headers(upstream.mock.calls[0][1]!.headers).get("Authorization"),
    ).toBe(`Bearer ${hostKey}`);
  },
);

it("leaves role-specific key authorization at Manager instead of promoting user or host credentials", async () => {
  upstream.mockImplementation(async (target, options) => {
    const token = new Headers(options!.headers).get("Authorization");
    const accepted =
      token ===
      `Bearer ${String(target).endsWith("accounting") ? gatewayKey : hostKey}`;
    return Response.json(
      accepted
        ? { code: 200, data: {} }
        : { code: 401, message: "unauthorized" },
      { status: accepted ? 200 : 401 },
    );
  });
  const app = createHostingWorker({ fetch: upstream });
  expect(
    (await app.fetch(request(accounting, { headers: headers(hostKey) }), env))
      .status,
  ).toBe(401);
  expect((await app.fetch(request(hostRoutes[0]), env)).status).toBe(401);
  expect((await app.fetch(request(), env)).status).toBe(200);
});

it.each([
  "/api/v1/hosting/gateway/accounting/",
  "/api/v1/hosting/gateway/accounting%2f",
  "/api/v1/hosting/jobs/claim/",
  "/api/v1/hosting/jobs/delete",
  "/api/v1/hosting/model",
  "/api/v1/user/self/me",
  "/internal/users/ensure",
  "/api/v1/node/register",
])("rejects paths outside the fixed service allowlist: %s", async (path) => {
  const result = await createHostingWorker({ fetch: upstream }).fetch(
    request(path),
    env,
  );
  expect(result.status).toBe(404);
  expect(upstream).not.toHaveBeenCalled();
});

it.each(["GET", "HEAD", "PUT", "PATCH", "DELETE", "OPTIONS"])(
  "does not forward a non-POST service request: %s",
  async (method) => {
    const result = await createHostingWorker({ fetch: upstream }).fetch(
      request(accounting, { method, body: undefined }),
      env,
    );
    expect(result.status).toBe(405);
    expect(upstream).not.toHaveBeenCalled();
  },
);

it.each(["?token=private", "?region=hk", "?anything=1"])(
  "rejects query-based credentials and caller routing: %s",
  async (query) => {
    const result = await createHostingWorker({ fetch: upstream }).fetch(
      request(accounting + query),
      env,
    );
    expect(result.status).toBe(400);
    expect(upstream).not.toHaveBeenCalled();
  },
);

it.each([
  "",
  "Bearer short",
  "Basic " + gatewayKey,
  "Bearer " + "a".repeat(4097),
  `Bearer ${gatewayKey}, Bearer other`,
])(
  "rejects malformed or missing bearer credentials before reading or forwarding",
  async (authorization) => {
    const result = await createHostingWorker({ fetch: upstream }).fetch(
      request(accounting, {
        headers: {
          "Content-Type": "application/json",
          Authorization: authorization,
        },
      }),
      env,
    );
    expect(result.status).toBe(401);
    expect(await result.text()).not.toContain(gatewayKey);
    expect(upstream).not.toHaveBeenCalled();
  },
);

it("is disabled by default and fails closed on missing or unsafe origin credentials", async () => {
  for (const changes of [
    { HOSTING_SERVICE_REGION: undefined },
    { HOSTING_SERVICE_REGION: "hk" },
    { HOSTING_SERVICE_REGION: "US" },
    { HOSTING_SERVICE_REGION: "us " },
    { US_MANAGER_URL: "http://manager.example" },
    { US_MANAGER_URL: "https://key@manager.example" },
    { US_MANAGER_URL: "https://manager.example/other" },
    { US_MANAGER_URL: "https://manager.example?region=hk" },
    { US_MANAGER_URL: "https://pax.example" },
    { US_ACCESS_CLIENT_ID: "" },
    { US_ACCESS_CLIENT_SECRET: "" },
    { US_ACCESS_CLIENT_ID: "client\r\nInjected:true" },
    { US_ACCESS_CLIENT_SECRET: "secret with space" },
    { US_ACCESS_CLIENT_SECRET: "s".repeat(4097) },
  ]) {
    const result = await createHostingWorker({ fetch: upstream }).fetch(
      request(),
      { ...env, ...changes },
    );
    expect(result.status).toBe(503);
    expect(upstream).not.toHaveBeenCalled();
  }
});

it("rejects another hostname, content type, compression and ambiguous body length", async () => {
  const cases = [
    new Request("https://other.example" + accounting, {
      method: "POST",
      headers: headers(),
      body: "{}",
    }),
    request(accounting, {
      headers: { ...headers(), "Content-Type": "text/plain" },
    }),
    request(accounting, {
      headers: { ...headers(), "Content-Encoding": "gzip" },
    }),
    request(accounting, {
      headers: { ...headers(), "Content-Length": "2, 2" },
    }),
    request(accounting, { headers: { ...headers(), "Content-Length": "1" } }),
  ];
  for (const input of cases) {
    const result = await createHostingWorker({ fetch: upstream }).fetch(
      input,
      env,
    );
    expect([400, 404, 415]).toContain(result.status);
    expect(upstream).not.toHaveBeenCalled();
  }
});

it("bounds declared and streamed bodies before any origin request", async () => {
  for (const input of [
    request(accounting, {
      headers: { ...headers(), "Content-Length": "65537" },
      body: "{}",
    }),
    request(accounting, { body: "x".repeat(65537) }),
  ]) {
    const result = await createHostingWorker({ fetch: upstream }).fetch(
      input,
      env,
    );
    expect(result.status).toBe(413);
    expect(upstream).not.toHaveBeenCalled();
  }
});

it("does not forward origin cookies, CORS identity or arbitrary headers", async () => {
  upstream.mockResolvedValue(
    Response.json(
      { data: {} },
      {
        headers: {
          "Set-Cookie": "private=1",
          "X-Pax-Identity-Assertion": "private",
          "Access-Control-Allow-Origin": "*",
          "Access-Control-Allow-Credentials": "true",
          "Retry-After": "60",
        },
      },
    ),
  );
  const result = await createHostingWorker({ fetch: upstream }).fetch(
    request(),
    env,
  );
  for (const name of [
    "Set-Cookie",
    "X-Pax-Identity-Assertion",
    "Access-Control-Allow-Origin",
    "Access-Control-Allow-Credentials",
  ])
    expect(result.headers.has(name)).toBe(false);
  expect(result.headers.get("Retry-After")).toBe("60");
});

it("sanitizes redirects, non-JSON, oversized replies and failed transport without logging secrets", async () => {
  const log = vi.spyOn(console, "error").mockImplementation(() => {});
  for (const response of [
    new Response("private", {
      status: 302,
      headers: { Location: "https://other.example?key=private" },
    }),
    new Response("private", { status: 403 }),
    new Response("not JSON private", {
      headers: { "Content-Type": "application/json" },
    }),
    new Response("x".repeat(2 * 1024 * 1024 + 1), {
      headers: { "Content-Type": "application/json" },
    }),
  ]) {
    upstream.mockResolvedValueOnce(response);
    const result = await createHostingWorker({ fetch: upstream }).fetch(
      request(),
      env,
    );
    expect(result.status).toBe(502);
    expect(await result.text()).not.toContain("private");
  }
  upstream.mockRejectedValueOnce(new Error("private credential failure"));
  const result = await createHostingWorker({ fetch: upstream }).fetch(
    request(),
    env,
  );
  expect(result.status).toBe(502);
  expect(await result.text()).not.toContain("private");
  expect(log).not.toHaveBeenCalled();
});

it("expires a stalled inbound body and a stalled origin reply within one bounded deadline", async () => {
  const app = createHostingWorker({ fetch: upstream, timeoutMs: 10 });
  const never = () => new ReadableStream<Uint8Array>({ start() {} });
  const input = request(accounting, {
    body: never(),
    duplex: "half",
  } as RequestInit);
  expect((await app.fetch(input, env)).status).toBe(504);
  expect(upstream).not.toHaveBeenCalled();
  upstream.mockResolvedValueOnce(
    new Response(never(), { headers: { "Content-Type": "application/json" } }),
  );
  expect((await app.fetch(request(), env)).status).toBe(504);
});

it("suppresses rejected origin-response cleanup without an unhandled transport error", async () => {
  const cancel = vi.fn(async () => {
    throw new Error("private origin cleanup failure");
  });
  upstream.mockResolvedValueOnce(
    new Response(new ReadableStream<Uint8Array>({ cancel }), { status: 302 }),
  );
  const result = await createHostingWorker({ fetch: upstream }).fetch(
    request(),
    env,
  );
  expect(result.status).toBe(502);
  expect(await result.text()).not.toContain("private");
  expect(cancel).toHaveBeenCalledTimes(1);
  await new Promise<void>((resolve) => setTimeout(resolve, 0));
});

it("does not forward a request whose caller has already aborted", async () => {
  const controller = new AbortController();
  controller.abort();
  const result = await createHostingWorker({ fetch: upstream }).fetch(
    request(accounting, { signal: controller.signal }),
    env,
  );
  expect(result.status).toBe(504);
  expect(upstream).not.toHaveBeenCalled();
});

it("handles body cancellation failure without leaking it or forwarding an oversized body", async () => {
  const cancel = vi.fn(async () => {
    throw new Error("private transport failure");
  });
  const body = new ReadableStream<Uint8Array>({
    start(controller) {
      controller.enqueue(new Uint8Array(65537));
    },
    cancel,
  });
  const result = await createHostingWorker({ fetch: upstream }).fetch(
    request(accounting, { body, duplex: "half" } as RequestInit),
    env,
  );
  expect(result.status).toBe(413);
  expect(await result.text()).not.toContain("private");
  expect(cancel).toHaveBeenCalledTimes(1);
  expect(upstream).not.toHaveBeenCalled();
});
