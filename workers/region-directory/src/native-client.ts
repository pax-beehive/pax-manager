import {
  createRemoteJWKSet,
  jwtVerify,
  errors,
  type JWTVerifyGetKey,
} from "jose";
import type { Env } from "./index";
import { assign, lookup } from "./directory";
import { bindWorkosIdentity, readWorkosIdentity } from "./workos-identity";
import { workos } from "./workos-auth";
import { attachWorkosIdentity } from "./origin-assertion";
import { crossSite, proxyBrowser } from "./browser-proxy";
import { provision } from "./provision";
import { allowedNativeUserRoute } from "./native";

const resolvers = new Map<string, JWTVerifyGetKey>();
const json = (value: unknown, status = 200) =>
  Response.json(value, {
    status,
    headers: {
      "Cache-Control": "no-store",
      Vary: "Authorization",
      "X-Content-Type-Options": "nosniff",
    },
  });
class ClientFailure extends Error {
  constructor(
    public status: number,
    message: string,
  ) {
    super(message);
  }
}

export async function verifyNativeToken(
  request: Request,
  env: Env,
  keys?: JWTVerifyGetKey,
) {
  const match = /^Bearer ([A-Za-z0-9_.-]{1,16384})$/.exec(
    request.headers.get("Authorization") ?? "",
  );
  if (!match) throw new ClientFailure(401, "unauthorized");
  const client = env.WORKOS_CLIENT_ID;
  if (!client || !/^client_[A-Za-z0-9]+$/.test(client))
    throw new ClientFailure(503, "auth_unavailable");
  if (!keys) {
    keys = resolvers.get(client);
    if (!keys) {
      keys = createRemoteJWKSet(
        new URL(`https://api.workos.com/sso/jwks/${client}`),
      );
      resolvers.clear();
      resolvers.set(client, keys);
    }
  }
  try {
    const { payload } = await jwtVerify(match[1], keys, {
      algorithms: ["RS256"],
      issuer: [
        "https://api.workos.com/",
        `https://api.workos.com/user_management/${client}`,
      ],
      requiredClaims: ["exp", "iat", "sub", "sid"],
    });
    if (
      typeof payload.sub !== "string" ||
      !/^user_[A-Za-z0-9]+$/.test(payload.sub) ||
      typeof payload.sid !== "string" ||
      !/^session_[A-Za-z0-9]+$/.test(payload.sid) ||
      (payload.client_id !== undefined && payload.client_id !== client) ||
      (payload.aud !== undefined &&
        !(Array.isArray(payload.aud) ? payload.aud : [payload.aud]).includes(
          client,
        ))
    )
      throw new ClientFailure(401, "unauthorized");
    return { subject: payload.sub, sessionId: payload.sid };
  } catch (e) {
    if (e instanceof ClientFailure) throw e;
    if (e instanceof errors.JOSEError && e.code !== "ERR_JWKS_TIMEOUT")
      throw new ClientFailure(401, "unauthorized");
    throw new ClientFailure(503, "auth_unavailable");
  }
}

async function boundedBody(
  request: Request,
  limit: number,
): Promise<Uint8Array<ArrayBuffer>> {
  if (
    request.headers.get("Content-Type")?.split(";")[0].trim() !==
    "application/json"
  )
    throw new ClientFailure(415, "json_required");
  const reader = request.body?.getReader();
  const chunks: Uint8Array[] = [];
  let size = 0;
  if (reader)
    try {
      for (;;) {
        const part = await reader.read();
        if (part.done) break;
        size += part.value.length;
        if (size > limit) throw new ClientFailure(413, "request_too_large");
        chunks.push(part.value);
      }
    } finally {
      await reader.cancel();
    }
  const bytes = new Uint8Array(size);
  let offset = 0;
  for (const chunk of chunks) {
    bytes.set(chunk, offset);
    offset += chunk.length;
  }
  return bytes;
}

