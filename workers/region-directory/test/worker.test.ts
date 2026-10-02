import { afterEach, describe, expect, it, vi } from "vitest";
import { createWorker, type Env } from "../src/index";
import { readState, stateCookie } from "../src/state";
const env = {
  BOOTSTRAP_ENABLED: "true",
  PUBLIC_ORIGIN: "https://pax.example",
  DIRECTORY_SECRET: "x".repeat(40),
  ACCESS_PROVIDERS: "[]",
  US_MANAGER_URL: "https://us.example",
  HK_MANAGER_URL: "https://hk.example",
  US_PROVISIONING_SECRET: "u".repeat(40),
  HK_PROVISIONING_SECRET: "h".repeat(40),
} as Env;
const assignment = {
  user_id: "usr_global",
  identity_key: "owner@example.com",
  region: "hk" as const,
};
function request(
  body = "{}",
  headers = {},
  method = "POST",
  path = "/api/v1/region/bootstrap",
) {
  return new Request(env.PUBLIC_ORIGIN + path, {
    method,
    headers: { "Content-Type": "application/json", ...headers },
    ...(method === "POST" ? { body } : {}),
  });
}
function setup() {
  const deps = {
    identity: vi.fn(async () => assignment.identity_key),
    lookup: vi.fn(async () => ({
      assignment: null as typeof assignment | null,
      bookmark: "bookmark",
    })),
    assign: vi.fn(async () => ({ assignment, bookmark: "committed" })),
    provision: vi.fn(async () => {}),
  };
  return { deps, worker: createWorker(deps) };
}
afterEach(() => vi.restoreAllMocks());
describe("verified and idempotent bootstrap", () => {
  it("never assigns or provisions when authentication fails", async () => {
    const { worker, deps } = setup();
    deps.identity.mockRejectedValue(new Error("invalid"));
    expect(
      (await worker.fetch(request('{"preferred_region":"hk"}'), env)).status,
    ).toBe(401);
    expect(deps.assign).not.toHaveBeenCalled();
    expect(deps.provision).not.toHaveBeenCalled();
  });
  it("requires explicit selection for a new account", async () => {
    const { worker, deps } = setup();
    const response = await worker.fetch(request(), env);
    expect(await response.json()).toEqual({
      status: "selection_required",
      regions: ["us", "hk"],
    });
    expect(deps.assign).not.toHaveBeenCalled();
  });
  it("returns the committed winner and provisions that region before caching", async () => {
    const { worker, deps } = setup();
    const response = await worker.fetch(
      request('{"preferred_region":"us"}'),
      env,
    );
    expect(deps.assign).toHaveBeenCalledWith(
      env.DB,
      assignment.identity_key,
      "us",
    );
    expect(deps.provision).toHaveBeenCalledWith(assignment, env);
    expect(await response.json()).toMatchObject({
      status: "ready",
      user_id: "usr_global",
      region: "hk",
    });
    const cookie = response.headers.get("Set-Cookie")!;
    expect(cookie).toContain("HttpOnly; Secure; SameSite=Lax");
    deps.lookup.mockClear();
    deps.provision.mockClear();
    expect(
      (await worker.fetch(request("{}", { Cookie: cookie }), env)).status,
    ).toBe(200);
    expect(deps.lookup).not.toHaveBeenCalled();
    expect(deps.provision).not.toHaveBeenCalled();
    expect(response.headers.get("Cache-Control")).toBe("no-store");
  });
  it("reads with the committed bookmark once the ten second shortcut expires", async () => {
    const { worker, deps } = setup();
    const cookie = await stateCookie(env.DIRECTORY_SECRET, {
      identity: assignment.identity_key,
      assignment,
      bookmark: "committed",
      cached_until: Date.now() - 1,
    });
    deps.lookup.mockResolvedValue({ assignment, bookmark: "newer" });
    expect(
      (await worker.fetch(request("{}", { Cookie: cookie }), env)).status,
    ).toBe(200);
    expect(deps.lookup).toHaveBeenCalledWith(
      env.DB,
      assignment.identity_key,
      "committed",
    );
    expect(deps.assign).not.toHaveBeenCalled();
  });
  it("does not change an existing region and retries failed provisioning with the same ID", async () => {
    const { worker, deps } = setup();
    deps.lookup.mockResolvedValue({ assignment, bookmark: "book" });
    deps.provision.mockRejectedValueOnce(new Error("offline"));
    const first = await worker.fetch(request('{"preferred_region":"us"}'), env);
    expect(first.status).toBe(503);
    expect(first.headers.has("Set-Cookie")).toBe(false);
    expect(
      (await worker.fetch(request('{"preferred_region":"us"}'), env)).status,
    ).toBe(200);
    expect(deps.assign).not.toHaveBeenCalled();
    expect(deps.provision).toHaveBeenNthCalledWith(2, assignment, env);
  });
  it("does not reuse another identity or tampered state", async () => {
    const cookie = await stateCookie(env.DIRECTORY_SECRET, {
      identity: "other@example.com",
      assignment,
      bookmark: "b",
      cached_until: Date.now() + 10000,
    });
    expect(
      await readState(
        request("{}", { Cookie: cookie }),
        env.DIRECTORY_SECRET,
        assignment.identity_key,
      ),
    ).toBeNull();
    expect(
      await readState(
        request("{}", { Cookie: cookie }),
        "wrong",
        "other@example.com",
      ),
    ).toBeNull();
    expect(
      await readState(
        request("{}", { Cookie: "__Host-pax_region=" + "x".repeat(9000) }),
        env.DIRECTORY_SECRET,
        assignment.identity_key,
      ),
    ).toBeNull();
  });
  it.each([
    "null",
    "[]",
    '{"email":"attacker@example.com"}',
    '{"preferred_region":"xx"}',
    "{",
    '{"pad":"' + "x".repeat(1100) + '"}',
  ])("rejects malformed input %s", async (body) => {
    const { worker, deps } = setup();
    expect((await worker.fetch(request(body), env)).status).toBe(400);
    expect(deps.lookup).not.toHaveBeenCalled();
  });
  it("rejects cross origin, missing JSON, missing body, invalid config and unavailable directory", async () => {
    const { worker, deps } = setup();
    for (const req of [
      request("{}", { Origin: "https://evil.example" }),
      request("{}", { "Content-Type": "text/plain" }),
      new Request(env.PUBLIC_ORIGIN + "/api/v1/region/bootstrap", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
      }),
    ])
      expect((await worker.fetch(req, env)).status).toBe(400);
    for (const override of [
      { DIRECTORY_SECRET: "" },
      { PUBLIC_ORIGIN: "bad" },
      { HK_MANAGER_URL: "http://hk.example" },
    ])
      expect(
        (await worker.fetch(request(), { ...env, ...override })).status,
      ).toBe(503);
    deps.lookup.mockRejectedValue(new Error("offline"));
    expect((await worker.fetch(request(), env)).status).toBe(503);
    expect(deps.assign).not.toHaveBeenCalled();
    expect((await worker.fetch(request("{}", {}, "GET"), env)).status).toBe(
      405,
    );
    expect(
      (await worker.fetch(request("{}", {}, "GET", "/wrong"), env)).status,
    ).toBe(404);
  });
});

it("keeps staged deployments from authenticating, assigning or provisioning", async () => {
  const { worker, deps } = setup();
  for (const enabled of [undefined, "false", "yes"]) {
    const response = await worker.fetch(request('{"preferred_region":"hk"}'), {
      ...env,
      BOOTSTRAP_ENABLED: enabled,
    } as Env);
    expect(response.status).toBe(503);
    expect(await response.json()).toEqual({
      error: "region_bootstrap_not_enabled",
      retryable: false,
    });
  }
  expect(deps.identity).not.toHaveBeenCalled();
  expect(deps.lookup).not.toHaveBeenCalled();
  expect(deps.assign).not.toHaveBeenCalled();
  expect(deps.provision).not.toHaveBeenCalled();
});
