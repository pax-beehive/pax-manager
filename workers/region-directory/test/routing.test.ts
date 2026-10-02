import { afterEach, describe, expect, it, vi } from "vitest";
import { routeCookie, readRoute } from "../src/routing";
import { createWorker, type Env } from "../src/index";
const assignment = {
  user_id: "usr_original",
  identity_key: "owner@example.com",
  region: "hk" as const,
};
const env = {
  BOOTSTRAP_ENABLED: "true",
  PUBLIC_ORIGIN: "https://pax.example",
  ACCESS_PROVIDERS: "[]",
  DIRECTORY_SECRET: "d".repeat(40),
  ROUTING_KEY_ID: "v1",
  US_MANAGER_URL: "https://us.example",
  HK_MANAGER_URL: "https://hk.example",
  US_PROVISIONING_SECRET: "u".repeat(40),
  HK_PROVISIONING_SECRET: "h".repeat(40),
} as Env;
const request = (path: string, cookie = "", init: RequestInit = {}) =>
  new Request(env.PUBLIC_ORIGIN + path, {
    ...init,
    headers: {
      Cookie: cookie,
      "Cf-Access-Jwt-Assertion": "verified-access",
      ...init.headers,
    },
  });
function setup() {
  const deps = {
    identity: vi.fn(async () => assignment.identity_key),
    lookup: vi.fn(async () => ({ assignment, bookmark: "book" })),
    provision: vi.fn(async () => {}),
    assign: vi.fn(),
  };
  return { deps, worker: createWorker(deps) };
}
afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});
describe("browser route credentials", () => {
  it("binds identity, purpose, expiry and signing key; supports a previous rotation key", async () => {
    const cookie = await routeCookie(assignment, env);
    expect(cookie).toContain("HttpOnly; Secure; SameSite=Lax");
    expect(
      await readRoute(request("/", cookie), env, assignment.identity_key),
    ).toEqual(assignment);
    expect(
      await readRoute(request("/", cookie), env, "other@example.com"),
    ).toBeNull();
    expect(
      await readRoute(
        request("/", cookie.replace(/=./, "=x")),
        env,
        assignment.identity_key,
      ),
    ).toBeNull();
    expect(
      await readRoute(
        request("/", cookie),
        { ...env, DIRECTORY_SECRET: "z".repeat(40) },
        assignment.identity_key,
      ),
    ).toBeNull();
    expect(
      await readRoute(
        request("/", cookie),
        {
          ...env,
          ROUTING_KEY_ID: "v2",
          DIRECTORY_SECRET: "z".repeat(40),
          PREVIOUS_ROUTING_KEY_ID: "v1",
          PREVIOUS_DIRECTORY_SECRET: env.DIRECTORY_SECRET,
        },
        assignment.identity_key,
      ),
    ).toEqual(assignment);
    vi.useFakeTimers();
    vi.setSystemTime(Date.now() + 3_601_000);
    expect(
      await readRoute(request("/", cookie), env, assignment.identity_key),
    ).toBeNull();
  });
  it("proxies only to the signed region, strips untrusted credentials and streams SSE", async () => {
    const { worker, deps } = setup();
    const cookie = await routeCookie(assignment, env);
    const fetch = vi.fn(
      async () =>
        new Response("data: hello\n\n", {
          headers: {
            "Content-Type": "text/event-stream",
            "Set-Cookie": "origin=secret",
            "Cache-Control": "public",
          },
        }),
    );
    vi.stubGlobal("fetch", fetch);
    const response = await worker.fetch(
      request("/api/pax/api/v1/user/self/events?region=us", cookie, {
        headers: {
          "X-Pax-Region": "us",
          Authorization: "Bearer attacker",
          "Last-Event-ID": "cursor",
        },
      }),
      env,
    );
    expect(await response.text()).toBe("data: hello\n\n");
    expect(response.headers.get("Cache-Control")).toBe("no-store");
    expect(response.headers.has("Set-Cookie")).toBe(false);
    const [url, init] = fetch.mock.calls[0] as unknown as [URL, RequestInit];
    expect(url.origin).toBe("https://hk.example");
    expect(url.pathname).toBe("/api/v1/user/self/events");
    const headers = new Headers(init.headers);
    expect(headers.get("Authorization")).toBeNull();
    expect(headers.get("X-Pax-Region")).toBeNull();
    expect(headers.get("Cookie")).toBe("CF_Authorization=verified-access");
    expect(headers.get("Last-Event-ID")).toBe("cursor");
    expect(deps.lookup).not.toHaveBeenCalled();
    expect(deps.provision).not.toHaveBeenCalled();
  });
  it("recovers missing or expired cookies from D1 without allocating or changing region", async () => {
    const { worker, deps } = setup();
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => Response.json({ code: 200, data: {} })),
    );
    const response = await worker.fetch(
      request("/api/pax/api/v1/user/self/me"),
      env,
    );
    expect(response.status).toBe(200);
    expect(response.headers.get("Set-Cookie")).toContain("__Host-pax_route=");
    expect(deps.lookup).toHaveBeenCalled();
    expect(deps.assign).not.toHaveBeenCalled();
  });
  it("fails closed on identity, directory, origin failures and cross-site writes", async () => {
    const { worker, deps } = setup();
    const fetch = vi.fn(async () => {
      throw Error("offline");
    });
    vi.stubGlobal("fetch", fetch);
    expect(
      (await worker.fetch(request("/api/pax/api/v1/user/self/me"), env)).status,
    ).toBe(503);
    expect(fetch).toHaveBeenCalledTimes(1);
    deps.lookup.mockRejectedValue(Error("D1 unavailable"));
    expect(
      (await worker.fetch(request("/api/pax/api/v1/user/self/me"), env)).status,
    ).toBe(503);
    expect(fetch).toHaveBeenCalledTimes(1);
    deps.identity.mockRejectedValue(Error("bad identity"));
    expect(
      (await worker.fetch(request("/api/pax/api/v1/user/self/me"), env)).status,
    ).toBe(401);
    deps.identity.mockResolvedValue(assignment.identity_key);
    expect(
      (
        await worker.fetch(
          request("/api/pax/api/v1/user/self/sessions", "", {
            method: "POST",
            headers: { Origin: "https://evil.example" },
          }),
          env,
        )
      ).status,
    ).toBe(403);
  });
  it("refuses machine/internal paths, wrong hosts and method confusion", async () => {
    const { worker } = setup();
    const fetch = vi.fn();
    vi.stubGlobal("fetch", fetch);
    for (const path of [
      "/api/pax/internal/users/ensure",
      "/api/pax/api/v1/node/events",
      "/api/pax//evil.example/",
      "/api/pax/api/v1/user/self%2f..%2f..%2fnode/events",
    ])
      expect((await worker.fetch(request(path), env)).status).toBe(404);
    expect(
      (
        await worker.fetch(
          new Request("https://wrong.example/api/pax/api/v1/user/self/me"),
          env,
        )
      ).status,
    ).toBe(404);
    expect(fetch).not.toHaveBeenCalled();
  });
  it("checks both real origins without allocating identities or caching probe responses", async () => {
    const { worker, deps } = setup();
    const fetch = vi.fn(async () => new Response("ok"));
    vi.stubGlobal("fetch", fetch);
    for (const region of ["us", "hk"]) {
      const response = await worker.fetch(
        request("/api/v1/region/probe/" + region + "?nonce=nonce"),
        env,
      );
      expect(await response.json()).toMatchObject({ region, nonce: "nonce" });
      expect(response.headers.get("Cache-Control")).toBe("no-store");
    }
    expect(deps.assign).not.toHaveBeenCalled();
    expect(deps.lookup).not.toHaveBeenCalled();
    expect(fetch).toHaveBeenCalledTimes(2);
  });
});
it("streams request bodies, handles HEAD, rejects redirects and preserves websocket upgrade responses", async () => {
  const { worker } = setup();
  const cookie = await routeCookie(assignment, env);
  const fetch = vi.fn(async () => new Response(null, { status: 204 }));
  vi.stubGlobal("fetch", fetch);
  expect(
    (
      await worker.fetch(
        request("/api/pax/api/v1/user/self/sessions", cookie, {
          method: "POST",
          body: '{"name":"test"}',
          headers: {
            Origin: env.PUBLIC_ORIGIN,
            "Content-Type": "application/json",
          },
        }),
        env,
      )
    ).status,
  ).toBe(204);
  const init = (fetch.mock.calls[0] as unknown as [URL, RequestInit])[1];
  expect(await new Response(init.body).text()).toBe('{"name":"test"}');
  expect(
    (
      await worker.fetch(
        request("/api/pax/api/v1/user/self/me", cookie, { method: "HEAD" }),
        env,
      )
    ).status,
  ).toBe(204);
  fetch.mockResolvedValue(
    new Response(null, {
      status: 302,
      headers: { Location: "https://login.example" },
    }),
  );
  expect(
    (await worker.fetch(request("/api/pax/api/v1/user/self/me", cookie), env))
      .status,
  ).toBe(503);
  const upgrade = { status: 101, webSocket: {} } as Response;
  fetch.mockResolvedValue(upgrade);
  expect(
    await worker.fetch(
      request("/api/v1/user/self/agents/agent_a/tunnel", cookie, {
        headers: { Upgrade: "websocket", Origin: env.PUBLIC_ORIGIN },
      }),
      env,
    ),
  ).toBe(upgrade);
});
it("never provisions missing identities during a business request and validates probe errors", async () => {
  const { worker, deps } = setup();
  deps.lookup.mockResolvedValue({
    assignment: null as unknown as typeof assignment,
    bookmark: "b",
  });
  expect(
    (await worker.fetch(request("/api/pax/api/v1/user/self/me"), env)).status,
  ).toBe(409);
  expect(deps.provision).not.toHaveBeenCalled();
  expect(
    (await worker.fetch(request("/api/v1/region/probe/hk"), env)).status,
  ).toBe(400);
  vi.stubGlobal(
    "fetch",
    vi.fn(async () => new Response(null, { status: 503 })),
  );
  expect(
    (await worker.fetch(request("/api/v1/region/probe/hk?nonce=fresh"), env))
      .status,
  ).toBe(503);
  expect(
    (
      await worker.fetch(
        request("/api/v1/region/probe/us?nonce=fresh", "", { method: "POST" }),
        env,
      )
    ).status,
  ).toBe(405);
});
it("issues route credentials on both committed and short-cache bootstrap results", async () => {
  const { worker } = setup();
  const first = await worker.fetch(
    request("/api/v1/region/bootstrap", "", {
      method: "POST",
      body: "{}",
      headers: { "Content-Type": "application/json" },
    }),
    env,
  );
  const cookies = first.headers.getSetCookie();
  expect(cookies).toHaveLength(2);
  const second = await worker.fetch(
    request(
      "/api/v1/region/bootstrap",
      cookies.map((c) => c.split(";")[0]).join("; "),
      {
        method: "POST",
        body: "{}",
        headers: { "Content-Type": "application/json" },
      },
    ),
    env,
  );
  expect(second.headers.get("Set-Cookie")).toContain("__Host-pax_route=");
  await expect(
    routeCookie(assignment, { ...env, ROUTING_KEY_ID: undefined }),
  ).rejects.toThrow();
  expect(
    await readRoute(
      request("/", "__Host-pax_route=" + "x".repeat(5000)),
      env,
      assignment.identity_key,
    ),
  ).toBeNull();
  expect(
    await readRoute(
      request("/", await routeCookie(assignment, env)),
      { ...env, ROUTING_KEY_ID: "unknown" },
      assignment.identity_key,
    ),
  ).toBeNull();
});
