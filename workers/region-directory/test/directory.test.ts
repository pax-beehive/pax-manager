import { afterAll, beforeAll, beforeEach, describe, expect, it } from "vitest";
import { Miniflare } from "miniflare";
import { readFile } from "node:fs/promises";
import { assign, lookup } from "../src/directory";
let mf: Miniflare;
let db: D1Database;
beforeAll(async () => {
  mf = new Miniflare({
    workers: [
      {
        modules: true,
        script: 'export default {fetch(){return new Response("ok")}}',
        compatibilityDate: "2026-07-01",
        d1Databases: ["DB"],
      },
    ],
  });
  db = (await mf.getD1Database("DB")) as unknown as D1Database;
  await db.exec(
    (await readFile("migrations/0001_directory.sql", "utf8")).replace(
      /\n/g,
      " ",
    ),
  );
});
afterAll(async () => {
  await mf?.dispose();
});
beforeEach(async () => {
  await db.prepare("DELETE FROM user_regions").run();
});
describe("a globally unique user region", () => {
  it("keeps one identity and region when concurrent signups choose different regions", async () => {
    const results = await Promise.all(
      Array.from({ length: 16 }, (_, i) =>
        assign(db, "owner@example.com", i % 2 ? "hk" : "us"),
      ),
    );
    expect(new Set(results.map((r) => r.assignment.user_id)).size).toBe(1);
    expect(new Set(results.map((r) => r.assignment.region)).size).toBe(1);
    expect(
      (
        await db
          .prepare("SELECT COUNT(*) AS count FROM user_regions")
          .first<{ count: number }>()
      )?.count,
    ).toBe(1);
    const found = await lookup(db, "owner@example.com", results[0].bookmark);
    expect(found.assignment).toEqual(results[0].assignment);
  });
  it("returns missing without allocating a region", async () => {
    expect((await lookup(db, "missing@example.com")).assignment).toBeNull();
  });
});

it("never mutates an established identity or region", async () => {
  const result = await assign(db, "immutable@example.com", "us");
  await expect(
    db
      .prepare("UPDATE user_regions SET region = 'hk' WHERE user_id = ?")
      .bind(result.assignment.user_id)
      .run(),
  ).rejects.toThrow("immutable");
});
it("confirms replica misses with primary and uses session bookmarks", async () => {
  const { vi } = await import("vitest");
  const record = {
    user_id: "usr_old",
    identity_key: "old@example.com",
    region: "us",
  };
  const first = vi
    .fn()
    .mockResolvedValueOnce(null)
    .mockResolvedValueOnce(record);
  const session = {
    prepare: vi.fn(() => ({ bind: vi.fn(() => ({ first })) })),
    getBookmark: () => "primary-bookmark",
  };
  const fake = { withSession: vi.fn(() => session) } as unknown as D1Database;
  expect(
    (await lookup(fake, "old@example.com", "previous")).assignment,
  ).toEqual(record);
  expect(fake.withSession).toHaveBeenNthCalledWith(1, "previous");
  expect(fake.withSession).toHaveBeenNthCalledWith(2, "first-primary");
});
it("fails closed if the assignment batch does not return its winner", async () => {
  const fake = {
    withSession: () => ({
      prepare: () => ({ bind: () => ({}) }),
      batch: async () => [{ results: [] }, { results: [] }],
      getBookmark: () => null,
    }),
  } as unknown as D1Database;
  await expect(assign(fake, "old@example.com", "us")).rejects.toThrow(
    "not returned",
  );
});
it("imports duplicate regional identities without changing the authoritative D1 account", async () => {
  const { buildImport } = await import("../src/import");
  const existing = { user_id: "usr_hk_original", email: "owner@example.com" };
  await db.exec(buildImport([], [existing]).sql.replace(/\n/g, " "));
  const next = buildImport(
    [
      { user_id: "usr_us_duplicate", email: existing.email },
      { user_id: "usr_new", email: "new@example.com" },
    ],
    [existing],
  );
  await db.exec(next.sql.replace(/\n/g, " "));
  await db.exec(next.sql.replace(/\n/g, " "));
  expect((await lookup(db, existing.email)).assignment).toMatchObject({
    user_id: existing.user_id,
    region: "hk",
  });
  expect((await lookup(db, "new@example.com")).assignment).toMatchObject({
    user_id: "usr_new",
    region: "us",
  });
});
it("rolls back an import that reuses another identity's global ID", async () => {
  const { buildImport } = await import("../src/import");
  await db.exec(
    buildImport(
      [{ user_id: "usr_original", email: "owner@example.com" }],
      [],
    ).sql.replace(/\n/g, " "),
  );
  const bad = buildImport(
    [
      { user_id: "usr_new", email: "new@example.com" },
      { user_id: "usr_original", email: "other@example.com" },
    ],
    [],
  );
  await expect(db.exec(bad.sql.replace(/\n/g, " "))).rejects.toThrow();
  expect((await lookup(db, "new@example.com")).assignment).toBeNull();
});

it("keeps the D1 winner when regional provisioning fails and another preference retries", async () => {
  const { createWorker } = await import("../src/index");
  const { vi } = await import("vitest");
  const provision = vi
    .fn()
    .mockRejectedValueOnce(new Error("offline"))
    .mockResolvedValue(undefined);
  const worker = createWorker({
    identity: async () => "retry@example.com",
    provision,
  });
  const env = {
    BOOTSTRAP_ENABLED: "true",
    DB: db,
    PUBLIC_ORIGIN: "https://pax.example",
    ACCESS_PROVIDERS: "[]",
    DIRECTORY_SECRET: "s".repeat(32),
    US_PROVISIONING_SECRET: "u".repeat(32),
    HK_PROVISIONING_SECRET: "h".repeat(32),
    US_MANAGER_URL: "https://us.example",
    HK_MANAGER_URL: "https://hk.example",
  };
  const request = (region: string) =>
    new Request(env.PUBLIC_ORIGIN + "/api/v1/region/bootstrap", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ preferred_region: region }),
    });
  expect((await worker.fetch(request("hk"), env)).status).toBe(503);
  const winner = (await lookup(db, "retry@example.com")).assignment!;
  expect(winner.region).toBe("hk");
  const retry = await worker.fetch(request("us"), env);
  expect(await retry.json()).toMatchObject({
    user_id: winner.user_id,
    region: "hk",
    status: "ready",
  });
  expect(provision).toHaveBeenNthCalledWith(
    2,
    winner,
    env,
    expect.any(Request),
  );
});
