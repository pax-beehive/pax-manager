import { afterEach, describe, expect, it, vi } from "vitest";
import { allowedNativeUserRoute, nativeFetch } from "./native";
import { createWorker, type Env } from "./index";

const base = "https://app.example.invalid";
const loginResponse = (region: string) => ({
  data: {
    protocol: "client_commit_v1",
    region,
    login_id: "login_fixture",
    poll_token: "poll_fixture",
    user_code: "ABC123",
    verification_uri: `https://${region}.example.invalid/paxl-login.html`,
  },
});
const env = () =>
  ({
    PUBLIC_ORIGIN: base,
    NATIVE_API_ENABLED: "true",
    WORKOS_BROWSER_ENABLED: "true",
    US_MANAGER_URL: "https://us.example.invalid",
    HK_MANAGER_URL: "https://hk.example.invalid",
    US_ACCESS_CLIENT_ID: "us-service",
    US_ACCESS_CLIENT_SECRET: "us-secret",
    HK_ACCESS_CLIENT_ID: "hk-service",
    HK_ACCESS_CLIENT_SECRET: "hk-secret",
    NATIVE_START_LIMITER: {
      limit: vi.fn().mockResolvedValue({ success: true }),
    },
    NATIVE_POLL_LIMITER: {
      limit: vi.fn().mockResolvedValue({ success: true }),
    },
  }) as unknown as Env;
const req = (path: string, method = "GET", body?: unknown) =>
  new Request(`${base}/api/v1/native/${path}`, {
    method,
    headers: {
      Authorization: "Bearer paxu_fixture",
      "Content-Type": "application/json",
      "CF-Connecting-IP": "192.0.2.1",
    },
    ...(body === undefined
      ? {}
      : { body: typeof body === "string" ? body : JSON.stringify(body) }),
  });
afterEach(() => vi.unstubAllGlobals());

