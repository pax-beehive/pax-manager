import { beforeEach, expect, it, vi } from "vitest";
import { createGateway, type ProxyPlan } from "./gateway";
import { gatewayConfig } from "./config";
import { directoryHeaders, ReplicatedDirectory } from "./directory";
import { SessionFailure } from "../workos-auth";
import { addOriginAssertion, attachWorkosIdentity } from "../origin-assertion";

export const fixtureEnv = {
  REGIONAL_DIRECTORY_READY: "true",
  PAX_REGION: "hk",
  DIRECTORY_AUTHORITY_REGION: "us",
  US_PUBLIC_ORIGIN: "https://us.example",
  HK_PUBLIC_ORIGIN: "https://hk.example",
  DIRECTORY_PEER_URL: "https://us.example",
  DIRECTORY_REPLICATION_SECRET: "r".repeat(40),
  REGION_PROVISIONING_SECRET: "p".repeat(40),
  WORKOS_COOKIE_PASSWORD: "c".repeat(40),
  WORKOS_CLIENT_ID: "client_fixture",
  WORKOS_API_KEY: "fixture",
  DIRECTORY_DATABASE_URL: "postgresql://fixture",
  REGIONAL_MANAGER_URL: "http://127.0.0.1:9879",
  REGIONAL_CONSOLE_URL: "http://127.0.0.1:3000",
};
const config = gatewayConfig(fixtureEnv);
const account = {
  user_id: "usr_fixture",
  identity_key: "fixture@example.com",
  region: "hk" as const,
};
function fixture() {
  const directory = {
    lookup: vi.fn(async () => account as typeof account | null),
    assign: vi.fn(async () => account),
    bind: vi.fn(async () => account.identity_key),
    readIdentity: vi.fn(async () => account.identity_key),
    preparePeer: vi.fn(async () => {}),
    execute: vi.fn<ReplicatedDirectory["execute"]>(async () => ({})),
  };
  const authenticate = vi.fn(async () => ({
    identity: account.identity_key,
    subject: "user_fixture",
    sessionId: "session_fixture",
    cookies: ["refreshed=1; Secure"],
  }));
  const provision = vi.fn(async () => {});
  const provider = {
    userManagement: {
      getUser: vi.fn(async () => ({
        id: "user_fixture",
        email: account.identity_key,
        emailVerified: true,
      })),
      revokeSession: vi.fn(async () => {}),
    },
  };
  const approve = vi.fn(async () => Response.json({ code: 0 }));
  const auth = vi.fn(async () => new Response(null, { status: 303 }));
  const verifyNative = vi.fn(async () => ({
    subject: "user_fixture",
    sessionId: "session_fixture",
  }));
  const app = createGateway(
    config,
    directory as unknown as ReplicatedDirectory,
    {
      approve,
      authenticate,
      provision,
      auth,
      verifyNative,
      workos: (() => provider) as never,
    },
  );
  return {
    ...app,
    approve,
    directory,
    authenticate,
    provision,
    auth,
    verifyNative,
    provider,
  };
}
let app: ReturnType<typeof fixture>;
beforeEach(() => {
  app = fixture();
});
function request(
  path: string,
  body?: unknown,
  headers: Record<string, string> = {},
) {
  return new Request(`https://hk.example${path}`, {
    method: body === undefined ? "GET" : "POST",
    body: body === undefined ? undefined : JSON.stringify(body),
    headers: {
      ...(body === undefined ? {} : { "Content-Type": "application/json" }),
      ...headers,
    },
  });
}
async function response(
  path: string,
  body?: unknown,
  headers?: Record<string, string>,
) {
  const result = await app.handle(request(path, body, headers));
  expect(result).toBeInstanceOf(Response);
  return result as Response;
}
it("given a local account, forwards a signed local request with no CF identity or credentials", async () => {
  const result = (await app.handle(
    request("/api/pax/api/v1/user/self/me", undefined, {
      "Cf-Access-Jwt-Assertion": "attacker",
      "CF-Access-Client-Secret": "attacker",
      "X-User-Email": "admin@example.com",
      Authorization: "Bearer attacker",
    }),
  )) as ProxyPlan;
  expect(result.target).toBe(config.managerURL);
  expect(result.path).toBe("/api/v1/user/self/me");
  expect(result.cookies).toEqual(["refreshed=1; Secure"]);
  for (const name of [
    "cookie",
    "cf-access-jwt-assertion",
    "cf-access-client-secret",
    "authorization",
    "x-user-email",
  ])
    expect(result.headers!.has(name)).toBe(false);
  const proof = result.headers!.get("x-pax-identity-assertion")!;
  expect(
    JSON.parse(Buffer.from(proof.split(".")[0], "base64url").toString()),
  ).toMatchObject({
    user_id: account.user_id,
    subject: "user_fixture",
    region: "hk",
    method: "GET",
    path: result.path,
  });
  expect(app.directory.preparePeer).not.toHaveBeenCalled();
  expect(app.provision).not.toHaveBeenCalled();
});
it("given a US account at HK, returns its fixed origin without proxying or modifying the request", async () => {
  app.directory.lookup.mockResolvedValue({ ...account, region: "us" } as never);
  const result = await response("/api/v1/user/self/projects", {
    display_name: "project",
  });
  expect(result.status).toBe(409);
  expect(await result.json()).toMatchObject({
    error: "region_mismatch",
    origin: "https://us.example",
  });
  expect(app.provision).not.toHaveBeenCalled();
  expect(app.directory.assign).not.toHaveBeenCalled();
});
it("restores fixed region and confirms the home account on bootstrap only", async () => {
  expect(
    await (
      await response("/api/v1/region/bootstrap", { preferred_region: "us" })
    ).json(),
  ).toMatchObject({ region: "hk", origin: "https://hk.example" });
  expect(app.directory.assign).not.toHaveBeenCalled();
  expect(app.provision).toHaveBeenCalledWith(account, app.env);
  app.directory.lookup.mockResolvedValue({ ...account, region: "us" } as never);
  expect(
    await (await response("/api/v1/region/bootstrap", {})).json(),
  ).toMatchObject({ region: "us" });
  expect(app.directory.preparePeer).toHaveBeenCalledWith(account.identity_key);
});
it("never guesses a region after a miss, and registers only an explicit choice", async () => {
  app.directory.lookup.mockResolvedValue(null);
  expect(
    await (await response("/api/v1/region/bootstrap", {})).json(),
  ).toMatchObject({ status: "selection_required" });
  expect(app.directory.assign).not.toHaveBeenCalled();
  expect((await response("/api/v1/user/self/me")).status).toBe(409);
  expect(
    (await response("/api/v1/region/bootstrap", { preferred_region: "hk" }))
      .status,
  ).toBe(200);
  expect(app.directory.assign).toHaveBeenCalledWith(account.identity_key, "hk");
});
it("supports native bearer bootstrap, local API access and provider logout", async () => {
  expect(
    await (await response("/api/v1/native/client/bootstrap", {})).json(),
  ).toMatchObject({ api_url: "https://hk.example/api/v1/native/client/" });
  expect(app.provider.userManagement.getUser).toHaveBeenCalledWith(
    "user_fixture",
  );
  expect(
    (
      (await app.handle(
        request("/api/v1/native/client/user/self/me"),
      )) as ProxyPlan
    ).target,
  ).toBe(config.managerURL);
  expect((await response("/api/v1/native/client/logout", {})).status).toBe(200);
  expect(app.provider.userManagement.revokeSession).toHaveBeenCalledWith({
    sessionId: "session_fixture",
  });
  app.provider.userManagement.getUser.mockResolvedValueOnce({
    id: "user_other",
    email: account.identity_key,
    emailVerified: true,
  });
  expect((await response("/api/v1/native/client/bootstrap", {})).status).toBe(
    401,
  );
  expect(app.authenticate).not.toHaveBeenCalled();
});
it("keeps auth endpoints local and refuses unknown APIs, origins and malformed bootstrap", async () => {
  expect((await response("/auth/login")).status).toBe(303);
  expect(app.auth).toHaveBeenCalled();
  expect(await (await response("/health")).json()).toEqual({ region: "hk" });
  expect(await (await response("/api/region-config")).json()).toMatchObject({
    mode: "direct",
    origin: "https://hk.example",
  });
  expect(
    ((await app.handle(request("/?session_id=fixture"))) as ProxyPlan).target,
  ).toBe(config.consoleURL);
  for (const path of [
    "/internal/users/ensure",
    "/api/v1/node/register",
    "/api/pax/evil",
    "/api/v1/native/client/evil",
  ])
    expect((await response(path)).status).toBe(404);
  expect(
    (
      await response("/api/v1/user/self/me", undefined, {
        Origin: "https://evil.example",
      })
    ).status,
  ).toBe(403);
  expect((await response("/api/v1/region/bootstrap")).status).toBe(400);
  expect((await response("/api/v1/region/bootstrap?x=1", {})).status).toBe(400);
  expect(
    (
      await response(
        "/api/v1/region/bootstrap",
        {},
        { "Content-Type": "text/plain" },
      )
    ).status,
  ).toBe(415);
  for (const body of [
    null,
    [],
    { preferred_region: "eu" },
    { evil: true },
    { x: "x".repeat(2000) },
  ])
    expect((await response("/api/v1/region/bootstrap", body)).status).toBe(400);
  expect(
    ((await app.handle(new Request("https://evil.example/health"))) as Response)
      .status,
  ).toBe(404);
});
it("distinguishes authentication and availability errors without exposing provider details", async () => {
  app.authenticate.mockRejectedValueOnce(new SessionFailure(401, ["clear=1"]));
  const unauthorized = await response("/api/v1/user/self/me");
  expect(unauthorized.status).toBe(401);
  expect(unauthorized.headers.getSetCookie()).toEqual(["clear=1"]);
  app.authenticate.mockRejectedValueOnce(new SessionFailure(503));
  expect((await response("/api/v1/user/self/me")).status).toBe(503);
  app.verifyNative.mockRejectedValueOnce({ status: 401 });
  expect((await response("/api/v1/native/client/user/self/me")).status).toBe(
    401,
  );
  app.directory.lookup.mockRejectedValue(
    new Error("sensitive provider failure"),
  );
  const unavailable = await response("/api/v1/user/self/me");
  expect(unavailable.status).toBe(503);
  expect(await unavailable.text()).not.toContain("sensitive");
});
it("requires a signed peer RPC and refuses signing for a foreign Manager", async () => {
  expect(
    (
      await response("/internal/account-directory", {
        op: "changes",
        after: "0",
      })
    ).status,
  ).toBe(401);
  const command = { op: "prepare", identity: account.identity_key };
  const result = await response(
    "/internal/account-directory",
    command,
    directoryHeaders(config.secret, JSON.stringify(command)),
  );
  expect(result.status).toBe(200);
  const prepare = app.directory.execute.mock.calls[0][1] as unknown as (
    row: unknown,
  ) => Promise<void>;
  await prepare(account);
  await expect(prepare({ ...account, region: "us" })).rejects.toThrow();
  const req = request("/api/v1/user/self/me");
  attachWorkosIdentity(req, {
    subject: "user_fixture",
    identity: account.identity_key,
  });
  await expect(
    addOriginAssertion(
      new Headers(),
      req,
      app.env,
      "us",
      "/api/v1/user/self/me",
    ),
  ).rejects.toThrow("Foreign");
});
it("validates deployment settings before accepting traffic", () => {
  expect(config.authority).toBe(false);
  expect(
    gatewayConfig({
      ...fixtureEnv,
      PAX_REGION: "us",
      DIRECTORY_PEER_URL: "https://hk.example",
      REGIONAL_ENTRY_ORIGIN: "https://us.example",
    }).entry,
  ).toBe(true);
  for (const patch of [
    { REGIONAL_DIRECTORY_READY: "false" },
    { PAX_REGION: "eu" },
    { DIRECTORY_AUTHORITY_REGION: "eu" },
    { WORKOS_API_KEY: "" },
    { US_PUBLIC_ORIGIN: "http://us.example" },
    { US_PUBLIC_ORIGIN: "https://us.example/path" },
    { US_PUBLIC_ORIGIN: "https://user:pass@us.example" },
    { HK_PUBLIC_ORIGIN: "https://us.example" },
    { DIRECTORY_REPLICATION_SECRET: "short" },
    { DIRECTORY_REPLICATION_SECRET: fixtureEnv.REGION_PROVISIONING_SECRET },
    { DIRECTORY_PEER_URL: "https://hk.example" },
    { PORT: "0" },
    { PORT: "NaN" },
  ])
    expect(() => gatewayConfig({ ...fixtureEnv, ...patch })).toThrow();
});

