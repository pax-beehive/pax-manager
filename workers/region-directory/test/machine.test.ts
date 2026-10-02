import { afterEach, expect, it, vi } from "vitest";
import { createWorker, type Env } from "../src/index";

const env = {
  MACHINE_ROUTING_ENABLED: "true",
  MACHINE_PUBLIC_ORIGIN: "https://machine.example",
  US_MACHINE_URL: "https://us-node.example",
  HK_MACHINE_URL: "https://hk-node.example",
} as Env;
const record = (user_id: string, region: "us" | "hk") => ({ user_id, region });
function setup(hint: string, failingUS = false) {
  const lookupUser = vi.fn(async (_db: unknown, id: string) =>
    id === "usr_real"
      ? record(id, "hk")
      : id === "usr_wrong"
        ? record(id, "us")
        : null,
  );
  const calls: { url: URL; init: RequestInit }[] = [];
  const fetch = vi.fn(async (input: URL, init: RequestInit) => {
    const url = new URL(input);
    calls.push({ url, init });
    expect(new Headers(init.headers).get("X-Pax-Key")).toBe("valid-node-key");
    expect(new Headers(init.headers).get("Cookie")).toBeNull();
    if (url.pathname === "/api/v1/node/identity") {
      if (url.host === "us-node.example")
        return new Response(null, { status: failingUS ? 503 : 401 });
      return Response.json({
        data: { node_id: "node_real", user_id: "usr_real", region: "hk" },
      });
    }
    expect(url.host).toBe("hk-node.example");
    return new Response("data: node-owned\n\n", {
      headers: {
        "Content-Type": "text/event-stream",
        "Set-Cookie": "bad=value",
      },
    });
  });
  vi.stubGlobal("fetch", fetch);
  const worker = createWorker({ lookupUser });
  const request = new Request(
    env.MACHINE_PUBLIC_ORIGIN + "/api/v1/node/status?region=us",
    {
      method: "POST",
      body: '{"node_id":"node_real"}',
      headers: {
        "X-Pax-Key": "valid-node-key",
        "X-Pax-User-ID": hint,
        Cookie: "browser=ignored",
        "X-User-Email": "attacker@example.com",
        "Idempotency-Key": "command-123",
        "X-Pax-Tunnel-ID": "tunnel-123",
      },
    },
  );
  return { worker, request, calls, fetch, lookupUser };
}
afterEach(() => vi.unstubAllGlobals());
it.each(["", "corrupt", "usr_missing", "usr_wrong", "usr_real"])(
  "repairs routing hint %s before forwarding any business request",
  async (hint) => {
    const { worker, request, calls } = setup(hint);
    const response = await worker.fetch(request, env);
    expect(response.status).toBe(200);
    expect(response.headers.get("X-Pax-User-ID")).toBe("usr_real");
    expect(response.headers.get("Cache-Control")).toBe("no-store");
    expect(response.headers.has("Set-Cookie")).toBe(false);
    expect(await response.text()).toBe("data: node-owned\n\n");
    const business = calls.filter(
      (x) => x.url.pathname !== "/api/v1/node/identity",
    );
    expect(business).toHaveLength(1);
    expect(new Headers(business[0].init.headers).get("Idempotency-Key")).toBe(
      "command-123",
    );
    expect(new Headers(business[0].init.headers).get("X-Pax-Tunnel-ID")).toBe(
      "tunnel-123",
    );
    expect(new Headers(business[0].init.headers).has("X-User-Email")).toBe(
      false,
    );
    expect(await new Response(business[0].init.body).text()).toContain(
      "node_real",
    );
  },
);
it("recovers the real identity even when the unrelated region is unavailable", async () => {
  const { worker, request } = setup("", true);
  expect((await worker.fetch(request, env)).headers.get("X-Pax-User-ID")).toBe(
    "usr_real",
  );
});
it("returns actual identity on recovery without proxying business data", async () => {
  const { worker, calls } = setup("usr_wrong");
  const response = await worker.fetch(
    new Request(env.MACHINE_PUBLIC_ORIGIN + "/api/v1/node/identity", {
      headers: { Authorization: "Bearer valid-node-key" },
    }),
    env,
  );
  expect(await response.json()).toEqual({
    code: 200,
    data: { node_id: "node_real", user_id: "usr_real", region: "hk" },
  });
  expect(calls.every((x) => x.url.pathname === "/api/v1/node/identity")).toBe(
    true,
  );
});
it("does not try a business request in another region after an origin failure", async () => {
  const { worker, request, fetch, calls } = setup("usr_real");
  fetch.mockImplementation(async (input, init) => {
    calls.push({ url: new URL(input), init });
    if (new URL(input).pathname === "/api/v1/node/identity")
      return Response.json({
        data: { node_id: "node_real", user_id: "usr_real", region: "hk" },
      });
    throw new Error("network offline");
  });
  expect((await worker.fetch(request, env)).status).toBe(503);
  expect(
    calls.filter((x) => x.url.pathname !== "/api/v1/node/identity"),
  ).toHaveLength(1);
  expect(calls.some((x) => x.url.host === "us-node.example")).toBe(false);
});
it("does not treat an arbitrary user ID as permission to access an account", async () => {
  const { worker, request, fetch } = setup("usr_real");
  fetch.mockResolvedValue(new Response(null, { status: 401 }));
  const response = await worker.fetch(request, env);
  expect(response.status).toBe(401);
  expect(response.headers.has("X-Pax-User-ID")).toBe(false);
});
it("preserves unavailable vs invalid credentials and directory failure", async () => {
  const { worker, request, fetch, lookupUser } = setup("usr_real");
  fetch.mockResolvedValue(new Response(null, { status: 503 }));
  expect((await worker.fetch(request.clone() as Request, env)).status).toBe(
    503,
  );
  lookupUser.mockRejectedValue(new Error("D1 offline"));
  expect((await worker.fetch(request, env)).status).toBe(503);
});
it.each([
  "/api/v1/user/self/me",
  "/internal/users/ensure",
  "/api/v1/node/debug",
  "/api/v1/node/registration/start",
  "/api/v1/node/%73tatus",
])("denies unreviewed or pre-pairing route %s", async (path) => {
  const { worker, fetch } = setup("");
  const response = await worker.fetch(
    new Request(env.MACHINE_PUBLIC_ORIGIN + path, {
      headers: { "X-Pax-Key": "valid-node-key" },
    }),
    env,
  );
  expect(response.status).toBe(404);
  expect(fetch).not.toHaveBeenCalled();
});
it("requires a key in a header and explicit machine configuration", async () => {
  const { worker, request, fetch } = setup("");
  expect(
    (
      await worker.fetch(
        new Request(env.MACHINE_PUBLIC_ORIGIN + "/api/v1/node/identity"),
        env,
      )
    ).status,
  ).toBe(401);
  expect(
    (
      await worker.fetch(request.clone() as Request, {
        ...env,
        MACHINE_ROUTING_ENABLED: "false",
      })
    ).status,
  ).toBe(503);
  expect(
    (
      await worker.fetch(request.clone() as Request, {
        ...env,
        HK_MACHINE_URL: env.MACHINE_PUBLIC_ORIGIN,
      })
    ).status,
  ).toBe(503);
  expect(fetch).not.toHaveBeenCalled();
});
