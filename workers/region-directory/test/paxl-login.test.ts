import { afterEach, expect, it, vi } from "vitest";
import { createWorker, type Env } from "../src/index";

const env = {
  BOOTSTRAP_ENABLED: "true",
  PUBLIC_ORIGIN: "https://pax.example",
  ACCESS_PROVIDERS: "[]",
  DIRECTORY_SECRET: "d".repeat(40),
  US_MANAGER_URL: "https://us.example",
  HK_MANAGER_URL: "https://hk.example",
  US_PROVISIONING_SECRET: "u".repeat(40),
  HK_PROVISIONING_SECRET: "h".repeat(40),
} as Env;
const assignment = {
  user_id: "usr_home",
  identity_key: "owner@example.com",
  region: "hk" as const,
};
const request = (body: unknown, headers = {}) =>
  new Request(env.PUBLIC_ORIGIN + "/api/v1/region/paxl-login/approve", {
    method: "POST",
    headers: { "Content-Type": "application/json", ...headers },
    body: JSON.stringify(body),
  });
afterEach(() => vi.restoreAllMocks());

it("given two codes confirms only the verified home region without writing D1", async () => {
  const assign = vi.fn();
  const provision = vi.fn();
  const worker = createWorker({
    identity: async () => assignment.identity_key,
    lookup: async () => ({ assignment, bookmark: null }),
    assign,
    provision,
  });
  const fetch = vi
    .spyOn(globalThis, "fetch")
    .mockResolvedValue(Response.json({ data: { status: "confirmed" } }));
  const response = await worker.fetch(
    request({ codes: { us: "US1234", hk: "HK1234" } }),
    env,
  );
  expect(response.status).toBe(200);
  expect(fetch).toHaveBeenCalledTimes(1);
  const [target, init] = fetch.mock.calls[0];
  expect(String(target)).toBe(
    "https://hk.example/api/v1/user/self/paxl/device-logins/HK1234/approve",
  );
  expect(JSON.parse(init!.body as string)).toEqual({
    ...assignment,
    user_code: "HK1234",
    purpose: "user",
  });
  expect(new Headers(init!.headers).get("X-Pax-Login-Signature")).toMatch(
    /^[0-9a-f]{64}$/,
  );
  expect(assign).not.toHaveBeenCalled();
  expect(provision).not.toHaveBeenCalled();
});

it("given an explicit other region rejects ordinary login instead of silently redirecting", async () => {
  const worker = createWorker({
    identity: async () => assignment.identity_key,
    lookup: async () => ({ assignment, bookmark: null }),
  });
  const fetch = vi.spyOn(globalThis, "fetch");
  expect(
    (await worker.fetch(request({ code: "US1234", target_region: "us" }), env))
      .status,
  ).toBe(409);
  expect(fetch).not.toHaveBeenCalled();
});

it("given an explicit admin login preserves its target for destination permission checks", async () => {
  const worker = createWorker({
    identity: async () => assignment.identity_key,
    lookup: async () => ({ assignment, bookmark: null }),
  });
  const fetch = vi
    .spyOn(globalThis, "fetch")
    .mockResolvedValue(
      Response.json({ message: "admin required" }, { status: 403 }),
    );
  expect(
    (
      await worker.fetch(
        request({ code: "US1234", target_region: "us", admin: true }),
        env,
      )
    ).status,
  ).toBe(403);
  expect(String(fetch.mock.calls[0][0])).toContain("https://us.example/");
  expect(JSON.parse(fetch.mock.calls[0][1]!.body as string).purpose).toBe(
    "admin",
  );
});

it.each([
  null,
  [],
  {},
  { code: "bad" },
  { code: "ABC123", extra: true },
  { code: "ABC123", target_region: "xx" },
  { code: "ABC123", admin: "yes" },
  { code: "ABC123", admin: true },
  { codes: [] },
  { codes: {} },
  { codes: { xx: "ABC123" } },
  { codes: { us: "bad" } },
  { codes: { us: "ABC123" }, code: "ABC123" },
  { codes: { us: "ABC123" }, admin: true },
])(
  "rejects malformed confirmation without contacting either region: %j",
  async (body) => {
    const worker = createWorker({
      identity: async () => assignment.identity_key,
    });
    const fetch = vi.spyOn(globalThis, "fetch");
    expect((await worker.fetch(request(body), env)).status).toBe(400);
    expect(fetch).not.toHaveBeenCalled();
  },
);

it("rejects oversized, non-JSON and empty confirmation bodies", async () => {
  const worker = createWorker({
    identity: async () => assignment.identity_key,
  });
  for (const req of [
    request({ code: "A".repeat(3000) }),
    request({}, { "Content-Type": "text/plain" }),
    new Request(env.PUBLIC_ORIGIN + "/api/v1/region/paxl-login/approve", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
    }),
  ])
    expect((await worker.fetch(req, env)).status).toBe(400);
});

it("fails closed on missing ownership, missing home request and directory outage", async () => {
  for (const mode of ["missing", "no-code", "outage"]) {
    const worker = createWorker({
      identity: async () => assignment.identity_key,
      lookup: async () => {
        if (mode === "outage") throw new Error("offline");
        return {
          assignment: mode === "missing" ? null : assignment,
          bookmark: null,
        };
      },
    });
    const fetch = vi.spyOn(globalThis, "fetch");
    expect(
      (await worker.fetch(request({ codes: { us: "US1234" } }), env)).status,
    ).toBe(mode === "outage" ? 503 : 409);
    expect(fetch).not.toHaveBeenCalled();
    fetch.mockRestore();
  }
});

it("never follows an origin redirect or falls back after failure", async () => {
  const worker = createWorker({
    identity: async () => assignment.identity_key,
    lookup: async () => ({ assignment, bookmark: null }),
  });
  const fetch = vi.spyOn(globalThis, "fetch").mockResolvedValue(
    new Response(null, {
      status: 302,
      headers: { Location: "https://us.example" },
    }),
  );
  expect((await worker.fetch(request({ code: "ABC123" }), env)).status).toBe(
    503,
  );
  expect(fetch).toHaveBeenCalledTimes(1);
  fetch.mockRejectedValueOnce(new Error("network"));
  expect((await worker.fetch(request({ code: "ABC123" }), env)).status).toBe(
    503,
  );
  expect(fetch).toHaveBeenCalledTimes(2);
});
