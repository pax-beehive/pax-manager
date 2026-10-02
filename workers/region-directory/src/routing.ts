import { decodeProtectedHeader, jwtVerify, SignJWT } from "jose";
import type { Assignment } from "./directory";
import type { Env } from "./index";
const name = "__Host-pax_route";
const encoder = new TextEncoder();
export async function routeCookie(
  assignment: Assignment,
  env: Env,
): Promise<string> {
  if (!env.ROUTING_KEY_ID || encoder.encode(env.DIRECTORY_SECRET).length < 32)
    throw new Error("Routing key unavailable");
  const token = await new SignJWT({
    v: 1,
    user_id: assignment.user_id,
    region: assignment.region,
  })
    .setProtectedHeader({ alg: "HS256", kid: env.ROUTING_KEY_ID })
    .setIssuer("pax-region-directory")
    .setAudience("browser-route")
    .setSubject(assignment.identity_key)
    .setIssuedAt()
    .setExpirationTime("1h")
    .sign(encoder.encode(env.DIRECTORY_SECRET));
  return `${name}=${token}; Path=/; HttpOnly; Secure; SameSite=Lax; Max-Age=3600`;
}
export async function readRoute(
  request: Request,
  env: Env,
  identity: string,
): Promise<Assignment | null> {
  const values =
    request.headers
      .get("Cookie")
      ?.split(";")
      .map((c) => c.trim())
      .filter((c) => c.startsWith(name + "=")) ?? [];
  if (values.length !== 1 || values[0].length > 4096) return null;
  try {
    const token = values[0].slice(name.length + 1);
    const { kid } = decodeProtectedHeader(token);
    const secret =
      kid === env.ROUTING_KEY_ID
        ? env.DIRECTORY_SECRET
        : kid === env.PREVIOUS_ROUTING_KEY_ID
          ? env.PREVIOUS_DIRECTORY_SECRET
          : undefined;
    if (!secret || encoder.encode(secret).length < 32) return null;
    const { payload } = await jwtVerify(token, encoder.encode(secret), {
      algorithms: ["HS256"],
      issuer: "pax-region-directory",
      audience: "browser-route",
      requiredClaims: ["exp", "iat", "sub"],
      maxTokenAge: "1h",
    });
    if (
      payload.v !== 1 ||
      payload.sub !== identity ||
      (payload.region !== "us" && payload.region !== "hk") ||
      typeof payload.user_id !== "string" ||
      !/^usr_[A-Za-z0-9_-]{1,124}$/.test(payload.user_id)
    )
      return null;
    return {
      user_id: payload.user_id,
      identity_key: identity,
      region: payload.region,
    };
  } catch {
    return null;
  }
}
