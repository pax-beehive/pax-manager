import routes from "../../../internal/manager/nodepolicy/routes.json";
import type { Env } from "./index";
import { lookupUser, type Region } from "./directory";

export const userHintHeader = "X-Pax-User-ID";
const identityPath = "/api/v1/node/identity";
const validUser = (value: unknown): value is string =>
  typeof value === "string" && /^usr_[A-Za-z0-9_-]{1,124}$/.test(value);
interface NodeIdentity {
  node_id: string;
  user_id: string;
  region: Region;
}
class MachineError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
  ) {
    super(code);
  }
}
function json(data: unknown, status = 200, user?: string): Response {
  const headers = new Headers({
    "Cache-Control": "no-store",
    Vary: "X-Pax-Key, Authorization, X-Pax-User-ID",
  });
  if (user) headers.set(userHintHeader, user);
  return Response.json(data, { status, headers });
}
function allowed(request: Request): boolean {
  const path = new URL(request.url).pathname;
  if (/%|\\|\/\//.test(path)) return false;
  return routes.some(
    (route) =>
      route.method === request.method &&
      route.auth.startsWith("node-key") &&
      new RegExp(
        "^" +
          route.path
            .split("/")
            .map((part) => (part.startsWith(":") ? "[A-Za-z0-9_-]+" : part))
            .join("/") +
          "$",
      ).test(path),
  );
}
function origin(env: Env, region: Region): string {
  return (region === "us" ? env.US_MACHINE_URL : env.HK_MACHINE_URL)!;
}
function configured(env: Env): boolean {
  try {
    const origins = [env.US_MACHINE_URL, env.HK_MACHINE_URL];
    return (
      env.MACHINE_ROUTING_ENABLED === "true" &&
      origins.every(
        (value) =>
          !!value &&
          new URL(value).protocol === "https:" &&
          new URL(value).origin === value &&
          value !== env.MACHINE_PUBLIC_ORIGIN,
      ) &&
      origins[0] !== origins[1]
    );
  } catch {
    return false;
  }
}
async function identify(
  env: Env,
  region: Region,
  key: string,
): Promise<NodeIdentity | null> {
  const response = await fetch(new URL(identityPath, origin(env, region)), {
    headers: {
      "X-Pax-Key": key,
      Accept: "application/json",
      "Cache-Control": "no-store",
    },
    redirect: "manual",
    cache: "no-store",
    signal: AbortSignal.timeout(5000),
  });
  if ([401, 403, 404].includes(response.status)) {
    await response.body?.cancel();
    return null;
  }
  if (!response.ok) {
    await response.body?.cancel();
    throw new Error("Identity origin unavailable");
  }
  const { data } = (await response.json()) as { data?: NodeIdentity };
  if (
    !data ||
    !validUser(data.user_id) ||
    data.region !== region ||
    typeof data.node_id !== "string" ||
    !/^node_[A-Za-z0-9_-]{1,124}$/.test(data.node_id)
  )
    throw new Error("Invalid identity response");
  return data;
}
async function recover(
  request: Request,
  env: Env,
  key: string,
  lookup: typeof lookupUser,
): Promise<NodeIdentity> {
  const hint = request.headers.get(userHintHeader);
  const suggested = validUser(hint) ? await lookup(env.DB, hint) : null;
  const first = suggested?.region ?? "us";
  const order: Region[] = [first, first === "us" ? "hk" : "us"];
  let unavailable = false;
  for (const region of order) {
    let identity: NodeIdentity | null;
    try {
      identity = await identify(env, region, key);
    } catch {
      unavailable = true;
      continue;
    }
    if (!identity) continue;
    // The caller's hint only orders discovery. Authority is the Node Key owner.
    const assignment = await lookup(env.DB, identity.user_id);
    if (!assignment) throw new MachineError(503, "node_directory_unavailable");
    if (assignment.region !== region)
      throw new MachineError(409, "node_region_mismatch");
    return identity;
  }
  throw new MachineError(
    unavailable ? 503 : 401,
    unavailable ? "region_unavailable" : "invalid_node_key",
  );
}
async function proxy(
  request: Request,
  env: Env,
  key: string,
  identity: NodeIdentity,
): Promise<Response> {
  const incoming = new URL(request.url);
  const target = new URL(incoming.pathname, origin(env, identity.region));
  target.search = incoming.search;
  const headers = new Headers({
    "X-Pax-Key": key,
    "Accept-Encoding": "identity",
    "Cache-Control": "no-store",
  });
  for (const name of [
    "accept",
    "content-type",
    "last-event-id",
    "range",
    "if-none-match",
    "if-modified-since",
    "idempotency-key",
    "x-pax-session-id",
    "x-pax-client-id",
    "x-pax-device-id",
    "x-pax-tunnel-id",
    "upgrade",
    "sec-websocket-key",
    "sec-websocket-version",
    "sec-websocket-protocol",
    "sec-websocket-extensions",
  ]) {
    const value = request.headers.get(name);
    if (value !== null) headers.set(name, value);
  }
  // Identity discovery is read-only; forward a business body exactly once.
  const response = await fetch(target, {
    method: request.method,
    headers,
    body: ["GET", "HEAD"].includes(request.method) ? undefined : request.body,
    redirect: "manual",
    cache: "no-store",
    signal: request.signal,
  });
  if (
    response.status >= 300 &&
    response.status < 400 &&
    response.status !== 304
  ) {
    await response.body?.cancel();
    throw new Error("Unexpected machine origin redirect");
  }
  const resultHeaders = new Headers(response.headers);
  for (const name of [
    "Set-Cookie",
    "Content-Length",
    "Content-Encoding",
    "Transfer-Encoding",
    "Access-Control-Allow-Origin",
    "Access-Control-Allow-Credentials",
  ])
    resultHeaders.delete(name);
  resultHeaders.set(userHintHeader, identity.user_id);
  resultHeaders.set("Cache-Control", "no-store");
  resultHeaders.set("Vary", "X-Pax-Key, Authorization, X-Pax-User-ID");
  if (response.status === 101)
    return new Response(null, {
      status: 101,
      headers: resultHeaders,
      webSocket: response.webSocket,
    });
  return new Response(response.body, {
    status: response.status,
    headers: resultHeaders,
  });
}
export async function machineFetch(
  request: Request,
  env: Env,
  lookup: typeof lookupUser = lookupUser,
): Promise<Response> {
  if (!allowed(request)) return json({ error: "not_found" }, 404);
  if (!configured(env))
    return json({ error: "machine_routing_unavailable" }, 503);
  const url = new URL(request.url);
  if (["pax_key", "paxKey", "key"].some((name) => url.searchParams.has(name)))
    return json({ error: "node_key_header_required" }, 400);
  const key =
    request.headers.get("X-Pax-Key") ||
    /^Bearer ([^\s]+)$/i.exec(request.headers.get("Authorization") ?? "")?.[1];
  if (!key || key.length > 4096)
    return json({ error: "invalid_node_key" }, 401);
  try {
    const identity = await recover(request, env, key, lookup);
    if (url.pathname === identityPath)
      return json({ code: 200, data: identity }, 200, identity.user_id);
    return await proxy(request, env, key, identity);
  } catch (error) {
    return json(
      {
        error:
          error instanceof MachineError ? error.code : "region_unavailable",
        retryable: !(error instanceof MachineError) || error.status === 503,
      },
      error instanceof MachineError ? error.status : 503,
    );
  }
}
