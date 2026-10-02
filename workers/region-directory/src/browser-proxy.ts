import type { Env } from "./index";
import type { Region } from "./directory";
import { managerURL } from "./provision";
export function browserPath(path: string): string | null {
  const target = path.startsWith("/api/pax/")
    ? path.slice("/api/pax".length)
    : path;
  if (/%|\\|\/\//.test(target)) return null;
  if (
    /^\/api\/v1\/user\/(self|usr_[A-Za-z0-9_-]{1,124})\//.test(target) ||
    target === "/api/v1/health" ||
    target === "/api/v1/public/paxd/download"
  )
    return target;
  return null;
}
export function crossSite(request: Request, origin: string): boolean {
  const supplied = request.headers.get("Origin");
  return (
    (supplied !== null && supplied !== origin) ||
    request.headers.get("Sec-Fetch-Site") === "cross-site"
  );
}
export function originIdentityHeaders(request: Request): Headers {
  const headers = new Headers();
  // A small allowlist preserves streaming and conditional requests without
  // forwarding browser-supplied service credentials or local auth overrides.
  for (const key of [
    "accept",
    "content-type",
    "last-event-id",
    "range",
    "if-range",
    "if-none-match",
    "if-modified-since",
    "origin",
    "upgrade",
    "sec-websocket-key",
    "sec-websocket-version",
    "sec-websocket-protocol",
    "sec-websocket-extensions",
  ]) {
    const value = request.headers.get(key);
    if (value !== null) headers.set(key, value);
  }
  const token = request.headers.get("Cf-Access-Jwt-Assertion");
  if (token) {
    headers.set("Cf-Access-Jwt-Assertion", token);
    headers.set("Cookie", `CF_Authorization=${token}`);
  }
  headers.set("Accept-Encoding", "identity");
  headers.set("Cache-Control", "no-store");
  return headers;
}
export async function proxyBrowser(
  request: Request,
  env: Env,
  region: Region,
  path: string,
): Promise<Response> {
  const target = new URL(path, managerURL(env, region));
  target.search = new URL(request.url).search;
  const response = await fetch(target, {
    method: request.method,
    headers: originIdentityHeaders(request),
    body:
      request.method === "GET" || request.method === "HEAD"
        ? undefined
        : request.body,
    redirect: "manual",
    signal: request.signal,
    cache: "no-store",
  });
  if (response.status === 101) return response;
  if (
    response.status >= 300 &&
    response.status < 400 &&
    response.status !== 304
  )
    throw new Error("Unexpected origin redirect");
  const headers = new Headers(response.headers);
  for (const key of [
    "Set-Cookie",
    "Content-Length",
    "Content-Encoding",
    "Transfer-Encoding",
    "Access-Control-Allow-Origin",
    "Access-Control-Allow-Credentials",
  ])
    headers.delete(key);
  headers.set("Cache-Control", "no-store");
  headers.set("Vary", "Cookie, Cf-Access-Jwt-Assertion");
  return new Response(response.body, {
    status: response.status,
    statusText: response.statusText,
    headers,
  });
}
export async function probeOrigin(
  request: Request,
  env: Env,
  region: Region,
): Promise<Response> {
  const nonce = new URL(request.url).searchParams.get("nonce");
  if (!nonce || !/^[A-Za-z0-9_-]{1,80}$/.test(nonce))
    return Response.json(
      { error: "invalid_request" },
      { status: 400, headers: { "Cache-Control": "no-store" } },
    );
  const url = new URL("/health", managerURL(env, region));
  url.searchParams.set("probe", crypto.randomUUID());
  const response = await fetch(url, {
    headers: originIdentityHeaders(request),
    redirect: "manual",
    cache: "no-store",
    signal: AbortSignal.timeout(5000),
  });
  await response.body?.cancel();
  if (!response.ok) throw new Error("Region unavailable");
  return Response.json(
    { region, nonce },
    { headers: { "Cache-Control": "no-store" } },
  );
}
