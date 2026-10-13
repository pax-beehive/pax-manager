import { WorkOS } from "@workos-inc/node/worker";
import { EncryptJWT, jwtDecrypt } from "jose";
import { bindWorkosIdentity, readWorkosIdentity } from "./workos-identity";
import { lookup } from "./directory";
import type { Env } from "./index";

const sessionName = "__Host-pax_session";
const transactionName = "__Host-pax_login";
const maxAge = 30 * 24 * 60 * 60;
const clients = new Map<string, WorkOS>();
export function workos(env: Env): WorkOS {
  if (
    !env.WORKOS_API_KEY ||
    !env.WORKOS_CLIENT_ID ||
    !env.WORKOS_COOKIE_PASSWORD ||
    env.WORKOS_COOKIE_PASSWORD.length < 32
  )
    throw new Error("WorkOS is not configured");
  const key = env.WORKOS_API_KEY + env.WORKOS_CLIENT_ID;
  let client = clients.get(key);
  if (!client) {
    client = new WorkOS(env.WORKOS_API_KEY, { clientId: env.WORKOS_CLIENT_ID });
    client.userManagement.issuer = [
      "https://api.workos.com/",
      "https://api.workos.com/user_management/" + env.WORKOS_CLIENT_ID,
    ];
    clients.clear();
    clients.set(key, client);
  }
  return client;
}
export function cookie(name: string, value: string, age: number): string {
  return `${name}=${value}; Path=/; HttpOnly; Secure; SameSite=Lax; Max-Age=${age}`;
}
export function cookieValue(request: Request, name: string): string {
  const matches = (request.headers.get("Cookie") ?? "")
    .split(";")
    .map((x) => x.trim())
    .filter((x) => x.startsWith(name + "="));
  return matches.length === 1 ? matches[0].slice(name.length + 1) : "";
}
export function sessionData(request: Request): string {
  return Array.from({ length: 4 }, (_, i) =>
    cookieValue(request, `${sessionName}.${i}`),
  ).join("");
}
export function sessionCookies(value: string): string[] {
  if (value.length > 4 * 3500) throw new Error("Session too large");
  return Array.from({ length: 4 }, (_, i) => {
    const part = value.slice(i * 3500, (i + 1) * 3500);
    return cookie(`${sessionName}.${i}`, part, part ? maxAge : 0);
  });
}
export function withCookies(response: Response, cookies: string[]): Response {
  if (!cookies.length) return response;
  const result = new Response(response.body, {
    status: response.status,
    statusText: response.statusText,
    headers: response.headers,
    ...(response.webSocket ? { webSocket: response.webSocket } : {}),
  });
  for (const value of cookies) result.headers.append("Set-Cookie", value);
  return result;
}
export function authJSON(body: unknown, status = 200): Response {
  return Response.json(body, {
    status,
    headers: {
      "Cache-Control": "private, no-store",
      Vary: "Cookie",
      "Referrer-Policy": "no-referrer",
      "X-Content-Type-Options": "nosniff",
    },
  });
}
export class SessionFailure extends Error {
  constructor(
    public status: number,
    public cookies: string[] = [],
  ) {
    super("Session unavailable");
  }
}
export async function authenticateSession(request: Request, env: Env) {
  const data = sessionData(request);
  if (!data) throw new SessionFailure(401);
  const session = workos(env).userManagement.loadSealedSession({
    sessionData: data,
    cookiePassword: env.WORKOS_COOKIE_PASSWORD!,
  });
  const result = await session.authenticate();
  let user,
    sessionId: string,
    cookies: string[] = [];
  if (result.authenticated) {
    user = result.user;
    sessionId = result.sessionId;
  } else {
    if (result.reason !== "invalid_jwt")
      throw new SessionFailure(401, sessionCookies(""));
    const refreshed = await session.refresh();
    if (!refreshed.authenticated)
      throw new SessionFailure(
        refreshed.retryable ? 503 : 401,
        refreshed.retryable ? [] : sessionCookies(""),
      );
    user = refreshed.user;
    sessionId = refreshed.sessionId;
    cookies = sessionCookies(refreshed.sealedSession!);
  }
  if (!user.emailVerified) throw new SessionFailure(401, sessionCookies(""));
  const identity = await readWorkosIdentity(env.DB, user.id);
  return { identity, subject: user.id, sessionId, cookies };
}
export function safeReturnTo(value: string | null): string {
  if (
    !value ||
    !value.startsWith("/") ||
    value.startsWith("//") ||
    /[\\\x00-\x20]/.test(value)
  )
    return "/";
  const url = new URL(value, "https://pax.invalid");
  if (
    url.origin !== "https://pax.invalid" ||
    (url.pathname.startsWith("/auth/") && url.pathname !== "/auth/session")
  )
    return "/";
  return url.pathname + url.search + url.hash;
}
async function transactionKey(env: Env) {
  return new Uint8Array(
    await crypto.subtle.digest(
      "SHA-256",
      new TextEncoder().encode(
        "pax-login-transaction-v1:" + env.WORKOS_COOKIE_PASSWORD,
      ),
    ),
  );
}
function redirect(url: string) {
  return new Response(null, {
    status: 303,
    headers: {
      Location: url,
      "Cache-Control": "no-store",
      "Referrer-Policy": "no-referrer",
    },
  });
}
export async function authRoute(request: Request, env: Env): Promise<Response> {
  const url = new URL(request.url);
  if (env.WORKOS_ENABLED !== "true" || url.origin !== env.PUBLIC_ORIGIN)
    return authJSON({ error: "not_found" }, 404);
  try {
    const client = workos(env);
    if (url.pathname === "/auth/login" && request.method === "GET") {
      const authorization =
        await client.userManagement.getAuthorizationUrlWithPKCE({
          provider: "authkit",
          redirectUri: env.PUBLIC_ORIGIN + "/auth/callback",
        });
      const transaction = await new EncryptJWT({
        state: authorization.state,
        verifier: authorization.codeVerifier,
        returnTo: safeReturnTo(url.searchParams.get("returnTo")),
      })
        .setProtectedHeader({ alg: "dir", enc: "A256GCM" })
        .setAudience("pax-login")
        .setIssuedAt()
        .setExpirationTime("10m")
        .encrypt(await transactionKey(env));
      return withCookies(redirect(authorization.url), [
        cookie(transactionName, transaction, 600),
      ]);
    }
    if (url.pathname === "/auth/callback" && request.method === "GET") {
      const clear = [cookie(transactionName, "", 0)];
      try {
        const { payload } = await jwtDecrypt(
          cookieValue(request, transactionName),
          await transactionKey(env),
          {
            audience: "pax-login",
            keyManagementAlgorithms: ["dir"],
            contentEncryptionAlgorithms: ["A256GCM"],
          },
        );
        const code = url.searchParams.get("code");
        if (
          !code ||
          code.length > 4096 ||
          !payload.state ||
          payload.state !== url.searchParams.get("state") ||
          typeof payload.verifier !== "string"
        )
          return withCookies(
            authJSON({ error: "invalid_login_state" }, 400),
            clear,
          );
        const result = await client.userManagement.authenticateWithCode({
          code,
          codeVerifier: payload.verifier,
          session: {
            sealSession: true,
            cookiePassword: env.WORKOS_COOKIE_PASSWORD,
          },
        });
        await bindWorkosIdentity(env.DB, result.user);
        if (!result.sealedSession) throw new Error("Missing session");
        return withCookies(
          redirect(
            safeReturnTo(
              typeof payload.returnTo === "string" ? payload.returnTo : null,
            ),
          ),
          [...clear, ...sessionCookies(result.sealedSession)],
        );
      } catch {
        return withCookies(
          authJSON({ error: "login_failed", retry: "/auth/login" }, 400),
          clear,
        );
      }
    }
    if (url.pathname === "/auth/session" && request.method === "GET") {
      const session = await authenticateSession(request, env);
      const { assignment } = await lookup(env.DB, session.identity);
      return withCookies(
        authJSON({
          authenticated: true,
          user_id: assignment?.user_id ?? null,
          region: assignment?.region ?? null,
          identity_key: session.identity,
          workos_user_id: session.subject,
        }),
        session.cookies,
      );
    }
    if (url.pathname === "/auth/logout" && request.method === "POST") {
      if (
        request.headers.get("Origin") !== env.PUBLIC_ORIGIN ||
        request.headers.get("Sec-Fetch-Site") === "cross-site"
      )
        return authJSON({ error: "invalid_origin" }, 403);
      let location = env.PUBLIC_ORIGIN;
      try {
        const session = await authenticateSession(request, env);
        location = client.userManagement.getLogoutUrl({
          sessionId: session.sessionId,
          returnTo: env.PUBLIC_ORIGIN + "/",
        });
      } catch (e) {
        if (!(e instanceof SessionFailure && e.status === 401)) throw e;
      }
      return withCookies(redirect(location), [
        ...sessionCookies(""),
        cookie(transactionName, "", 0),
        cookie("__Host-pax_route", "", 0),
        cookie("__Host-pax_region", "", 0),
      ]);
    }
    return authJSON({ error: "not_found" }, 404);
  } catch (e) {
    if (e instanceof SessionFailure)
      return withCookies(
        authJSON(
          { error: e.status === 401 ? "unauthorized" : "auth_unavailable" },
          e.status,
        ),
        e.cookies,
      );
    return authJSON({ error: "auth_unavailable" }, 503);
  }
}
