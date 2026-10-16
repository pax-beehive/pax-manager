import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import {
  createLocalJWKSet,
  exportJWK,
  generateKeyPair,
  SignJWT,
  type CryptoKey,
} from "jose";
import {
  createNativeClient,
  nativeCallback,
  verifyNativeToken,
} from "./native-client";
import { createWorker, type Env } from "./index";

const base = "https://app.example.invalid";
const assignment = {
  user_id: "usr_fixture",
  identity_key: "member@example.invalid",
  region: "hk" as const,
};
const env = () =>
  ({
    PUBLIC_ORIGIN: base,
    NATIVE_API_ENABLED: "true",
    WORKOS_ENABLED: "true",
    BOOTSTRAP_ENABLED: "true",
    WORKOS_CLIENT_ID: "client_fixture",
    HK_MANAGER_URL: "https://hk.example.invalid",
    US_MANAGER_URL: "https://us.example.invalid",
    HK_ACCESS_CLIENT_ID: "service",
    HK_ACCESS_CLIENT_SECRET: "secret",
    HK_PROVISIONING_SECRET: "x".repeat(32),
    DB: {
      withSession: () => ({
        getBookmark: () => null,
        prepare: () => ({ bind: () => ({ first: async () => assignment }) }),
      }),
    },
    NATIVE_POLL_LIMITER: {
      limit: vi.fn().mockResolvedValue({ success: true }),
    },
  }) as unknown as Env;
