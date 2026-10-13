import { beforeEach, expect, it, vi } from "vitest";
import { EncryptJWT } from "jose";
const fake = vi.hoisted(() => ({
  getAuthorizationUrlWithPKCE: vi.fn(),
  authenticateWithCode: vi.fn(),
  loadSealedSession: vi.fn(),
  getLogoutUrl: vi.fn(),
  bind: vi.fn(),
  read: vi.fn(),
  lookup: vi.fn(),
}));
vi.mock("@workos-inc/node/worker", () => ({
  WorkOS: class {
    userManagement = fake;
  },
}));
vi.mock("../src/workos-identity", () => ({
  bindWorkosIdentity: fake.bind,
  readWorkosIdentity: fake.read,
}));
vi.mock("../src/directory", () => ({
  lookup: fake.lookup,
  assign: vi.fn(),
  lookupUser: vi.fn(),
}));
import {
  authRoute,
  authenticateSession,
  cookieValue,
  sessionCookies,
  sessionData,
  safeReturnTo,
} from "../src/workos-auth";
import { createWorker, type Env } from "../src/index";
const env = {
  PUBLIC_ORIGIN: "https://pax.example",
  WORKOS_ENABLED: "true",
  WORKOS_CLIENT_ID: "client_test",
  WORKOS_API_KEY: "sk_test",
  WORKOS_COOKIE_PASSWORD: "s".repeat(40),
} as Env;
const user = { id: "user_test", email: "new@example.com", emailVerified: true };
const request = (path: string, options: RequestInit = {}) =>
  new Request(env.PUBLIC_ORIGIN + path, options);
