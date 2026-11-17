import { afterEach, expect, it, vi } from "vitest";
import { runtimeConfig, runtimeConfigPath } from "./runtime-config";
import { attachWorkosIdentity } from "./origin-assertion";
import type { Env } from "./index";
import { createWorker } from "./index";

const env = {
  US_MANAGER_URL: "https://us.example.invalid",
  HK_MANAGER_URL: "https://hk.example.invalid",
} as Env;
const request = (path: string, method = "GET", body?: string) =>
  new Request(`https://app.example.invalid/api/pax${path}`, {
    method,
    body,
    headers: {
      "Cf-Access-Jwt-Assertion": "original-identity",
      "Content-Type": "application/json",
      Cookie: "browser-cookie=1",
    },
  });
const managerJSON = (data: unknown, status = 200) =>
  Response.json(
    { code: status, message: "ok", data },
    { status, headers: { "Set-Cookie": "upstream=1" } },
  );
afterEach(() => vi.unstubAllGlobals());

it.each([
  ["/api/pax/api/v1/admin/config", "/api/v1/admin/config"],
  ["/api/v1/admin/config/test", "/api/v1/admin/config/test"],
  ["/api/pax/api/v1/admin/config/audit", "/api/v1/admin/config/audit"],
  [
    "/api/pax/api/v1/admin/config/audit/42/revert",
    "/api/v1/admin/config/audit/42/revert",
  ],
  [
    "/api/pax/api/v1/admin/config/feature.secretary",
    "/api/v1/admin/config/feature.secretary",
  ],
])("Given %s, routes the runtime config admin path", (input, want) => {
  expect(runtimeConfigPath(input)).toBe(want);
});

it.each([
  "/api/pax/api/v1/admin/configx",
  "/api/pax/api/v1/admin/config/",
  "/api/pax/api/v1/admin/config/Search.URL",
  "/api/pax/api/v1/admin/config/a/b",
  "/api/pax/api/v1/admin/config/audit/0/revert",
  "/api/pax/api/v1/admin/config/audit/x/revert",
  "/api/pax/api/v1/admin/other",
])("Given %s, does not route", (input) => {
  expect(runtimeConfigPath(input)).toBeNull();
});

it("Given a selected region, forwards only to that Manager with the original identity and query", async () => {
  const fetch = vi.fn(async (url: URL, init: RequestInit) => {
    expect(url.origin).toBe("https://hk.example.invalid");
    expect(url.pathname).toBe("/api/v1/admin/config/audit");
    expect(url.searchParams.get("region")).toBeNull();
    expect(url.searchParams.get("key")).toBe("search.url");
    expect(init.method).toBe("GET");
    const headers = new Headers(init.headers);
    expect(headers.get("Cf-Access-Jwt-Assertion")).toBe("original-identity");
    expect(headers.get("Cookie")).toBe("CF_Authorization=original-identity");
    return managerJSON({ items: [], next_cursor: null });
  });
  vi.stubGlobal("fetch", fetch);
  const response = await runtimeConfig(
    request("/api/v1/admin/config/audit?region=hk&key=search.url"),
    env,
    "/api/v1/admin/config/audit",
  );
  expect(response.status).toBe(200);
  expect(response.headers.get("Set-Cookie")).toBeNull();
  expect(response.headers.get("Cache-Control")).toBe("private, no-store");
  expect(fetch).toHaveBeenCalledTimes(1);
  expect(await response.json()).toEqual({
    code: 200,
    message: "ok",
    data: { items: [], next_cursor: null },
  });
});

it("Given a write, forwards the method and body and passes Manager errors through", async () => {
  const sent = JSON.stringify({
    value: "http://es:9200",
    expected_version: 0,
    reason: "hk rollout",
  });
  const fetch = vi.fn(async (url: URL, init: RequestInit) => {
    expect(url.origin).toBe("https://us.example.invalid");
    expect(init.method).toBe("PUT");
    expect(init.body).toBe(sent);
    return managerJSON({ error: "version_conflict", version: 3 }, 409);
  });
  vi.stubGlobal("fetch", fetch);
  const response = await runtimeConfig(
    request("/api/v1/admin/config/search.url?region=us", "PUT", sent),
    env,
    "/api/v1/admin/config/search.url",
  );
  expect(response.status).toBe(409);
  expect(((await response.json()) as { data: unknown }).data).toEqual({
    error: "version_conflict",
    version: 3,
  });
});