const req = (path: string, method = "GET", body?: unknown) =>
  new Request(`${base}/api/v1/native/client/${path}`, {
    method,
    headers: {
      Authorization: "Bearer fixture",
      "Content-Type": "application/json",
    },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
const setup = () => {
  const deps = {
    verify: vi.fn().mockResolvedValue({
      subject: "user_fixture",
      sessionId: "session_fixture",
    }),
    bind: vi.fn().mockResolvedValue(assignment.identity_key),
    identity: vi.fn().mockResolvedValue(assignment.identity_key),
    lookup: vi.fn().mockResolvedValue({ assignment }),
    assign: vi.fn().mockResolvedValue({ assignment }),
    provision: vi.fn().mockResolvedValue(undefined),
    workos: vi.fn().mockReturnValue({
      userManagement: {
        getUser: vi.fn().mockResolvedValue({
          id: "user_fixture",
          email: assignment.identity_key,
          emailVerified: true,
        }),
        revokeSession: vi.fn().mockResolvedValue(undefined),
      },
    }),
  };
  return { deps, fetch: createNativeClient(deps) };
};
afterEach(() => {
  vi.unstubAllGlobals();
});

describe("native WorkOS user gateway", () => {
  it.each([
    ["POST", "user/self/projects"],
    ["GET", "user/self/nodes/node_1/agents"],
    ["GET", "user/self/nodes/node_1"],
    ["GET", "user/self/nodes/node_1/daemon/directories"],
    ["GET", "user/self/nodes/node_1/daemon/status"],
    ["GET", "user/self/nodes/node_1/daemon/commands/upgrade_1"],
    ["POST", "user/self/nodes/node_1/daemon/upgrade"],
    ["GET", "public/paxd/download"],
    ["GET", "user/self/projects/project_1/targets"],
    ["POST", "user/self/nodes/node_1/agents/agent_1/sessions"],
  ])(
    "authenticates and routes authorized mobile operations: %s %s",
    async (method, path) => {
      const { deps, fetch } = setup();
      const upstream = vi.fn().mockResolvedValue(Response.json({ data: {} }));
      vi.stubGlobal("fetch", upstream);
      const result = await fetch(
        req(
          path,
          method,
          method === "POST"
            ? path === "user/self/projects"
              ? { display_name: "Mobile project" }
              : {
                  session_id: "sess_fixture",
                  pax_config: { cwd: "/workspace" },
                }
            : undefined,
        ),
        env(),
      );
      expect(result.status).toBe(200);
      expect(deps.verify).toHaveBeenCalled();
      const [url, init] = upstream.mock.calls[0];
      expect(String(url)).toBe(`https://hk.example.invalid/api/v1/${path}`);
      expect(init.method).toBe(method);
    },
  );

  it("bootstraps the existing Web account without creating a node or key", async () => {
    const { deps, fetch } = setup();
    const result = await fetch(
      req("bootstrap", "POST", { preferred_region: "us" }),
      env(),
    );
    expect(result.status).toBe(200);
    expect(await result.json()).toMatchObject({
      status: "ready",
      user_id: assignment.user_id,
      region: "hk",
      api_url: base + "/api/v1/native/client/",
    });
    expect(deps.assign).not.toHaveBeenCalled();
    expect(deps.provision).toHaveBeenCalledWith(assignment, expect.anything());
  });
  it("requires explicit region choice only for an unassigned account", async () => {
    const { deps, fetch } = setup();
    deps.lookup.mockResolvedValue({ assignment: null });
    expect(
      await (await fetch(req("bootstrap", "POST", {}), env())).json(),
    ).toMatchObject({ status: "selection_required" });
    expect(deps.provision).not.toHaveBeenCalled();
    await fetch(req("bootstrap", "POST", { preferred_region: "hk" }), env());
    expect(deps.assign).toHaveBeenCalledWith(
      expect.anything(),
      assignment.identity_key,
      "hk",
    );
  });
  it("uses the verified subject and assigned region, strips client identity and preserves SSE", async () => {
    const upstream = vi.fn().mockResolvedValue(
      new Response("data: {}\n\n", {
        headers: {
          "Content-Type": "text/event-stream",
          "Set-Cookie": "secret",
          "CF-Access-Client-Secret": "secret",
        },
      }),
    );
    vi.stubGlobal("fetch", upstream);
    const { fetch } = setup();
    const request = req(
      "user/self/agents/agent_fixture/sessions/session_fixture/events?after_cursor=7",
    );
    for (const name of [
      "Cookie",
      "Cf-Access-Jwt-Assertion",
      "X-Pax-Identity-Assertion",
      "X-User-Email",
      "CF-Access-Client-Secret",
    ])
      request.headers.set(name, "forged");
    request.headers.set("Last-Event-ID", "7");
    request.headers.set("Idempotency-Key", "once");
    const result = await fetch(request, env());
    expect(result.status).toBe(200);
    const [url, options] = upstream.mock.calls[0];
    expect(url.origin).toBe("https://hk.example.invalid");
    expect(url.search).toBe("?after_cursor=7");
    expect(options.headers.get("Authorization")).toBeNull();
    expect(options.headers.get("Cookie")).toBeNull();
    expect(options.headers.get("Cf-Access-Jwt-Assertion")).toBeNull();
    expect(options.headers.get("X-User-Email")).toBeNull();
    expect(options.headers.get("CF-Access-Client-Secret")).toBe("secret");
    expect(options.headers.get("Last-Event-ID")).toBe("7");
    expect(options.headers.get("Idempotency-Key")).toBe("once");
    const proof = JSON.parse(
      atob(
        options.headers
          .get("X-Pax-Identity-Assertion")
          .split(".")[0]
          .replaceAll("-", "+")
          .replaceAll("_", "/"),
      ),
    );
    expect(proof).toMatchObject({
      user_id: assignment.user_id,
      subject: "user_fixture",
      region: "hk",
      home_region: "hk",
    });
    expect(result.headers.get("Set-Cookie")).toBeNull();
    expect(result.headers.get("CF-Access-Client-Secret")).toBeNull();
    expect(await result.text()).toBe("data: {}\n\n");
  });
  it("revokes only the verified session", async () => {
    const { deps, fetch } = setup();
    expect((await fetch(req("logout", "POST", {}), env())).status).toBe(200);
    expect(deps.workos().userManagement.revokeSession).toHaveBeenCalledWith({
      sessionId: "session_fixture",
    });
    expect(
      (
        await fetch(
          req("logout", "POST", { session_id: "someone_else" }),
          env(),
        )
      ).status,
    ).toBe(400);
  });
  it.each([
    "paxl/device-login/start",
    "user/usr_other/me",
    "user/self/nodes/node_fixture/delete",
    "us/user/self/me",
  ])("denies route %s", async (path) => {
    const { deps, fetch } = setup();
    expect((await fetch(req(path), env())).status).toBe(404);
    expect(deps.verify).not.toHaveBeenCalled();
  });
  it("rejects cross-site, query credentials, invalid bootstrap and oversized mutations", async () => {
    const { fetch } = setup();
    const cross = req("bootstrap", "POST", {});
    cross.headers.set("Origin", "https://evil.invalid");
    expect((await fetch(cross, env())).status).toBe(403);
    expect((await fetch(req("user/self/me?token=secret"), env())).status).toBe(
      400,
    );
    expect(
      (
        await fetch(
          req("bootstrap", "POST", { preferred_region: "evil" }),
          env(),
        )
      ).status,
    ).toBe(400);
    expect(
      (
        await fetch(
          req("bootstrap", "POST", { preferred_region: "x".repeat(2000) }),
          env(),
        )
      ).status,
    ).toBe(413);
  });
  it("checks email verification and never provisions on identity failure", async () => {
    const { deps, fetch } = setup();
    deps.workos().userManagement.getUser.mockResolvedValue({
      id: "user_fixture",
      emailVerified: false,
    });
    expect((await fetch(req("bootstrap", "POST", {}), env())).status).toBe(401);
    expect(deps.provision).not.toHaveBeenCalled();
  });
  it("disables old node login through the native dispatcher", async () => {
    for (const region of ["us", "hk"]) {
      const result = await createWorker().fetch(
        new Request(`${base}/api/v1/native/${region}/paxl/device-login/start`, {
          method: "POST",
        }),
        env(),
      );
      expect(result.status).toBe(410);
    }
  });
});

describe("native bearer validation", () => {
  let privateKey: CryptoKey;
  let keys: ReturnType<typeof createLocalJWKSet>;
  beforeAll(async () => {
    const pair = await generateKeyPair("RS256");
    privateKey = pair.privateKey;
    keys = createLocalJWKSet({
      keys: [
        { ...(await exportJWK(pair.publicKey)), kid: "test", alg: "RS256" },
      ],
    });
  });
  const token = async (
    claims = {},
    issuer = "https://api.workos.com/",
    expires = "5m",
  ) =>
    new SignJWT({ sid: "session_fixture", ...claims })
      .setProtectedHeader({ alg: "RS256", kid: "test" })
      .setIssuer(issuer)
      .setSubject("user_fixture")
      .setIssuedAt()
      .setExpirationTime(expires)
      .sign(privateKey);
  const verify = async (value: string) => {
    const request = req("user/self/me");
    request.headers.set("Authorization", "Bearer " + value);
    return verifyNativeToken(request, env(), keys);
  };
  it("accepts signed WorkOS tokens", async () => {
    expect(await verify(await token())).toEqual({
      subject: "user_fixture",
      sessionId: "session_fixture",
    });
  });
  it("rejects expired, wrong issuer, wrong client, missing session and invalid signature", async () => {
    for (const value of [
      await token({}, undefined, "-1s"),
      await token({}, "https://evil.invalid"),
      await token({ aud: "client_other" }),
      await token({ client_id: "client_other" }),
      await token({ sid: null }),
      (await token()).slice(0, -10) + "tampered00",
      "paxu_fixture",
    ])
      await expect(verify(value)).rejects.toMatchObject({ status: 401 });
  });
});

describe("native callback relay", () => {
  const state = "native.android." + "a".repeat(43);
  it("relays only code and state to the fixed app callback without Web cookies", () => {
    const result = nativeCallback(
      new Request(
        `${base}/auth/callback?state=${state}&code=fixture&returnTo=https://evil.invalid&access_token=secret`,
      ),
      env(),
    )!;
    expect(result.status).toBe(303);
    const target = new URL(result.headers.get("Location")!);
    expect(target.protocol).toBe("net.paxtech.workspace:");
    expect(target.host).toBe("auth");
    expect([...target.searchParams.keys()]).toEqual(["state", "code"]);
    expect(result.headers.get("Set-Cookie")).toBeNull();
  });
  it("leaves Web callbacks alone and rejects ambiguous native responses", () => {
    expect(
      nativeCallback(new Request(base + "/auth/callback?state=web"), env()),
    ).toBeNull();
    for (const query of [
      `state=${state}&state=other&code=x`,
      `state=${state}&code=x&error=denied`,
      `state=${state}`,
      "state=native.android.bad&code=x",
    ])
      expect(
        nativeCallback(new Request(base + "/auth/callback?" + query), env())!
          .status,
      ).toBe(400);
  });
});
