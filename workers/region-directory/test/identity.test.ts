import { beforeAll, describe, expect, it, vi } from "vitest";
import { generateKeyPair, SignJWT } from "jose";
import { verifyIdentity } from "../src/identity";
const holder = vi.hoisted(() => ({ key: undefined as unknown }));
vi.mock("jose", async (importOriginal) => {
  const actual = await importOriginal<typeof import("jose")>();
  return { ...actual, createRemoteJWKSet: vi.fn(() => async () => holder.key) };
});
let privateKey: CryptoKey;
const issuer = "https://test.cloudflareaccess.com";
const providers = JSON.stringify([{ issuer, audience: "pax" }]);
beforeAll(async () => {
  const pair = await generateKeyPair("RS256");
  privateKey = pair.privateKey;
  holder.key = pair.publicKey;
});
async function token(
  payload: Record<string, unknown> = {},
  iss = issuer,
  aud = "pax",
) {
  return new SignJWT({ email: " Owner@Example.COM ", ...payload })
    .setProtectedHeader({ alg: "RS256" })
    .setIssuer(iss)
    .setAudience(aud)
    .setIssuedAt()
    .setExpirationTime("1m")
    .sign(privateKey);
}
const request = (jwt?: string) =>
  new Request("https://pax.example", {
    headers: jwt ? { "Cf-Access-Jwt-Assertion": jwt } : {},
  });
describe("Access JWT verification", () => {
  it("normalizes only a signed verified identity", async () => {
    expect(await verifyIdentity(request(await token()), providers)).toBe(
      "owner@example.com",
    );
    expect(await verifyIdentity(request(await token()), providers)).toBe(
      "owner@example.com",
    );
  });
  it("rejects absent or oversized tokens, unknown issuers and audience mismatch", async () => {
    for (const jwt of [
      undefined,
      "x".repeat(17000),
      await token({}, "https://other.cloudflareaccess.com"),
      await token({}, issuer, "other"),
    ])
      await expect(verifyIdentity(request(jwt), providers)).rejects.toThrow();
  });
  it("rejects expired and forged JWTs", async () => {
    const expired = await new SignJWT({ email: "owner@example.com" })
      .setProtectedHeader({ alg: "RS256" })
      .setIssuer(issuer)
      .setAudience("pax")
      .setIssuedAt()
      .setExpirationTime("0s")
      .sign(privateKey);
    await expect(verifyIdentity(request(expired), providers)).rejects.toThrow();
    const forged = await new SignJWT({ email: "owner@example.com" })
      .setProtectedHeader({ alg: "HS256" })
      .setIssuer(issuer)
      .setAudience("pax")
      .setIssuedAt()
      .setExpirationTime("1m")
      .sign(new TextEncoder().encode("x".repeat(32)));
    await expect(verifyIdentity(request(forged), providers)).rejects.toThrow();
  });
  it("rejects bad email and unsafe configured issuers", async () => {
    for (const email of [
      42,
      "invalid",
      "a b@example.com",
      "a".repeat(255) + "@x",
    ])
      await expect(
        verifyIdentity(request(await token({ email })), providers),
      ).rejects.toThrow();
    const unsafe = "http://localhost";
    await expect(
      verifyIdentity(
        request(await token({}, unsafe)),
        JSON.stringify([{ issuer: unsafe, audience: "pax" }]),
      ),
    ).rejects.toThrow();
    await expect(
      verifyIdentity(
        request(await token()),
        JSON.stringify([{ issuer, audience: "" }]),
      ),
    ).rejects.toThrow();
  });
});
