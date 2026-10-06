import { lookup, type Region } from "./directory";
import { originIdentityHeaders } from "./browser-proxy";
import { managerURL } from "./provision";
import type { Env } from "./index";

const fail = (status: number, message: string) =>
  Response.json(
    { code: status, message },
    { status, headers: { "Cache-Control": "no-store" } },
  );
const region = (value: unknown): value is Region =>
  value === "us" || value === "hk";
const code = (value: unknown): value is string =>
  typeof value === "string" && /^[A-Z0-9]{6}$/.test(value);

async function input(request: Request) {
  if (request.headers.get("Content-Type")?.split(";")[0] !== "application/json")
    throw new Error("Expected JSON");
  const reader = request.body?.getReader();
  if (!reader) throw new Error("Missing body");
  const chunks: Uint8Array[] = [];
  let size = 0;
  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (done) break;
      size += value.length;
      if (size > 2048) throw new Error("Body too large");
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
    Object.keys(body).some(
      (k) => !["codes", "code", "target_region", "admin"].includes(k),
    )
  )
    throw new Error("Invalid body");
  if (body.admin !== undefined && typeof body.admin !== "boolean")
    throw new Error("Invalid admin option");
  if (body.target_region !== undefined && !region(body.target_region))
    throw new Error("Invalid region");
  if (body.codes !== undefined) {
    if (
      body.code !== undefined ||
      body.target_region !== undefined ||
      body.admin ||
      !body.codes ||
      typeof body.codes !== "object" ||
      Array.isArray(body.codes) ||
      Object.keys(body.codes).some((k) => !region(k) || !code(body.codes[k])) ||
      Object.keys(body.codes).length === 0
    )
      throw new Error("Invalid regional codes");
  } else if (!code(body.code)) throw new Error("Invalid code");
  if (body.admin && !body.target_region)
    throw new Error("Admin login requires an explicit target");
  return body as {
    codes?: Partial<Record<Region, string>>;
    code?: string;
    target_region?: Region;
    admin?: boolean;
  };
}

// Only the existing directory is read; login state stays in the client and Managers.
export async function approvePaxlLogin(
  request: Request,
  env: Env,
  identity: string,
  read = lookup,
): Promise<Response> {
  let body: Awaited<ReturnType<typeof input>>;
  try {
    body = await input(request);
  } catch {
    return fail(400, "Invalid login confirmation");
  }
  try {
    const { assignment } = await read(env.DB, identity);
    if (!assignment)
      return fail(
        409,
        "Choose your account region before confirming this login",
      );
    const target = body.target_region ?? assignment.region;
    if (target !== assignment.region && !body.admin)
      return fail(409, "This login targets a different account region");
    const userCode = body.codes ? body.codes[target] : body.code;
    if (!userCode)
      return fail(
        409,
        "The login request for your account region is unavailable; retry from the CLI",
      );
    const proof = JSON.stringify({
      identity_key: identity,
      user_id: assignment.user_id,
      region: target,
      user_code: userCode,
      purpose: body.admin ? "admin" : "user",
    });
    const timestamp = Math.floor(Date.now() / 1000).toString();
    const encoder = new TextEncoder();
    const secret =
      target === "us" ? env.US_PROVISIONING_SECRET : env.HK_PROVISIONING_SECRET;
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
      encoder.encode(`pax-login-approval-v1\n${timestamp}\n${proof}`),
    );
    const headers = originIdentityHeaders(request);
    headers.set("Content-Type", "application/json");
    headers.set("X-Pax-Login-Timestamp", timestamp);
    headers.set(
      "X-Pax-Login-Signature",
      Array.from(new Uint8Array(mac), (v) =>
        v.toString(16).padStart(2, "0"),
      ).join(""),
    );
    const response = await fetch(
      new URL(
        `/api/v1/user/self/paxl/device-logins/${userCode}/approve`,
        managerURL(env, target),
      ),
      {
        method: "POST",
        headers,
        body: proof,
        redirect: "manual",
        signal: AbortSignal.timeout(10000),
      },
    );
    if (response.status >= 300 && response.status < 400) {
      await response.body?.cancel();
      return fail(503, "The selected login region is unavailable");
    }
    return new Response(response.body, {
      status: response.status,
      headers: {
        "Content-Type": "application/json",
        "Cache-Control": "no-store",
      },
    });
  } catch {
    return fail(
      503,
      "The selected login region is unavailable; retry the same request",
    );
  }
}
