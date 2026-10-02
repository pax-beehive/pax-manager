import type { Assignment } from "./directory";
import type { Env } from "./index";
export function managerURL(env: Env, region: "us" | "hk"): string {
  return region === "us" ? env.US_MANAGER_URL : env.HK_MANAGER_URL;
}
export async function provision(
  assignment: Assignment,
  env: Env,
): Promise<void> {
  const body = JSON.stringify(assignment);
  const timestamp = Math.floor(Date.now() / 1000).toString();
  const encoder = new TextEncoder();
  const secret =
    assignment.region === "us"
      ? env.US_PROVISIONING_SECRET
      : env.HK_PROVISIONING_SECRET;
  const key = await crypto.subtle.importKey(
    "raw",
    encoder.encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  const mac = await crypto.subtle.sign(
    "HMAC",
    key,
    encoder.encode(`pax-region-ensure-v1\n${timestamp}\n${body}`),
  );
  const signature = Array.from(new Uint8Array(mac), (b) =>
    b.toString(16).padStart(2, "0"),
  ).join("");
  const headers: Record<string, string> = {
    "Content-Type": "application/json",
    "X-Pax-Timestamp": timestamp,
    "X-Pax-Signature": signature,
  };
  const clientID =
    assignment.region === "us"
      ? env.US_ACCESS_CLIENT_ID
      : env.HK_ACCESS_CLIENT_ID;
  const clientSecret =
    assignment.region === "us"
      ? env.US_ACCESS_CLIENT_SECRET
      : env.HK_ACCESS_CLIENT_SECRET;
  if (clientID && clientSecret) {
    headers["CF-Access-Client-Id"] = clientID;
    headers["CF-Access-Client-Secret"] = clientSecret;
  }
  const response = await fetch(
    new URL("/internal/users/ensure", managerURL(env, assignment.region)),
    {
      method: "POST",
      headers,
      body,
      redirect: "error",
      signal: AbortSignal.timeout(5000),
    },
  );
  if (!response.ok) throw new Error("Regional provisioning failed");
  const result = (await response.json()) as { data: Assignment };
  if (
    result.data?.user_id !== assignment.user_id ||
    result.data?.region !== assignment.region ||
    result.data?.identity_key !== assignment.identity_key
  )
    throw new Error("Provisioning identity mismatch");
}
