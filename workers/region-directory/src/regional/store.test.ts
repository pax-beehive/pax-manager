import { afterAll, beforeAll, describe, expect, it } from "vitest";
import { Pool } from "pg";
import { DirectoryStore } from "./store";

// A real PostgreSQL transaction is required to exercise uniqueness and locking.
describe.skipIf(!process.env.DIRECTORY_TEST_DATABASE_URL)(
  "durable account directory",
  () => {
    const pool = new Pool({
      connectionString: process.env.DIRECTORY_TEST_DATABASE_URL,
    });
    const schema = `test_directory_${crypto.randomUUID().replaceAll("-", "")}`;
    let store: DirectoryStore;
    beforeAll(async () => {
      await pool.query(`CREATE SCHEMA ${schema}`);
      store = new DirectoryStore(
        new Pool({
          connectionString: process.env.DIRECTORY_TEST_DATABASE_URL,
          options: `-c search_path=${schema}`,
        }),
      );
      await store.migrate();
    });
    afterAll(async () => {
      await store.close();
      await pool.query(`DROP SCHEMA ${schema} CASCADE`);
      await pool.end();
    });
    it("given concurrent regional choices, assigns one immutable user and region", async () => {
      const rows = await Promise.all(
        Array.from({ length: 12 }, (_, i) =>
          store.assign("one@example.com", i % 2 ? "hk" : "us"),
        ),
      );
      expect(new Set(rows.map((row) => JSON.stringify(row))).size).toBe(1);
      expect(await store.lookup("one@example.com")).toEqual(rows[0]);
      expect(await store.lookupUser(rows[0].user_id)).toEqual(rows[0]);
      expect(await store.lookup("missing@example.com")).toBeNull();
      expect(await store.lookupUser("usr_missing")).toBeNull();
    });
    it("given an existing subject, keeps the original account when email changes", async () => {
      expect(await store.bind("user_A", "first@example.com")).toBe(
        "first@example.com",
      );
      expect(await store.bind("user_A", "changed@example.com")).toBe(
        "first@example.com",
      );
      await expect(store.bind("user_B", "first@example.com")).rejects.toThrow();
      expect(await store.identity("user_A")).toBe("first@example.com");
      expect(await store.identity("user_missing")).toBeNull();
    });
    it("given a D1 export, preserves identifiers and refuses a conflicting import atomically", async () => {
      const original = {
        user_id: "usr_legacy",
        identity_key: "legacy@example.com",
        region: "hk" as const,
      };
      const snapshot = {
        routes: [original],
        bindings: [
          {
            workos_user_id: "user_legacy",
            identity_key: original.identity_key,
          },
        ],
      };
      await store.import(snapshot);
      await store.import(snapshot);
      expect(await store.lookup(original.identity_key)).toEqual(original);
      await expect(
        store.import({
          routes: [
            {
              user_id: "usr_rollback",
              identity_key: "rollback@example.com",
              region: "us",
            },
            { ...original, region: "us" },
          ],
          bindings: [],
        }),
      ).rejects.toThrow();
      expect(await store.lookup("rollback@example.com")).toBeNull();
    });
    it("given committed changes, replays a complete ordered stream without advancing on conflict", async () => {
      const replicaSchema = `${schema}_replica`;
      await pool.query(`CREATE SCHEMA ${replicaSchema}`);
      const replica = new DirectoryStore(
        new Pool({
          connectionString: process.env.DIRECTORY_TEST_DATABASE_URL,
          options: `-c search_path=${replicaSchema}`,
        }),
      );
      try {
        await replica.migrate();
        const page = await store.changes("0");
        expect(page.events.length).toBeGreaterThan(0);
        await replica.applyPage(page);
        expect(await replica.cursor()).toBe(page.cursor);
        expect(await replica.identity("user_legacy")).toBe(
          "legacy@example.com",
        );
        expect(await replica.lookup("legacy@example.com")).toEqual(
          await store.lookup("legacy@example.com"),
        );
        await replica.applyPage(page);
        const conflict = {
          ...page,
          cursor: "999",
          events: [
            {
              seq: "999",
              data: {
                routes: [
                  {
                    user_id: "usr_legacy",
                    identity_key: "legacy@example.com",
                    region: "us" as const,
                  },
                ],
                bindings: [],
              },
            },
          ],
        };
        await expect(replica.applyPage(conflict)).rejects.toThrow();
        expect(await replica.cursor()).toBe(page.cursor);
        expect((await store.changes(page.cursor)).events).toEqual([]);
      } finally {
        await replica.close();
        await pool.query(`DROP SCHEMA ${replicaSchema} CASCADE`);
      }
    });
    it("rejects malformed identities and regions before writing", async () => {
      await expect(store.assign("bad", "hk")).rejects.toThrow();
      await expect(
        store.assign("x@example.com", "eu" as "hk"),
      ).rejects.toThrow();
      await expect(store.bind("bad", "x@example.com")).rejects.toThrow();
      await expect(store.changes("-1")).rejects.toThrow();
      await expect(
        store.import({
          routes: [],
          bindings: [
            { workos_user_id: "user_x", identity_key: "local@example.local" },
          ],
        }),
      ).rejects.toThrow();
    });
  },
);
