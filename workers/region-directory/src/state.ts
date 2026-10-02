import { jwtVerify, SignJWT } from "jose";
import type { Assignment } from "./directory";
export interface State {
  identity: string;
  assignment: Assignment;
  bookmark: string | null;
  cached_until: number;
}
const name = "__Host-pax_region";
const key = (secret: string) => new TextEncoder().encode(secret);
export async function readState(
  request: Request,
  secret: string,
  identity: string,
): Promise<State | null> {
  const value = request.headers
    .get("Cookie")
    ?.split(";")
    .map((c) => c.trim())
    .find((c) => c.startsWith(name + "="))
    ?.slice(name.length + 1);
  if (!value || value.length > 8192) return null;
  try {
    const { payload } = await jwtVerify(value, key(secret), {
      algorithms: ["HS256"],
      issuer: "pax-region-directory",
      audience: "bootstrap-state",
      requiredClaims: ["exp"],
    });
    if (payload.sub !== identity) return null;
    return payload.state as unknown as State;
  } catch {
    return null;
  }
}
export async function stateCookie(
  secret: string,
  state: State,
): Promise<string> {
  const token = await new SignJWT({ state })
    .setProtectedHeader({ alg: "HS256" })
    .setIssuer("pax-region-directory")
    .setAudience("bootstrap-state")
    .setSubject(state.identity)
    .setIssuedAt()
    .setExpirationTime("1d")
    .sign(key(secret));
  return `${name}=${token}; Path=/; HttpOnly; Secure; SameSite=Lax; Max-Age=86400`;
}
