import { addOriginAssertion } from "./origin-assertion";
import { originIdentityHeaders } from "./browser-proxy";
import { managerURL } from "./provision";
import type { Env } from "./index";
import type { Region } from "./directory";

export const runtimeConfigRoot = "/api/v1/admin/config";

const maxBodyBytes = 64 * 1024;

// Each admin route and the only methods the Manager serves on it. Keys are
// registry keys (lowercase, dots, underscores); anything else is not routed.
const routes: [RegExp, string[]][] = [
  [/^\/api\/v1\/admin\/config$/, ["GET"]],
  [/^\/api\/v1\/admin\/config\/test$/, ["POST"]],
  [/^\/api\/v1\/admin\/config\/audit$/, ["GET"]],
  [/^\/api\/v1\/admin\/config\/audit\/[1-9][0-9]{0,17}\/revert$/, ["POST"]],
  [/^\/api\/v1\/admin\/config\/[a-z0-9_.]{1,128}$/, ["PUT", "DELETE"]],
];

/** Returns the Manager path for a runtime config admin route, or null. */
export function runtimeConfigPath(pathname: string): string | null {
  const target = pathname.startsWith("/api/pax/")
    ? pathname.slice("/api/pax".length)
    : pathname;
  return routes.some(([pattern]) => pattern.test(target)) ? target : null;
}

function allowedMethods(path: string): string[] {
  return routes.find(([pattern]) => pattern.test(path))?.[1] ?? [];
}

const headers = {
  "Content-Type": "application/json",
  "Cache-Control": "private, no-store",
  Vary: "Cookie, Cf-Access-Jwt-Assertion",
};

const respond = (status: number, message: string, error?: string) =>
  Response.json(
    { code: status, message, data: error ? { error } : null },
    { status, headers },
  );

// The admin page selects one region per request with ?region=us|hk. The
// selected Manager authorizes the original identity through the signed origin
// assertion; this proxy never grants access on its own.
export async function runtimeConfig(
  request: Request,
  env: Env,
  path: string,
): Promise<Response> {
  if (!allowedMethods(path).includes(request.method))
    return respond(405, "method not allowed");
  const query = new URL(request.url).searchParams;
  const region = query.get("region");
  if (region !== "us" && region !== "hk")
    return respond(400, "region must be us or hk", "invalid_region");
  const target = new URL(path, managerURL(env, region as Region));
  query.forEach((value, key) => {
    if (key !== "region") target.searchParams.append(key, value);
  });
  let body: string | undefined;
  if (request.method === "PUT" || request.method === "POST") {
    body = await request.text();
    if (body.length > maxBodyBytes)
      return respond(413, "request body too large");
  }
  let response: Response;
  try {
    response = await fetch(target, {
      method: request.method,
      headers: await addOriginAssertion(
        originIdentityHeaders(request),
        request,
        env,
        region as Region,
        path,
      ),
      body,
      redirect: "manual",
      cache: "no-store",
      signal: AbortSignal.any([request.signal, AbortSignal.timeout(15_000)]),
    });
  } catch {
    return respond(503, "region unavailable", "region_unavailable");
  }
  if (response.status >= 300 && response.status < 400) {
    await response.body?.cancel();
    return respond(503, "region unavailable", "region_unavailable");
  }
  const contentType = response.headers.get("Content-Type") ?? "";
  if (!contentType.startsWith("application/json")) {
    await response.body?.cancel();
    return respond(503, "region unavailable", "region_unavailable");
  }
  return new Response(response.body, { status: response.status, headers });
}
