import { afterEach, expect, it, vi } from "vitest";
import {
  notificationSubscriptions,
  notificationSubscriptionsPath,
} from "./notification-subscriptions";
import type { Env } from "./index";
import { createWorker } from "./index";

const env = {
  US_MANAGER_URL: "https://us.example.invalid",
  HK_MANAGER_URL: "https://hk.example.invalid",
} as Env;
const request = (query = "", method = "GET") =>
  new Request(
    `https://app.example.invalid/api/pax${notificationSubscriptionsPath}${query}`,
    { method, headers: { "Cf-Access-Jwt-Assertion": "original-identity" } },
  );
const snapshot = (region: string) => ({
  region,
  available: true,
  registration_enabled: true,
  updated_at: "2026-10-19T00:00:00Z",
  summary: {
    total: 1,
    enabled: 1,
    disabled: 0,
    revoked: 0,
    expired: 0,
    invalidated: 0,
    enabled_users: 1,
  },
  receivers: [
    {
      receiver_id: "r1",
      user_id: "u1",
      email: "one@example.invalid",
      device_label: "Pixel",
      channel: "web_push",
      revision: 1,
      status: "enabled",
      permission: "granted",
      permission_reported_at: "2026-10-19T00:00:00Z",
      created_at: "2026-10-19T00:00:00Z",
      last_registered_at: "2026-10-19T00:00:00Z",
      latest_delivery: null,
      endpoint: "secret-endpoint",
      record_json: "secret-record",
    },
  ],
  next_cursor: "next",
});
afterEach(() => vi.unstubAllGlobals());

it("Given the public admin route, authenticates and checks origin before aggregation without provisioning", async () => {
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
  const fetch = vi.fn(async (url: URL) =>
    Response.json({
      data: { regions: [snapshot(url.host.startsWith("us") ? "us" : "hk")] },
    }),
  );
  vi.stubGlobal("fetch", fetch);
  expect((await worker.fetch(request(), configured)).status).toBe(200);
  expect(lookup).not.toHaveBeenCalled();
  expect(provision).not.toHaveBeenCalled();
  identity.mockRejectedValue(new Error("invalid identity"));
  expect((await worker.fetch(request(), configured)).status).toBe(401);
  const cross = request();
  cross.headers.set("Origin", "https://other.example.invalid");
  expect((await worker.fetch(cross, configured)).status).toBe(403);
  expect(fetch).toHaveBeenCalledTimes(2);
});

it("Given an administrator request, preserves filters and distinct regional cursors without exposing secrets", async () => {
  const fetch = vi.fn(async (url: URL, init: RequestInit) => {
    expect(new Headers(init.headers).get("Cf-Access-Jwt-Assertion")).toBe(
      "original-identity",
    );
    expect(url.searchParams.get("search")).toBe("pixel");
    const region = url.host.startsWith("us") ? "us" : "hk";
    expect(url.searchParams.get("after")).toBe(
      region === "us" ? "cursor-us" : "cursor-hk",
    );
    return Response.json({ data: { regions: [snapshot(region)] } });
  });
  vi.stubGlobal("fetch", fetch);
  const response = await notificationSubscriptions(
    request("?search=pixel&after_us=cursor-us&after_hk=cursor-hk"),
    env,
  );
  const body = await response.text();
  expect(response.status).toBe(200);
  expect(fetch).toHaveBeenCalledTimes(2);
  expect(body).not.toContain("secret-endpoint");
  expect(body).not.toContain("secret-record");
  expect(body).toContain('"registration_enabled":true');
  expect(JSON.parse(body).data.regions).toHaveLength(2);
});

it.each([401, 403])(
  "Given regional authorization denial %s, returns no partial protected data",
  async (status) => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async (url: URL) =>
        url.host.startsWith("hk")
          ? new Response(null, { status })
          : Response.json({ data: { regions: [snapshot("us")] } }),
      ),
    );
    const response = await notificationSubscriptions(request(), env);
    expect(response.status).toBe(403);
    expect(await response.text()).not.toContain("one@example.invalid");
  },
);

it("Given a regional outage, marks it unavailable instead of reporting zero subscriptions", async () => {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: URL) => {
      if (url.host.startsWith("hk")) throw new Error("private upstream detail");
      return Response.json({ data: { regions: [snapshot("us")] } });
    }),
  );
  const body = (await (
    await notificationSubscriptions(request(), env)
  ).json()) as { data: { regions: unknown[] } };
  expect(body.data.regions[1]).toEqual({
    region: "hk",
    available: false,
    error: "unavailable",
  });
});

it("Given invalid region or write request, makes no upstream calls", async () => {
  const fetch = vi.fn();
  vi.stubGlobal("fetch", fetch);
  expect(
    (await notificationSubscriptions(request("?region=other"), env)).status,
  ).toBe(400);
  expect(
    (await notificationSubscriptions(request("", "POST"), env)).status,
  ).toBe(405);
  expect(fetch).not.toHaveBeenCalled();
});

it("Given a selected region with invalid snapshot, surfaces an explicit unavailable result", async () => {
  const fetch = vi.fn(async () =>
    Response.json({
      data: { regions: [{ ...snapshot("us"), summary: null }] },
    }),
  );
  vi.stubGlobal("fetch", fetch);
  const response = await notificationSubscriptions(request("?region=us"), env);
  expect(fetch).toHaveBeenCalledTimes(1);
  expect(
    ((await response.json()) as { data: { regions: unknown[] } }).data.regions,
  ).toEqual([{ region: "us", available: false, error: "unavailable" }]);
});

it("Given delivery metadata, preserves typed outcomes and strips provider bodies", async () => {
  const source = {
    ...snapshot("us"),
    receivers: [
      {
        ...snapshot("us").receivers[0],
        expires_at: "2026-10-20T00:00:00Z",
        latest_delivery: {
          status: "accepted",
          attempted_at: "2026-10-19T00:00:00Z",
          attempts: 1,
          provider_status: 201,
          error_category: "network_timeout",
          next_attempt_at: "2026-10-19T00:01:00Z",
          response_body: "secret-provider-body",
        },
      },
    ],
  };
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => Response.json({ data: { regions: [source] } })),
  );
  const response = await notificationSubscriptions(request("?region=us"), env);
  const text = await response.text();
  expect(text).toContain('"status":"accepted"');
  expect(text).toContain('"provider_status":201');
  expect(text).not.toContain("secret-provider-body");
});

it.each(["field", "state", "date", "summary", "http", "delivery"])(
  "Given malformed upstream %s, never exposes invalid metadata",
  async (kind) => {
    const source = snapshot("us");
    const row = source.receivers[0] as Record<string, unknown>;
    if (kind === "field") delete row.email;
    if (kind === "state") row.status = "online";
    if (kind === "date") row.created_at = "not-a-date";
    if (kind === "summary") source.summary.total = -1;
    if (kind === "delivery")
      row.latest_delivery = {
        status: "displayed",
        attempts: 1,
        attempted_at: "2026-10-19T00:00:00Z",
      };
    vi.stubGlobal(
      "fetch",
      vi.fn(async () =>
        kind === "http"
          ? new Response(null, { status: 503 })
          : Response.json({ data: { regions: [source] } }),
      ),
    );
    const body = (await (
      await notificationSubscriptions(request("?region=us"), env)
    ).json()) as { data: { regions: unknown[] } };
    expect(body.data.regions).toEqual([
      { region: "us", available: false, error: "unavailable" },
    ]);
  },
);
