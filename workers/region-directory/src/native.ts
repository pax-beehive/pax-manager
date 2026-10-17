import type { Env } from "./index";
import { managerURL } from "./provision";
import { crossSite } from "./browser-proxy";

const id = "[A-Za-z0-9_-]{1,128}";
const userRoutes: [string, string][] = [
  ["GET", "(?:me|sessions|projects|nodes|approvals)"],
  ["POST", "projects"],
  ["GET", `sessions/${id}/history`],
  ["GET", `attachments/${id}/content`],
  [
    "GET",
    `(?:artifacts|artifact-publications)/${id}/content/[A-Za-z0-9_-][A-Za-z0-9_.-]{0,127}`,
  ],
  ["GET", `nodes/${id}/agents`],
  ["GET", `nodes/${id}`],
  ["GET", `nodes/${id}/daemon/(?:directories|status)`],
  ["GET", `nodes/${id}/daemon/commands/${id}`],
  ["POST", `nodes/${id}/daemon/upgrade`],
  ["GET", `projects/${id}/targets`],
  ["POST", `nodes/${id}/agents/${id}/sessions`],
  ["GET", `nodes/${id}/agents/${id}/sessions/${id}`],
  ["GET", `agents/${id}/sessions/${id}/(?:events|encrypted-events)`],
  ["POST", `nodes/${id}/agents/${id}/conversation`],
  ["POST", `agents/${id}/sessions/${id}/(?:encrypted-commands|turn/stop)`],
  ["POST", `approvals/${id}/decision`],
  ["POST", `agents/${id}/e2ee/pairings`],
  ["GET", `agents/${id}/e2ee/key-packages/${id}`],
  ["DELETE", `api-keys/${id}`],
];
export const allowedNativeUserRoute = (method: string, path: string) =>
  userRoutes.some(
    ([verb, pattern]) =>
      verb === method && new RegExp(`^user/self/${pattern}$`).test(path),
  );
const json = (error: string, status: number) =>
  Response.json(
    { error },
    { status, headers: { "Cache-Control": "no-store" } },
  );

