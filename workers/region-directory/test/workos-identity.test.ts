import { afterAll, beforeAll, expect, it } from "vitest";
import { Miniflare } from "miniflare";
import { readFile } from "node:fs/promises";
import { bindWorkosIdentity, readWorkosIdentity } from "../src/workos-identity";
import {
  addOriginAssertion,
  attachWorkosIdentity,
} from "../src/origin-assertion";
import type { Env } from "../src/index";
let mf: Miniflare;
let db: D1Database;
beforeAll(async () => {
  mf = new Miniflare({
    modules: true,
    script: "export default {fetch(){return new Response('ok')}}",
    d1Databases: ["DB"],
  });
  db = (await mf.getD1Database("DB")) as unknown as D1Database;
  for (const file of ["0001_directory.sql", "0002_workos_identities.sql"])
    await db.exec(
      (await readFile("migrations/" + file, "utf8"))
        .split("\n")
        .filter((x) => !x.startsWith("--"))
        .join(" "),
    );
  await db
    .prepare(
      "INSERT INTO user_regions(user_id,identity_key,region) VALUES ('usr_original','owner@example.com','hk')",
    )
    .run();
});
afterAll(async () => {
  await mf?.dispose();
});
it("binds a verified legacy email once and preserves its original account after email changes", async () => {
  const user = {
    id: "user_first",
    email: " Owner@Example.COM ",
    emailVerified: true,
  };
  expect(await bindWorkosIdentity(db, user)).toBe("owner@example.com");
  expect(
    await bindWorkosIdentity(db, { ...user, email: "changed@example.com" }),
  ).toBe("owner@example.com");
  expect(await readWorkosIdentity(db, user.id)).toBe("owner@example.com");
  expect(
    await db
      .prepare(
        "SELECT user_id, region FROM user_regions WHERE identity_key='owner@example.com'",
      )
      .first(),
  ).toEqual({ user_id: "usr_original", region: "hk" });
  await expect(
    db
      .prepare(
        "UPDATE workos_identities SET identity_key='takeover@example.com'",
      )
      .run(),
  ).rejects.toThrow();
});
it("rejects unverified, invalid, local and conflicting identities", async () => {
  for (const user of [
    { id: "user_bad", email: "owner@example.com", emailVerified: false },
    { id: "bad", email: "x@example.com", emailVerified: true },
    { id: "user_bad", email: "broken", emailVerified: true },
    { id: "user_bad", email: "local@example.local", emailVerified: true },
    { id: "user_second", email: "owner@example.com", emailVerified: true },
  ])
    await expect(bindWorkosIdentity(db, user)).rejects.toThrow();
  await expect(readWorkosIdentity(db, "user_missing")).rejects.toThrow();
});
it("does not create a region during new signup and handles concurrent callbacks idempotently", async () => {
  const user = {
    id: "user_new",
    email: "new@example.com",
    emailVerified: true,
  };
  expect(
    await Promise.all([
      bindWorkosIdentity(db, user),
      bindWorkosIdentity(db, user),
    ]),
  ).toEqual([user.email, user.email]);
  expect(
    await db
      .prepare("SELECT user_id FROM user_regions WHERE identity_key=?")
      .bind(user.email)
      .first(),
  ).toBeNull();
});
it("signs canonical identity and request target with the destination region secret", async () => {
  const env = {
    DB: db,
    HK_PROVISIONING_SECRET: "h".repeat(40),
    US_PROVISIONING_SECRET: "u".repeat(40),
    HK_ACCESS_CLIENT_ID: "service_id",
    HK_ACCESS_CLIENT_SECRET: "service_secret",
  } as Env;
  const request = new Request(
    "https://pax.example/api/pax/api/v1/user/self/agents",
  );
  attachWorkosIdentity(request, {
    identity: "owner@example.com",
    subject: "user_first",
  });
  const headers = await addOriginAssertion(
    new Headers({
      Cookie: "untrusted",
      "Cf-Access-Jwt-Assertion": "untrusted",
    }),
    request,
    env,
    "hk",
    "/api/v1/user/self/agents",
  );
  expect(headers.get("Cookie")).toBeNull();
  expect(headers.get("Cf-Access-Jwt-Assertion")).toBeNull();
  expect(headers.get("CF-Access-Client-Id")).toBe("service_id");
  const [payload, signature] = headers
    .get("X-Pax-Identity-Assertion")!
    .split(".");
  const body = Buffer.from(payload, "base64url");
  expect(JSON.parse(body.toString())).toMatchObject({
    user_id: "usr_original",
    home_region: "hk",
    region: "hk",
    subject: "user_first",
    method: "GET",
    path: "/api/v1/user/self/agents",
  });
  const key = await crypto.subtle.importKey(
    "raw",
    new TextEncoder().encode(env.HK_PROVISIONING_SECRET),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["verify"],
  );
  expect(
    await crypto.subtle.verify(
      "HMAC",
      key,
      Buffer.from(signature, "base64url"),
      new TextEncoder().encode("pax-browser-identity-v1\n" + body.toString()),
    ),
  ).toBe(true);
  await expect(
    addOriginAssertion(new Headers(), request, env, "us", "/health"),
  ).rejects.toThrow("Origin service credentials");
  attachWorkosIdentity(request, {
    identity: "new@example.com",
    subject: "user_new",
  });
  const probe = await addOriginAssertion(
    new Headers(),
    request,
    env,
    "hk",
    "/health",
  );
  expect(probe.get("CF-Access-Client-Id")).toBe("service_id");
  expect(probe.get("X-Pax-Identity-Assertion")).toBeNull();
  await expect(
    addOriginAssertion(
      new Headers(),
      request,
      env,
      "hk",
      "/api/v1/user/self/me",
    ),
  ).rejects.toThrow("Account region");
});