it("Given a WorkOS session, signs the exact Manager path for the selected region", async () => {
  const configured = {
    ...env,
    US_ACCESS_CLIENT_ID: "us-id",
    US_ACCESS_CLIENT_SECRET: "us-secret",
    HK_ACCESS_CLIENT_ID: "hk-id",
    HK_ACCESS_CLIENT_SECRET: "hk-secret",
    HK_PROVISIONING_SECRET: "h".repeat(40),
    DB: {
      lookup: async () => ({
        user_id: "usr_admin",
        identity_key: "admin@example.invalid",
        region: "us",
      }),
    },
  } as unknown as Env;
  const fetch = vi.fn(async (_url: URL, init: RequestInit) => {
    const headers = new Headers(init.headers);
    expect(headers.get("CF-Access-Client-Id")).toBe("hk-id");
    expect(headers.get("Cf-Access-Jwt-Assertion")).toBeNull();
    const assertion = headers.get("X-Pax-Identity-Assertion");
    expect(assertion).toBeTruthy();
    const proof = JSON.parse(
      atob(assertion!.split(".")[0].replace(/-/g, "+").replace(/_/g, "/")),
    );
    expect(proof).toMatchObject({
      region: "hk",
      home_region: "us",
      method: "POST",
      path: "/api/v1/admin/config/test",
    });
    return managerJSON({ ok: true, latency_ms: 12, message: "connected" });
  });
  vi.stubGlobal("fetch", fetch);
  const signed = request(
    "/api/v1/admin/config/test?region=hk",
    "POST",
    JSON.stringify({ group: "search" }),
  );
  attachWorkosIdentity(signed, {
    identity: "admin@example.invalid",
    subject: "user_admin",
  });
  const response = await runtimeConfig(
    signed,
    configured,
    "/api/v1/admin/config/test",
  );
  expect(response.status).toBe(200);
});

it.each([
  ["missing region", "/api/v1/admin/config", "GET", 400],
  ["unknown region", "/api/v1/admin/config?region=eu", "GET", 400],
  ["wrong method", "/api/v1/admin/config?region=us", "DELETE", 405],
  ["key read", "/api/v1/admin/config/search.url?region=us", "GET", 405],
])(
  "Given %s, rejects without contacting a Manager",
  async (_, path, method, status) => {
    const fetch = vi.fn();
    vi.stubGlobal("fetch", fetch);
    const managerPath = path.split("?")[0];
    const response = await runtimeConfig(
      request(path, method),
      env,
      managerPath,
    );
    expect(response.status).toBe(status);
    expect(fetch).not.toHaveBeenCalled();
  },
);

it("Given an oversized body, rejects it", async () => {
  vi.stubGlobal("fetch", vi.fn());
  const response = await runtimeConfig(
    request("/api/v1/admin/config/test?region=us", "POST", "x".repeat(70_000)),
    env,
    "/api/v1/admin/config/test",
  );
  expect(response.status).toBe(413);
});

it.each([
  ["network", () => Promise.reject(new Error("private upstream detail"))],
  ["redirect", async () => new Response(null, { status: 302 })],
  ["non-json", async () => new Response("<html>login</html>", { status: 200 })],
])(
  "Given a %s failure, reports the region unavailable",
  async (_, upstream) => {
    vi.stubGlobal("fetch", vi.fn(upstream));
    const response = await runtimeConfig(
      request("/api/v1/admin/config?region=hk"),
      env,
      "/api/v1/admin/config",
    );
    expect(response.status).toBe(503);
    const body = await response.text();
    expect(body).toContain("region_unavailable");
    expect(body).not.toContain("private upstream detail");
  },
);

it("Given the public route, authenticates and checks origin before proxying without provisioning", async () => {
  const configured = {
    ...env,
    BOOTSTRAP_ENABLED: "true",
    PUBLIC_ORIGIN: "https://app.example.invalid",
    DIRECTORY_SECRET: "d".repeat(40),
    US_PROVISIONING_SECRET: "u".repeat(40),
    HK_PROVISIONING_SECRET: "h".repeat(40),
  } as Env;
  const identity = vi.fn().mockResolvedValue("admin@example.invalid"),
    lookup = vi.fn(),
    provision = vi.fn();
  const worker = createWorker({ identity, lookup, provision });
  const fetch = vi.fn(async () => managerJSON({ region: "us", entries: [] }));
  vi.stubGlobal("fetch", fetch);
  expect(
    (await worker.fetch(request("/api/v1/admin/config?region=us"), configured))
      .status,
  ).toBe(200);
  expect(lookup).not.toHaveBeenCalled();
  expect(provision).not.toHaveBeenCalled();
  identity.mockRejectedValue(new Error("invalid identity"));
  expect(
    (await worker.fetch(request("/api/v1/admin/config?region=us"), configured))
      .status,
  ).toBe(401);
  const cross = request("/api/v1/admin/config?region=us");
  cross.headers.set("Origin", "https://other.example.invalid");
  expect((await worker.fetch(cross, configured)).status).toBe(403);
  expect(fetch).toHaveBeenCalledTimes(1);
});