const headers = { Cookie: "__Host-pax_session.0=sealed" };
beforeEach(() => {
  vi.resetAllMocks();
  fake.read.mockResolvedValue("original@example.com");
  fake.lookup.mockResolvedValue({
    assignment: { user_id: "usr_original", region: "hk" },
  });
});
it("starts SDK PKCE login and encrypts state in a host-only transaction cookie", async () => {
  fake.getAuthorizationUrlWithPKCE.mockResolvedValue({
    url: "https://auth.example/login",
    state: "state",
    codeVerifier: "secret_verifier",
  });
  const result = await authRoute(
    request("/auth/login?returnTo=%2Fauth%2Fsession"),
    env,
  );
  expect(result.status).toBe(303);
  expect(result.headers.get("Location")).toBe("https://auth.example/login");
  expect(result.headers.get("Set-Cookie")).toContain(
    "HttpOnly; Secure; SameSite=Lax",
  );
  expect(result.headers.get("Set-Cookie")).not.toContain("secret_verifier");
  const transaction = result.headers.get("Set-Cookie")!.split(";")[0];
  fake.authenticateWithCode.mockResolvedValue({
    user,
    sealedSession: "new-seal",
  });
  const callback = await authRoute(
    request("/auth/callback?code=once&state=state", {
      headers: { Cookie: transaction },
    }),
    env,
  );
  expect(callback.status).toBe(303);
  expect(callback.headers.get("Location")).toBe("/auth/session");
  expect(fake.bind).toHaveBeenCalledWith(env.DB, user);
  expect(fake.authenticateWithCode).toHaveBeenCalledWith(
    expect.objectContaining({ code: "once", codeVerifier: "secret_verifier" }),
  );
  expect(callback.headers.get("Set-Cookie")).toContain(
    "__Host-pax_session.0=new-seal",
  );
});
it("rejects missing, mismatched and expired callback state before exchanging a code", async () => {
  fake.getAuthorizationUrlWithPKCE.mockResolvedValue({
    url: "https://auth.example",
    state: "right",
    codeVerifier: "verifier",
  });
  const login = await authRoute(request("/auth/login"), env);
  const transaction = login.headers.get("Set-Cookie")!.split(";")[0];
  for (const cookie of ["", transaction]) {
    const result = await authRoute(
      request("/auth/callback?code=stolen&state=wrong", {
        headers: { Cookie: cookie },
      }),
      env,
    );
    expect(result.status).toBe(400);
    expect(result.headers.get("Set-Cookie")).toContain("Max-Age=0");
  }
  const key = new Uint8Array(
    await crypto.subtle.digest(
      "SHA-256",
      new TextEncoder().encode(
        "pax-login-transaction-v1:" + env.WORKOS_COOKIE_PASSWORD,
      ),
    ),
  );
  const expired = await new EncryptJWT({ state: "right", verifier: "verifier" })
    .setProtectedHeader({ alg: "dir", enc: "A256GCM" })
    .setAudience("pax-login")
    .setExpirationTime(1)
    .encrypt(key);
  expect(
    (
      await authRoute(
        request("/auth/callback?code=stolen&state=right", {
          headers: { Cookie: "__Host-pax_login=" + expired },
        }),
        env,
      )
    ).status,
  ).toBe(400);
  expect(fake.authenticateWithCode).not.toHaveBeenCalled();
});
it("fails closed on identity binding conflict without setting a session", async () => {
  fake.getAuthorizationUrlWithPKCE.mockResolvedValue({
    url: "https://auth.example",
    state: "right",
    codeVerifier: "verifier",
  });
  const login = await authRoute(request("/auth/login"), env);
  fake.authenticateWithCode.mockResolvedValue({
    user,
    sealedSession: "never-set",
  });
  fake.bind.mockRejectedValue(new Error("conflict"));
  const result = await authRoute(
    request("/auth/callback?code=once&state=right", {
      headers: { Cookie: login.headers.get("Set-Cookie")!.split(";")[0] },
    }),
    env,
  );
  expect(result.status).toBe(400);
  expect(result.headers.get("Set-Cookie")).not.toContain("never-set");
});
it("returns original account and region without exposing SDK tokens", async () => {
  fake.loadSealedSession.mockReturnValue({
    authenticate: vi.fn().mockResolvedValue({
      authenticated: true,
      user,
      sessionId: "session",
      accessToken: "secret",
    }),
  });
  const result = await authRoute(request("/auth/session", { headers }), env);
  expect(await result.json()).toEqual({
    authenticated: true,
    user_id: "usr_original",
    region: "hk",
    identity_key: "original@example.com",
    workos_user_id: "user_test",
  });
  expect(fake.read).toHaveBeenCalledWith(env.DB, user.id);
});
it("refreshes an expired access token and replaces the sealed cookies", async () => {
  fake.loadSealedSession.mockReturnValue({
    authenticate: vi
      .fn()
      .mockResolvedValue({ authenticated: false, reason: "invalid_jwt" }),
    refresh: vi.fn().mockResolvedValue({
      authenticated: true,
      user,
      sessionId: "s",
      sealedSession: "rotated",
    }),
  });
  const result = await authRoute(request("/auth/session", { headers }), env);
  expect(result.status).toBe(200);
  expect(result.headers.get("Set-Cookie")).toContain("rotated");
});
it.each([true, false])(
  "handles refresh failure with retryable=%s without confusing outages with logout",
  async (retryable) => {
    fake.loadSealedSession.mockReturnValue({
      authenticate: vi
        .fn()
        .mockResolvedValue({ authenticated: false, reason: "invalid_jwt" }),
      refresh: vi.fn().mockResolvedValue({ authenticated: false, retryable }),
    });
    const result = await authRoute(request("/auth/session", { headers }), env);
    expect(result.status).toBe(retryable ? 503 : 401);
    expect(!!result.headers.get("Set-Cookie")).toBe(!retryable);
  },
);
it("rejects missing, invalid and unverified sessions", async () => {
  expect((await authRoute(request("/auth/session"), env)).status).toBe(401);
  for (const result of [
    { authenticated: false, reason: "invalid_session_cookie" },
    { authenticated: true, user: { ...user, emailVerified: false } },
  ]) {
    fake.loadSealedSession.mockReturnValue({
      authenticate: vi.fn().mockResolvedValue(result),
    });
    expect(
      (await authRoute(request("/auth/session", { headers }), env)).status,
    ).toBe(401);
  }
});
it("does not clear a valid cookie on a provider outage", async () => {
  fake.loadSealedSession.mockReturnValue({
    authenticate: vi.fn().mockRejectedValue(new Error("offline")),
  });
  const result = await authRoute(request("/auth/session", { headers }), env);
  expect(result.status).toBe(503);
  expect(result.headers.get("Set-Cookie")).toBeNull();
});
it("requires same-origin POST logout and clears session and routing cookies", async () => {
  expect((await authRoute(request("/auth/logout"), env)).status).toBe(404);
  expect(
    (
      await authRoute(
        request("/auth/logout", {
          method: "POST",
          headers: { Origin: "https://evil.example" },
        }),
        env,
      )
    ).status,
  ).toBe(403);
  fake.loadSealedSession.mockReturnValue({
    authenticate: vi
      .fn()
      .mockResolvedValue({ authenticated: true, user, sessionId: "sid" }),
  });
  fake.getLogoutUrl.mockReturnValue("https://api.workos.com/logout");
  const result = await authRoute(
    request("/auth/logout", {
      method: "POST",
      headers: { ...headers, Origin: env.PUBLIC_ORIGIN },
    }),
    env,
  );
  expect(result.status).toBe(303);
  expect(fake.getLogoutUrl).toHaveBeenCalledWith({
    sessionId: "sid",
    returnTo: env.PUBLIC_ORIGIN + "/",
  });
  expect(result.headers.get("Set-Cookie")).toContain("__Host-pax_route=;");
  const loggedOut = await authRoute(
    request("/auth/logout", {
      method: "POST",
      headers: { Origin: env.PUBLIC_ORIGIN },
    }),
    env,
  );
  expect(loggedOut.status).toBe(303);
});
it("limits routes, origin, methods and feature flags", async () => {
  expect(
    (
      await authRoute(request("/auth/login"), {
        ...env,
        WORKOS_ENABLED: "false",
      })
    ).status,
  ).toBe(404);
  expect(
    (await authRoute(new Request("https://evil.example/auth/login"), env))
      .status,
  ).toBe(404);
  expect(
    (await authRoute(request("/auth/login", { method: "POST" }), env)).status,
  ).toBe(404);
  fake.getAuthorizationUrlWithPKCE.mockResolvedValue({
    url: "https://auth.example",
    state: "s",
    codeVerifier: "v",
  });
  expect(
    (
      await authRoute(
        request("/auth/login", { headers: { "Sec-Fetch-Site": "cross-site" } }),
        env,
      )
    ).status,
  ).toBe(303);
  expect(
    (
      await authRoute(request("/auth/login"), {
        ...env,
        WORKOS_API_KEY: undefined,
      })
    ).status,
  ).toBe(503);
});
it("chunks large sealed sessions and rejects duplicate cookies and open redirects", () => {
  const value = "a".repeat(7100);
  const cookies = sessionCookies(value);
  const joined = cookies.map((x) => x.split(";")[0]).join("; ");
  expect(sessionData(request("/", { headers: { Cookie: joined } }))).toBe(
    value,
  );
  expect(() => sessionCookies("x".repeat(14001))).toThrow();
  expect(
    cookieValue(request("/", { headers: { Cookie: "a=x; a=y" } }), "a"),
  ).toBe("");
  for (const path of [
    null,
    "//evil.example",
    "/\\evil.example",
    "/\nevil",
    "/auth/logout",
    "https://evil.example",
  ])
    expect(safeReturnTo(path)).toBe("/");
  expect(safeReturnTo("/settings?tab=account")).toBe("/settings?tab=account");
});
it("requires WorkOS when enabled and never falls back to a supplied Cloudflare assertion", async () => {
  const result = await createWorker().fetch(
    request("/api/pax/api/v1/user/self/agents", {
      headers: { "Cf-Access-Jwt-Assertion": "legacy" },
    }),
    { ...env, WORKOS_BROWSER_ENABLED: "true" },
  );
  expect(result.status).toBe(401);
  const session = await createWorker().fetch(request("/auth/session"), env);
  expect(session.status).toBe(401);
});