/** Explicit native-only surface; no browser identity or caller-selected origin. */
export async function nativeFetch(
  request: Request,
  env: Env,
): Promise<Response> {
  const url = new URL(request.url);
  const route = /^\/api\/v1\/native\/(us|hk)\/(.+)$/.exec(url.pathname);
  if (
    url.origin !== env.PUBLIC_ORIGIN ||
    !route ||
    /[%\\]|\/\//.test(url.pathname)
  )
    return json("not_found", 404);
  const region = route[1] as "us" | "hk";
  const path = "/api/v1/" + route[2];
  const login = /^paxl\/device-login\/(start|poll)$/.exec(route[2]);
  const allowed = login
    ? request.method === "POST"
    : userRoutes.some(
        ([method, pattern]) =>
          method === request.method &&
          new RegExp(`^user/self/${pattern}$`).test(route[2]),
      );
  if (!allowed) return json("not_found", 404);
  if (crossSite(request, env.PUBLIC_ORIGIN)) return json("invalid_origin", 403);
  if (env.NATIVE_API_ENABLED !== "true")
    return json("native_api_not_enabled", 503);
  const authorization = request.headers.get("Authorization") ?? "";
  if (!login && !/^Bearer paxu_[A-Za-z0-9_-]{1,4096}$/i.test(authorization))
    return json("user_api_key_required", 401);
  if (
    ["key", "api_key", "token", "authorization"].some((name) =>
      url.searchParams.has(name),
    )
  )
    return json("credential_header_required", 400);
  const clientId =
    region === "us" ? env.US_ACCESS_CLIENT_ID : env.HK_ACCESS_CLIENT_ID;
  const clientSecret =
    region === "us" ? env.US_ACCESS_CLIENT_SECRET : env.HK_ACCESS_CLIENT_SECRET;
  if (!clientId || !clientSecret) return json("native_origin_unavailable", 503);
  try {
    if (login) {
      if (url.search) return json("invalid_request", 400);
      const limiter =
        login[1] === "start"
          ? env.NATIVE_START_LIMITER
          : env.NATIVE_POLL_LIMITER;
      if (!limiter) return json("native_limiter_unavailable", 503);
      const ip = request.headers.get("CF-Connecting-IP");
      if (!ip) return json("native_client_unavailable", 503);
      if (!(await limiter.limit({ key: ip })).success)
        return json("rate_limited", 429);
    }
    let body: Uint8Array<ArrayBuffer> | undefined;
    if (!["GET", "HEAD"].includes(request.method)) {
      if (
        request.headers.get("Content-Type")?.split(";")[0].trim() !==
        "application/json"
      )
        return json("json_required", 415);
      const limit = login ? 4096 : 1024 * 1024;
      const reader = request.body?.getReader();
      const chunks: Uint8Array[] = [];
      let size = 0;
      if (reader) {
        try {
          for (;;) {
            const next = await reader.read();
            if (next.done) break;
            size += next.value.length;
            if (size > limit) return json("request_too_large", 413);
            chunks.push(next.value);
          }
        } finally {
          await reader.cancel();
        }
      }
      body = new Uint8Array(size);
      let offset = 0;
      for (const chunk of chunks) {
        body.set(chunk, offset);
        offset += chunk.length;
      }
      if (login) {
        let parsed;
        try {
          parsed = JSON.parse(new TextDecoder().decode(body));
        } catch {
          return json("invalid_request", 400);
        }
        if (!parsed || typeof parsed !== "object" || Array.isArray(parsed))
          return json("invalid_request", 400);
        const keys =
          login[1] === "start"
            ? ["client_name", "protocol"]
            : ["login_id", "poll_token", "action", "expected_user_id"];
        if (
          Object.keys(parsed).some((key) => !keys.includes(key)) ||
          Object.values(parsed).some((value) => typeof value !== "string")
        )
          return json("invalid_request", 400);
        if (login[1] === "start" && parsed.protocol !== "client_commit_v1")
          return json("client_commit_required", 400);
        if (
          login[1] === "poll" &&
          (!parsed.login_id ||
            !parsed.poll_token ||
            ![undefined, "", "commit", "ack", "cancel"].includes(parsed.action))
        )
          return json("invalid_request", 400);
      }
    }
    const headers = new Headers({
      "CF-Access-Client-Id": clientId,
      "CF-Access-Client-Secret": clientSecret,
      "Cache-Control": "no-store",
      "Accept-Encoding": "identity",
    });
    for (const name of [
      "accept",
      "content-type",
      "last-event-id",
      "idempotency-key",
    ]) {
      const value = request.headers.get(name);
      if (value !== null) headers.set(name, value);
    }
    // Manager resolves the key owner and checks every resource. Never forward
    // browser cookies, user hints, assertions, or client-supplied Access credentials.
    if (!login) headers.set("Authorization", authorization);
    const target = new URL(path, managerURL(env, region));
    target.search = url.search;
    const upstream = await fetch(target, {
      method: request.method,
      headers,
      body,
      redirect: "manual",
      cache: "no-store",
      signal: request.signal,
    });
    if (upstream.status >= 300 && upstream.status < 400) {
      await upstream.body?.cancel();
      return json("native_origin_redirect", 502);
    }
    const contentType = upstream.headers.get("Content-Type") ?? "";
    if (
      !contentType.includes("application/json") &&
      !contentType.includes("text/event-stream")
    ) {
      await upstream.body?.cancel();
      return json("native_origin_response", 502);
    }
    if (login?.[1] === "start" && upstream.ok) {
      const envelope = (await upstream.json()) as {
        data?: Record<string, unknown>;
      };
      const data = envelope.data;
      if (
        !data ||
        data.protocol !== "client_commit_v1" ||
        data.region !== region ||
        typeof data.user_code !== "string" ||
        !/^[A-Z0-9]{6}$/.test(data.user_code) ||
        typeof data.login_id !== "string" ||
        !data.login_id ||
        typeof data.poll_token !== "string" ||
        !data.poll_token
      ) {
        return json("native_login_response", 502);
      }
      // Regional Managers may advertise regional Console hosts. WorkOS and
      // coordinated approval live on the canonical origin, including HK users.
      const verification = new URL("/paxl-login.html", env.PUBLIC_ORIGIN);
      data.verification_uri = verification.href;
      verification.searchParams.set("code", data.user_code);
      verification.searchParams.set("region", region);
      data.verification_uri_complete = verification.href;
      return Response.json(envelope, {
        headers: {
          "Cache-Control": "no-store",
          "X-Content-Type-Options": "nosniff",
        },
      });
    }
    const output = new Headers({
      "Cache-Control": "no-store",
      Vary: "Authorization",
      "X-Content-Type-Options": "nosniff",
    });
    for (const name of ["content-type", "retry-after"]) {
      const value = upstream.headers.get(name);
      if (value) output.set(name, value);
    }
    return new Response(upstream.body, {
      status: upstream.status,
      headers: output,
    });
  } catch {
    return json("native_origin_unavailable", 503);
  }
}
