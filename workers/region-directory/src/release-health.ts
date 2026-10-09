import type { Env } from "./index";
import type { Region } from "./directory";

// A dedicated machine credential grants only this read-only diagnostic. It never
// bypasses browser identity verification, provisions users, or returns secrets.
export async function releaseHealth(
  request: Request,
  env: Env,
  region: Region,
): Promise<Response> {
  const json = (data: unknown, status = 200) =>
    Response.json(data, { status, headers: { "Cache-Control": "no-store" } });
  if (new URL(request.url).origin !== env.PUBLIC_ORIGIN)
    return json({ error: "not_found" }, 404);
  if (request.method !== "GET")
    return json({ error: "method_not_allowed" }, 405);
  if (
    !env.RELEASE_PROBE_TOKEN ||
    env.RELEASE_PROBE_TOKEN.length < 32 ||
    !env.WORKER_VERSION?.id
  )
    return json({ error: "release_probe_not_configured" }, 503);
  const actual = request.headers.get("Authorization") || "";
  const digest = async (s: string) =>
    new Uint8Array(
      await crypto.subtle.digest("SHA-256", new TextEncoder().encode(s)),
    );
  const [a, b] = await Promise.all([
    digest(actual),
    digest(`Bearer ${env.RELEASE_PROBE_TOKEN}`),
  ]);
  let difference = 0;
  for (let i = 0; i < a.length; i++) difference |= a[i] ^ b[i];
  if (difference !== 0) return json({ error: "unauthorized" }, 401);
  try {
    const origin = region === "us" ? env.US_MANAGER_URL : env.HK_MANAGER_URL;
    const id =
      region === "us" ? env.US_ACCESS_CLIENT_ID : env.HK_ACCESS_CLIENT_ID;
    const secret =
      region === "us"
        ? env.US_ACCESS_CLIENT_SECRET
        : env.HK_ACCESS_CLIENT_SECRET;
    const url = new URL("/health", origin);
    if (url.protocol !== "https:" || url.username || url.password)
      throw new Error("invalid origin");
    const headers: Record<string, string> = { Accept: "application/json" };
    if (id && secret) {
      headers["CF-Access-Client-Id"] = id;
      headers["CF-Access-Client-Secret"] = secret;
    }
    const response = await fetch(url.href, {
      headers,
      redirect: "manual",
      signal: AbortSignal.timeout(5000),
    });
    if (!response.ok) throw new Error("unavailable");
    const body = await response.text();
    if (body.length > 16384) throw new Error("oversized health");
    const { data } = JSON.parse(body);
    if (
      data?.status !== "ok" ||
      data.region !== region ||
      !Array.isArray(data.region_directory_capabilities)
    )
      throw new Error("incompatible manager");
    return json({
      version_id: env.WORKER_VERSION.id,
      region,
      manager: {
        status: data.status,
        region: data.region,
        region_directory_capabilities: data.region_directory_capabilities,
      },
    });
  } catch {
    return json({ error: "manager_verification_failed", region }, 502);
  }
}