export function createNativeClient(
  overrides: Partial<{
    verify: typeof verifyNativeToken;
    bind: typeof bindWorkosIdentity;
    identity: typeof readWorkosIdentity;
    lookup: typeof lookup;
    assign: typeof assign;
    provision: typeof provision;
    proxy: typeof proxyBrowser;
    workos: typeof workos;
  }> = {},
) {
  const deps = {
    verify: verifyNativeToken,
    bind: bindWorkosIdentity,
    identity: readWorkosIdentity,
    lookup,
    assign,
    provision,
    proxy: proxyBrowser,
    workos,
    ...overrides,
  };
  return async (request: Request, env: Env): Promise<Response> => {
    const url = new URL(request.url);
    const prefix = "/api/v1/native/client/";
    const path = url.pathname.slice(prefix.length);
    const bootstrap = path === "bootstrap" && request.method === "POST";
    const logout = path === "logout" && request.method === "POST";
    if (
      url.origin !== env.PUBLIC_ORIGIN ||
      !url.pathname.startsWith(prefix) ||
      /[%\\]|\/\//.test(url.pathname) ||
      (!bootstrap &&
        !logout &&
        !allowedNativeUserRoute(request.method, path) &&
        !(request.method === "GET" && path === "public/paxd/download"))
    )
      return json({ error: "not_found" }, 404);
    if (
      env.NATIVE_API_ENABLED !== "true" ||
      env.WORKOS_ENABLED !== "true" ||
      env.BOOTSTRAP_ENABLED !== "true"
    )
      return json({ error: "native_api_not_enabled" }, 503);
    if (crossSite(request, env.PUBLIC_ORIGIN))
      return json({ error: "invalid_origin" }, 403);
    if (
      ((bootstrap || logout) && url.search) ||
      ["key", "api_key", "token", "authorization"].some((k) =>
        url.searchParams.has(k),
      )
    )
      return json({ error: "invalid_request" }, 400);
    try {
      const session = await deps.verify(request, env);
      const bytes = ["GET", "HEAD"].includes(request.method)
        ? undefined
        : await boundedBody(request, bootstrap || logout ? 1024 : 1024 * 1024);
      if (bootstrap || logout) {
        if (!env.NATIVE_POLL_LIMITER)
          throw new ClientFailure(503, "native_limiter_unavailable");
        if (
          !(await env.NATIVE_POLL_LIMITER.limit({ key: session.subject }))
            .success
        )
          throw new ClientFailure(429, "rate_limited");
        let body;
        try {
          body = JSON.parse(new TextDecoder().decode(bytes));
        } catch {
          throw new ClientFailure(400, "invalid_request");
        }
        if (
          !body ||
          typeof body !== "object" ||
          Array.isArray(body) ||
          Object.keys(body).some((k) => k !== "preferred_region" || logout) ||
          (body.preferred_region !== undefined &&
            !["us", "hk"].includes(body.preferred_region))
        )
          throw new ClientFailure(400, "invalid_request");
        if (logout) {
          await deps
            .workos(env)
            .userManagement.revokeSession({ sessionId: session.sessionId });
          return json({ status: "signed_out" });
        }
        const user = await deps
          .workos(env)
          .userManagement.getUser(session.subject);
        if (user.id !== session.subject || !user.emailVerified)
          throw new ClientFailure(401, "unauthorized");
        const identity = await deps.bind(env.DB, user);
        let { assignment } = await deps.lookup(env.DB, identity);
        if (!assignment && body.preferred_region)
          ({ assignment } = await deps.assign(
            env.DB,
            identity,
            body.preferred_region,
          ));
        if (!assignment)
          return json({ status: "selection_required", regions: ["us", "hk"] });
        // Provision only the user, exactly as Web does. No node or user API key is issued.
        await deps.provision(assignment, env);
        return json({
          status: "ready",
          user_id: assignment.user_id,
          email: user.email,
          region: assignment.region,
          api_url: env.PUBLIC_ORIGIN + prefix,
        });
      }
      const identity = await deps.identity(env.DB, session.subject);
      const { assignment } = await deps.lookup(env.DB, identity);
      if (!assignment) return json({ error: "region_selection_required" }, 409);
      // A fresh Request carries only trusted identity in the out-of-band assertion map.
      const forwarded = new Request(request.url, {
        method: request.method,
        headers: request.headers,
        body: bytes,
        signal: request.signal,
      });
      attachWorkosIdentity(forwarded, { identity, subject: session.subject });
      const upstream = await deps.proxy(
        forwarded,
        env,
        assignment.region,
        "/api/v1/" + path,
      );
      const type = upstream.headers.get("Content-Type") ?? "";
      if (
        !type.includes("application/json") &&
        !type.includes("text/event-stream")
      ) {
        await upstream.body?.cancel();
        throw new ClientFailure(502, "native_origin_response");
      }
      const headers = new Headers({
        "Cache-Control": "no-store",
        Vary: "Authorization",
        "X-Content-Type-Options": "nosniff",
      });
      for (const name of ["content-type", "retry-after"]) {
        const value = upstream.headers.get(name);
        if (value) headers.set(name, value);
      }
      return new Response(upstream.body, { status: upstream.status, headers });
    } catch (e) {
      return e instanceof ClientFailure
        ? json({ error: e.message }, e.status)
        : json({ error: "native_service_unavailable" }, 503);
    }
  };
}
export const nativeClientFetch = createNativeClient();

// Native apps use the already-registered HTTPS callback and PKCE. Relay only a
// code/state to one fixed app scheme; never exchange tokens or alter Web cookies.
export function nativeCallback(request: Request, env: Env): Response | null {
  const url = new URL(request.url);
  const state = url.searchParams.get("state") ?? "";
  if (!state.startsWith("native.android.")) return null;
  if (
    env.NATIVE_API_ENABLED !== "true" ||
    request.method !== "GET" ||
    !/^native\.android\.[A-Za-z0-9_-]{43}$/.test(state) ||
    ["state", "code", "error"].some(
      (k) => url.searchParams.getAll(k).length > 1,
    )
  )
    return json({ error: "invalid_login_state" }, 400);
  const code = url.searchParams.get("code");
  const error = url.searchParams.get("error");
  if (
    !!code === !!error ||
    (code && code.length > 4096) ||
    (error && !/^[a-z_]{1,128}$/.test(error))
  )
    return json({ error: "invalid_login_state" }, 400);
  const target = new URL("net.paxtech.workspace://auth/callback");
  target.searchParams.set("state", state);
  target.searchParams.set(code ? "code" : "error", code || error!);
  return new Response(null, {
    status: 303,
    headers: {
      Location: target.href,
      "Cache-Control": "no-store",
      "Referrer-Policy": "no-referrer",
    },
  });
}