describe("native gateway", () => {
  it("restricts daemon tools to exact authorized methods and current user", () => {
    for (const path of [
      "nodes/node_1",
      "nodes/node_1/daemon/directories",
      "nodes/node_1/daemon/status",
      "nodes/node_1/daemon/commands/cmd_1",
    ]) {
      expect(allowedNativeUserRoute("GET", "user/self/" + path)).toBe(true);
      expect(allowedNativeUserRoute("DELETE", "user/self/" + path)).toBe(false);
      expect(allowedNativeUserRoute("GET", "user/other/" + path)).toBe(false);
    }
    expect(
      allowedNativeUserRoute("POST", "user/self/nodes/node_1/daemon/upgrade"),
    ).toBe(true);
    for (const suffix of [
      "restart",
      "browser",
      "secret-channel/open",
      "directories/child",
    ]) {
      expect(
        allowedNativeUserRoute(
          "POST",
          "user/self/nodes/node_1/daemon/" + suffix,
        ),
      ).toBe(false);
    }
  });
  it("allows project creation only on the current user's collection", async () => {
    expect(allowedNativeUserRoute("POST", "user/self/projects")).toBe(true);
    for (const [method, path] of [
      ["POST", "user/other/projects"],
      ["POST", "user/self/projects/project_1"],
      ["DELETE", "user/self/projects"],
      ["PATCH", "user/self/projects"],
    ])
      expect(allowedNativeUserRoute(method, path)).toBe(false);
    const upstream = vi.fn().mockResolvedValue(
      Response.json(
        {
          data: {
            project: { project_id: "project_1", display_name: "Mobile" },
          },
        },
        { status: 201 },
      ),
    );
    vi.stubGlobal("fetch", upstream);
    const response = await nativeFetch(
      req("us/user/self/projects", "POST", { display_name: "Mobile" }),
      env(),
    );
    expect(response.status).toBe(201);
    expect(new TextDecoder().decode(upstream.mock.calls[0][1].body)).toBe(
      '{"display_name":"Mobile"}',
    );
  });
  it("keeps mobile session creation limited to exact user-scoped routes", () => {
    for (const [method, path] of [
      ["DELETE", "user/self/nodes/node_1/agents/agent_1/sessions"],
      ["POST", "user/self/nodes/node_1/agents"],
      ["POST", "user/self/projects/project_1/targets"],
      ["POST", "user/other/nodes/node_1/agents/agent_1/sessions"],
      ["POST", "user/self/nodes/node_1/agents/agent_1/sessions/extra"],
      ["GET", "user/self/nodes/../agents"],
    ])
      expect(allowedNativeUserRoute(method, path)).toBe(false);
  });

  it("dispatches native requests without authenticating a browser session", async () => {
    const upstream = vi.fn().mockResolvedValue(Response.json({ data: {} }));
    vi.stubGlobal("fetch", upstream);
    expect(
      (await createWorker().fetch(req("hk/user/self/me"), env())).status,
    ).toBe(200);
    expect(upstream.mock.calls[0][0].href).toBe(
      "https://hk.example.invalid/api/v1/user/self/me",
    );
  });
  it("uses only server credentials and the user key, preserving SSE and mutation headers", async () => {
    const upstream = vi.fn().mockResolvedValue(
      new Response("data: {}\n\n", {
        headers: {
          "Content-Type": "text/event-stream",
          "Set-Cookie": "secret=1",
          "CF-Access-Client-Secret": "bad",
        },
      }),
    );
    vi.stubGlobal("fetch", upstream);
    const r = req(
      "us/user/self/agents/agent_1/sessions/ses_1/encrypted-events?after_cursor=7",
    );
    for (const key of [
      "Cookie",
      "X-User-Email",
      "Cf-Access-Jwt-Assertion",
      "X-Pax-Identity-Assertion",
      "CF-Access-Client-Secret",
    ])
      r.headers.set(key, "forged");
    r.headers.set("Last-Event-ID", "7");
    r.headers.set("Idempotency-Key", "once");
    const response = await nativeFetch(r, env());
    const [url, options] = upstream.mock.calls[0];
    expect(url.search).toBe("?after_cursor=7");
    expect(options.headers.get("Authorization")).toBe("Bearer paxu_fixture");
    expect(options.headers.get("CF-Access-Client-Secret")).toBe("us-secret");
    expect(options.headers.get("Last-Event-ID")).toBe("7");
    expect(options.headers.get("Idempotency-Key")).toBe("once");
    for (const key of [
      "Cookie",
      "X-User-Email",
      "Cf-Access-Jwt-Assertion",
      "X-Pax-Identity-Assertion",
    ])
      expect(options.headers.has(key)).toBe(false);
    expect(response.headers.has("Set-Cookie")).toBe(false);
    expect(response.headers.has("CF-Access-Client-Secret")).toBe(false);
    expect(await response.text()).toBe("data: {}\n\n");
  });
  it.each([
    "us/admin/users",
    "us/user/usr_other/me",
    "us/user/self/api-keys",
    "us/node/identity",
    "xx/user/self/me",
    "us/user/self/nodes/n1/agents/a1/sessions/s1/extra",
    "us/user/self/%6de",
    "us/user/self//me",
    "us/paxl/device-login/start/extra",
  ])("rejects routes outside the allowlist: %s", async (path) => {
    const upstream = vi.fn();
    vi.stubGlobal("fetch", upstream);
    expect((await nativeFetch(req(path), env())).status).toBe(404);
    expect(upstream).not.toHaveBeenCalled();
  });
  it.each(["", "Bearer paxn_node", "Basic paxu_fixture"])(
    "does not accept other credentials: %s",
    async (auth) => {
      const r = req("us/user/self/me");
      r.headers.set("Authorization", auth);
      expect((await nativeFetch(r, env())).status).toBe(401);
    },
  );
  it("retains Manager denial without retrying another region", async () => {
    const upstream = vi
      .fn()
      .mockResolvedValue(
        Response.json({ error: "unauthorized" }, { status: 401 }),
      );
    vi.stubGlobal("fetch", upstream);
    expect((await nativeFetch(req("hk/user/self/me"), env())).status).toBe(401);
    expect(upstream).toHaveBeenCalledTimes(1);
  });
  it("does not follow origin redirects or expose HTML", async () => {
    const upstream = vi
      .fn()
      .mockResolvedValueOnce(
        new Response(null, {
          status: 302,
          headers: { Location: "https://login.example.invalid" },
        }),
      )
      .mockResolvedValueOnce(
        new Response("login", { headers: { "Content-Type": "text/html" } }),
      );
    vi.stubGlobal("fetch", upstream);
    expect((await nativeFetch(req("us/user/self/me"), env())).status).toBe(502);
    expect((await nativeFetch(req("us/user/self/me"), env())).status).toBe(502);
    expect(upstream.mock.calls[0][1].redirect).toBe("manual");
  });
  it("fails closed when disabled, origin credentials are absent, or cross site", async () => {
    expect(
      (
        await nativeFetch(req("us/user/self/me"), {
          ...env(),
          NATIVE_API_ENABLED: "false",
        })
      ).status,
    ).toBe(503);
    expect(
      (
        await nativeFetch(req("us/user/self/me"), {
          ...env(),
          US_ACCESS_CLIENT_SECRET: undefined,
        })
      ).status,
    ).toBe(503);
    const r = req("us/user/self/me");
    r.headers.set("Origin", "https://evil.invalid");
    expect((await nativeFetch(r, env())).status).toBe(403);
  });
  it("allows only safe device login protocol and rate limits unauthenticated calls", async () => {
    const e = env();
    const upstream = vi
      .fn()
      .mockResolvedValue(Response.json(loginResponse("us")));
    vi.stubGlobal("fetch", upstream);
    expect(
      (
        await nativeFetch(
          req("us/paxl/device-login/start", "POST", { protocol: "legacy" }),
          e,
        )
      ).status,
    ).toBe(400);
    const r = req("us/paxl/device-login/start", "POST", {
      protocol: "client_commit_v1",
      client_name: "Android",
    });
    r.headers.delete("Authorization");
    expect((await nativeFetch(r, e)).status).toBe(200);
    expect(upstream.mock.calls[0][1].headers.has("Authorization")).toBe(false);
    vi.mocked(e.NATIVE_START_LIMITER!.limit).mockResolvedValue({
      success: false,
    });
    expect(
      (
        await nativeFetch(
          req("us/paxl/device-login/start", "POST", {
            protocol: "client_commit_v1",
          }),
          e,
        )
      ).status,
    ).toBe(429);
    expect(
      (
        await nativeFetch(
          req("us/paxl/device-login/poll", "POST", {
            login_id: "id",
            poll_token: "token",
            action: "approve",
          }),
          e,
        )
      ).status,
    ).toBe(400);
  });
  it("bounds bodies even without a Content-Length header", async () => {
    expect(
      (
        await nativeFetch(
          req("us/paxl/device-login/start", "POST", "x".repeat(4097)),
          env(),
        )
      ).status,
    ).toBe(413);
    expect(
      (
        await nativeFetch(
          req("us/paxl/device-login/start", "POST", "invalid json"),
          env(),
        )
      ).status,
    ).toBe(400);
  });
  it("uses canonical WorkOS confirmation for HK and rejects an upstream region mismatch", async () => {
    vi.stubGlobal(
      "fetch",
      vi
        .fn()
        .mockResolvedValueOnce(Response.json(loginResponse("hk")))
        .mockResolvedValueOnce(Response.json(loginResponse("us"))),
    );
    const r = () =>
      req("hk/paxl/device-login/start", "POST", {
        protocol: "client_commit_v1",
      });
    const response = await nativeFetch(r(), env());
    expect(response.status).toBe(200);
    const data = ((await response.json()) as ReturnType<typeof loginResponse>)
      .data;
    expect(data.verification_uri).toBe(`${base}/paxl-login.html`);
    expect(data).toHaveProperty(
      "verification_uri_complete",
      `${base}/paxl-login.html?code=ABC123&region=hk`,
    );
    expect(data.poll_token).toBe("poll_fixture");
    expect((await nativeFetch(r(), env())).status).toBe(502);
  });
  it("sends a mutation once and forwards its exact body", async () => {
    const upstream = vi.fn().mockRejectedValue(new Error("lost response"));
    vi.stubGlobal("fetch", upstream);
    const body = '{"session_id":"s1","input":"hello"}';
    expect(
      (
        await nativeFetch(
          req("us/user/self/nodes/n1/agents/a1/conversation", "POST", body),
          env(),
        )
      ).status,
    ).toBe(503);
    expect(upstream).toHaveBeenCalledTimes(1);
    expect(new TextDecoder().decode(upstream.mock.calls[0][1].body)).toBe(body);
  });
});
