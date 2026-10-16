import type { Env } from "./index";
import { lookup, type Region } from "./directory";
const identities = new WeakMap<
  Request,
  { identity: string; subject: string }
>();
export function attachWorkosIdentity(
  request: Request,
  identity: { identity: string; subject: string },
) {
  identities.set(request, identity);
}
export async function addOriginAssertion(
  headers: Headers,
  request: Request,
  env: Env,
  region: Region,
  path: string,
): Promise<Headers> {
  const identity = identities.get(request);
  if (!identity) return headers;
  const clientId =
    region === "us" ? env.US_ACCESS_CLIENT_ID : env.HK_ACCESS_CLIENT_ID;
  const clientSecret =
    region === "us" ? env.US_ACCESS_CLIENT_SECRET : env.HK_ACCESS_CLIENT_SECRET;
  if (env.LOCAL_REGION && env.LOCAL_REGION !== region)
    throw new Error("Foreign regional upstream");
  if (!env.LOCAL_REGION && (!clientId || !clientSecret))
    throw new Error("Origin service credentials unavailable");
  if (!env.LOCAL_REGION) {
    headers.set("CF-Access-Client-Id", clientId!);
    headers.set("CF-Access-Client-Secret", clientSecret!);
  }
  headers.delete("Cf-Access-Jwt-Assertion");
  headers.delete("Cookie");
  // Region probes must work before a new user has chosen their account region.
  if (path === "/health") return headers;
  const { assignment } = await lookup(env.DB, identity.identity);
  if (!assignment) throw new Error("Account region not selected");
  const proof = JSON.stringify({
    user_id: assignment.user_id,
    identity_key: assignment.identity_key,
    home_region: assignment.region,
    region,
    subject: identity.subject,
    method: request.method,
    path,
    timestamp: Math.floor(Date.now() / 1000),
  });
  const encoder = new TextEncoder();
  const secret =
    region === "us" ? env.US_PROVISIONING_SECRET : env.HK_PROVISIONING_SECRET;
  const key = await crypto.subtle.importKey(
    "raw",
    encoder.encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  const signature = await crypto.subtle.sign(
    "HMAC",
    key,
    encoder.encode("pax-browser-identity-v1\n" + proof),
  );
  const encode = (bytes: Uint8Array) =>
    btoa(String.fromCharCode(...bytes))
      .replaceAll("+", "-")
      .replaceAll("/", "_")
      .replace(/=+$/g, "");
  headers.delete("Cf-Access-Jwt-Assertion");
  headers.delete("Cookie");
  headers.set(
    "X-Pax-Identity-Assertion",
    encode(encoder.encode(proof)) + "." + encode(new Uint8Array(signature)),
  );
  return headers;
}
