import { createRemoteJWKSet, decodeJwt, jwtVerify } from "jose";
const keys = new Map<string, ReturnType<typeof createRemoteJWKSet>>();
export async function verifyIdentity(
  request: Request,
  providersJSON: string,
): Promise<string> {
  const token = request.headers.get("Cf-Access-Jwt-Assertion");
  if (!token || token.length > 16384) throw new Error("Missing identity");
  const providers: { issuer: string; audience: string }[] =
    JSON.parse(providersJSON);
  const issuer = decodeJwt(token).iss;
  const provider = providers.find((p) => p.issuer === issuer);
  if (!provider || !provider.audience) throw new Error("Untrusted issuer");
  const url = new URL(provider.issuer);
  if (
    url.protocol !== "https:" ||
    !url.hostname.endsWith(".cloudflareaccess.com") ||
    url.pathname !== "/" ||
    url.search ||
    url.hash ||
    url.username ||
    url.password ||
    url.port
  )
    throw new Error("Invalid issuer");
  let jwks = keys.get(provider.issuer);
  if (!jwks) {
    jwks = createRemoteJWKSet(new URL("/cdn-cgi/access/certs", url));
    keys.set(provider.issuer, jwks);
  }
  const { payload } = await jwtVerify(token, jwks, {
    issuer: provider.issuer,
    audience: provider.audience,
    algorithms: ["RS256"],
    requiredClaims: ["exp", "iat", "email"],
  });
  if (typeof payload.email !== "string") throw new Error("Missing email");
  const email = payload.email.trim().toLowerCase();
  if (!/^[^\s@]+@[^\s@]+$/.test(email) || email.length > 254)
    throw new Error("Invalid email");
  return email;
}