it("keeps CLI confirmation on the local home Manager", async () => {
  expect(
    (await response("/api/v1/region/paxl-login/approve", { code: "ABC123" }))
      .status,
  ).toBe(200);
  expect(app.approve).toHaveBeenCalledWith(
    expect.any(Request),
    app.env,
    account.identity_key,
  );
  app.directory.lookup.mockResolvedValue({ ...account, region: "us" } as never);
  expect(
    (await response("/api/v1/region/paxl-login/approve", { code: "ABC123" }))
      .status,
  ).toBe(409);
  expect(app.approve).toHaveBeenCalledTimes(1);
  expect(
    (await response("/api/v1/native/client/user/self/me?token=leak")).status,
  ).toBe(400);
});
it("validates native logout before revoking and retains the native bootstrap email", async () => {
  expect(
    await (await response("/api/v1/native/client/bootstrap", {})).json(),
  ).toMatchObject({ email: account.identity_key });
  for (const body of [null, [], { injected: true }, { text: "x".repeat(2000) }])
    expect((await response("/api/v1/native/client/logout", body)).status).toBe(
      400,
    );
  expect(
    (
      await response(
        "/api/v1/native/client/logout",
        {},
        { "Content-Type": "text/plain" },
      )
    ).status,
  ).toBe(415);
  expect(app.provider.userManagement.revokeSession).not.toHaveBeenCalled();
  const limited = createGateway(
    config,
    app.directory as unknown as ReplicatedDirectory,
    { verifyNative: app.verifyNative, limitNative: () => false },
  );
  expect(
    (
      (await limited.handle(
        request("/api/v1/native/client/bootstrap", {}),
      )) as Response
    ).status,
  ).toBe(429);
});
it("relays the existing Android callback scheme on the direct regional origin", async () => {
  const callbackApp = createGateway(
    config,
    app.directory as unknown as ReplicatedDirectory,
  );
  const result = (await callbackApp.handle(
    request(
      "/auth/callback?state=native.android." + "a".repeat(43) + "&code=one",
    ),
  )) as Response;
  expect(result.status).toBe(303);
  expect(result.headers.get("Location")).toContain(
    "net.paxtech.workspace://auth/callback?",
  );
});
