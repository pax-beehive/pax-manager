import {
  assign,
  lookup,
  lookupUser,
  type Assignment,
  type Region,
} from "./directory";
import { machineFetch } from "./machine";
import { verifyIdentity } from "./identity";
import { managerURL, provision } from "./provision";
import { readState, stateCookie } from "./state";
import { readRoute, routeCookie } from "./routing";
import {
  browserPath,
  crossSite,
  probeOrigin,
  proxyBrowser,
} from "./browser-proxy";
export interface Env {
  MACHINE_ROUTING_ENABLED?: string;
  MACHINE_PUBLIC_ORIGIN?: string;
  US_MACHINE_URL?: string;
  HK_MACHINE_URL?: string;
  BOOTSTRAP_ENABLED?: string;
  ROUTING_KEY_ID?: string;
  PREVIOUS_ROUTING_KEY_ID?: string;
  PREVIOUS_DIRECTORY_SECRET?: string;
  DB: D1Database;
  PUBLIC_ORIGIN: string;
  ACCESS_PROVIDERS: string;
  DIRECTORY_SECRET: string;
  US_MANAGER_URL: string;
  HK_MANAGER_URL: string;
  US_PROVISIONING_SECRET: string;
  HK_PROVISIONING_SECRET: string;
  US_ACCESS_CLIENT_ID?: string;
  US_ACCESS_CLIENT_SECRET?: string;
  HK_ACCESS_CLIENT_ID?: string;
  HK_ACCESS_CLIENT_SECRET?: string;
}
const json = (data: unknown, status = 200, cookie?: string) => {
  const headers = new Headers({
    "Content-Type": "application/json",
    "Cache-Control": "no-store",
    Vary: "Cookie, Cf-Access-Jwt-Assertion",
  });
  if (cookie) headers.set("Set-Cookie", cookie);
  return new Response(JSON.stringify(data), { status, headers });
};
function configured(env: Env): boolean {
  const urls = [env.PUBLIC_ORIGIN, env.US_MANAGER_URL, env.HK_MANAGER_URL];
  try {
    return (
      [
        env.DIRECTORY_SECRET,
        env.US_PROVISIONING_SECRET,
        env.HK_PROVISIONING_SECRET,
      ].every(
        (s) =>
          typeof s === "string" && new TextEncoder().encode(s).length >= 32,
      ) &&
      urls.every((s) => {
        const u = new URL(s);
        return (
          u.protocol === "https:" &&
          !u.username &&
          !u.password &&
          !u.search &&
          !u.hash &&
          u.pathname === "/"
        );
      })
    );
  } catch {
    return false;
  }
}
async function preference(
  request: Request,
  origin: string,
): Promise<Region | undefined> {
  if (request.headers.get("Origin") && request.headers.get("Origin") !== origin)
    throw new Error("Invalid origin");
  if (
    request.headers.get("Content-Type")?.split(";")[0].trim() !==
    "application/json"
  )
    throw new Error("Expected JSON");
  const reader = request.body?.getReader();
  if (!reader) throw new Error("Missing body");
  let size = 0;
  const chunks: Uint8Array[] = [];
  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      size += value.length;
      if (size > 1024) throw new Error("Body too large");
      chunks.push(value);
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
  const body = JSON.parse(new TextDecoder().decode(bytes));
  if (
    !body ||
    typeof body !== "object" ||
    Array.isArray(body) ||
    Object.keys(body).some((k) => k !== "preferred_region")
  )
    throw new Error("Invalid body");
  if (
    body.preferred_region !== undefined &&
    body.preferred_region !== "us" &&
    body.preferred_region !== "hk"
  )
    throw new Error("Invalid region");
  return body.preferred_region;
}
const ready = (assignment: Assignment, env: Env) => ({
  status: "ready",
  user_id: assignment.user_id,
  region: assignment.region,
  api_url: managerURL(env, assignment.region),
});
async function readyResponse(assignment: Assignment, env: Env, state?: string) {
  const response = json(ready(assignment, env), 200, state);
  if (env.ROUTING_KEY_ID)
    response.headers.append("Set-Cookie", await routeCookie(assignment, env));
  return response;
}
export function createWorker(
  overrides: Partial<{
    identity: typeof verifyIdentity;
    assign: typeof assign;
    lookup: typeof lookup;
    lookupUser: typeof lookupUser;
    provision: typeof provision;
  }> = {},
) {
  const deps = {
    identity: verifyIdentity,
    assign,
    lookup,
    lookupUser,
    provision,
    ...overrides,
  };
  return {
    async fetch(request: Request, env: Env): Promise<Response> {
      const url = new URL(request.url);
      if (env.MACHINE_PUBLIC_ORIGIN && url.origin === env.MACHINE_PUBLIC_ORIGIN)
        return machineFetch(request, env, deps.lookupUser);
      const bootstrap = url.pathname === "/api/v1/region/bootstrap";
      const probe = /^\/api\/v1\/region\/probe\/(us|hk)$/.exec(url.pathname);
      const path = browserPath(url.pathname);
      if (!bootstrap && !probe && !path)
        return json({ error: "not_found" }, 404);
      if (
        (bootstrap && request.method !== "POST") ||
        (probe && request.method !== "GET")
      )
        return json({ error: "method_not_allowed" }, 405);
      if (env.BOOTSTRAP_ENABLED !== "true")
        return json(
          { error: "region_bootstrap_not_enabled", retryable: false },
          503,
        );
      if (!configured(env))
        return json({ error: "directory_unavailable" }, 503);
      if (url.origin !== new URL(env.PUBLIC_ORIGIN).origin)
        return json({ error: "not_found" }, 404);
      if (!bootstrap && crossSite(request, new URL(env.PUBLIC_ORIGIN).origin))
        return json({ error: "invalid_origin" }, 403);
      let identity: string;
      try {
        identity = await deps.identity(request, env.ACCESS_PROVIDERS);
      } catch {
        return json({ error: "unauthorized" }, 401);
      }
      if (!bootstrap) {
        try {
          if (probe) return await probeOrigin(request, env, probe[1] as Region);
          let assignment = await readRoute(request, env, identity);
          let cookie: string | undefined;
          if (!assignment) {
            const result = await deps.lookup(env.DB, identity);
            assignment = result.assignment;
            if (!assignment)
              return json({ error: "region_selection_required" }, 409);
            await deps.provision(assignment, env, request);
            cookie = await routeCookie(assignment, env);
          }
          const response = await proxyBrowser(
            request,
            env,
            assignment.region,
            path!,
          );
          if (cookie && response.status !== 101)
            response.headers.append("Set-Cookie", cookie);
          return response;
        } catch {
          return json({ error: "region_unavailable", retryable: true }, 503);
        }
      }
      let region: Region | undefined;
      try {
        region = await preference(request, new URL(env.PUBLIC_ORIGIN).origin);
      } catch {
        return json({ error: "invalid_request" }, 400);
      }
      try {
        const state = await readState(request, env.DIRECTORY_SECRET, identity);
        if (state && state.cached_until > Date.now())
          return readyResponse(state.assignment, env);
        let result = await deps.lookup(env.DB, identity, state?.bookmark);
        if (!result.assignment && region)
          result = await deps.assign(env.DB, identity, region);
        if (!result.assignment)
          return json({ status: "selection_required", regions: ["us", "hk"] });
        await deps.provision(result.assignment, env, request);
        const cookie = await stateCookie(env.DIRECTORY_SECRET, {
          identity,
          assignment: result.assignment,
          bookmark: result.bookmark,
          cached_until: Date.now() + 10_000,
        });
        return readyResponse(result.assignment, env, cookie);
      } catch {
        return json({ error: "directory_unavailable", retryable: true }, 503);
      }
    },
  };
}
export default createWorker();
