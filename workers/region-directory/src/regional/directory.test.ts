import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import { Pool } from "pg";
import { DirectoryStore } from "./store";
import {
  lookup as sharedLookup,
  lookupUser as sharedLookupUser,
  assign as sharedAssign,
} from "../directory";
import { bindWorkosIdentity, readWorkosIdentity } from "../workos-identity";
import {
  ReplicatedDirectory,
  directoryHeaders,
  verifyDirectoryRequest,
  type Command,
} from "./directory";

describe("directory peer authentication", () => {
  const secret = "a".repeat(40),
    body = '{"op":"changes","after":"0"}';
  it("binds the body, method, endpoint and freshness", () => {
    const make = (
      content = body,
      method = "POST",
      path = "/internal/account-directory",
      time = Date.now(),
    ) =>
      new Request(`https://us.example${path}`, {
        method,
        headers: directoryHeaders(secret, body, time),
        body: method === "POST" ? content : undefined,
      });
    expect(verifyDirectoryRequest(make(), body, secret)).toBe(true);
    expect(verifyDirectoryRequest(make(), body + " ", secret)).toBe(false);
    expect(verifyDirectoryRequest(make(body, "GET"), body, secret)).toBe(false);
    expect(
      verifyDirectoryRequest(make(body, "POST", "/other"), body, secret),
    ).toBe(false);
    expect(
      verifyDirectoryRequest(
        make(body, "POST", undefined, Date.now() - 60000),
        body,
        secret,
      ),
    ).toBe(false);
    expect(
      verifyDirectoryRequest(
        make(body, "POST", undefined, Date.now() + 60000),
        body,
        secret,
      ),
    ).toBe(false);
    expect(verifyDirectoryRequest(make(), body, "short")).toBe(false);
    expect(
      verifyDirectoryRequest(
        new Request("https://us.example/internal/account-directory"),
        body,
        secret,
      ),
    ).toBe(false);
  });
});

describe.skipIf(!process.env.DIRECTORY_TEST_DATABASE_URL)(
  "two regional PostgreSQL directories",
  () => {
    const admin = new Pool({
      connectionString: process.env.DIRECTORY_TEST_DATABASE_URL,
    });
    const prefix = `test_pair_${crypto.randomUUID().replaceAll("-", "")}`;
    const stores: DirectoryStore[] = [];
    let authority: ReplicatedDirectory, replica: ReplicatedDirectory;
    const fetcher = vi.fn<typeof fetch>();
    beforeAll(async () => {
      for (const name of ["us", "hk"]) {
        await admin.query(`CREATE SCHEMA ${prefix}_${name}`);
        const store = new DirectoryStore(
          new Pool({
            connectionString: process.env.DIRECTORY_TEST_DATABASE_URL,
            options: `-c search_path=${prefix}_${name}`,
          }),
        );
        await store.migrate();
        stores.push(store);
      }
      authority = new ReplicatedDirectory(
        stores[0],
        true,
        "https://hk.example",
        "s".repeat(40),
        fetcher,
      );
      replica = new ReplicatedDirectory(
        stores[1],
        false,
        "https://us.example",
        "s".repeat(40),
        fetcher,
      );
      fetcher.mockImplementation(async (_url, options) => {
        const reply = await authority.execute(
          JSON.parse(options!.body as string) as Command,
          async () => {},
        );
        return Response.json(reply);
      });
    });
    afterAll(async () => {
      for (const store of stores) await store.close();
      for (const name of ["us", "hk"])
        await admin.query(`DROP SCHEMA ${prefix}_${name} CASCADE`);
      await admin.end();
    });
    it("allocates only on the authority and durably fills a replica miss", async () => {
      const user = {
        id: "user_pair",
        email: "pair@example.com",
        emailVerified: true,
      };
      expect(await bindWorkosIdentity(replica, user)).toBe(user.email);
      const { assignment: row, bookmark } = await sharedAssign(
        replica,
        user.email,
        "hk",
      );
      expect(bookmark).toBeNull();
      expect(await stores[0].lookup(user.email)).toEqual(row);
      expect(await stores[1].lookup(user.email)).toEqual(row);
      fetcher.mockClear();
      expect(await replica.assign(user.email, "us")).toEqual(row);
      expect(
        await replica.bind({ ...user, email: "changed@example.com" }),
      ).toBe(user.email);
      expect(await readWorkosIdentity(replica, user.id)).toBe(user.email);
      expect(await sharedLookupUser(replica, row.user_id)).toEqual(row);
      expect(
        await sharedLookup(replica, user.email, "old-d1-bookmark"),
      ).toEqual({ assignment: row, bookmark: null });
      expect(fetcher).not.toHaveBeenCalled();
    });
    it("keeps existing users working when the authority is down; unknown users fail explicitly", async () => {
      fetcher.mockRejectedValueOnce(new Error("offline"));
      expect((await replica.lookup("pair@example.com"))?.region).toBe("hk");
      await expect(replica.lookup("new@example.com")).rejects.toThrow(
        "offline",
      );
      expect(await stores[1].lookup("new@example.com")).toBeNull();
    });
    it("replicates all historical routes, including users belonging to the other region", async () => {
      const row = await authority.assign("other@example.com", "us");
      await authority.bind({
        id: "user_other",
        email: "other@example.com",
        emailVerified: true,
      });
      await replica.sync();
      fetcher.mockClear();
      expect(await sharedLookupUser(replica, row.user_id)).toEqual(row);
      expect(await replica.readIdentity("user_other")).toBe(
        "other@example.com",
      );
      expect(fetcher).not.toHaveBeenCalled();
      await authority.sync();
      expect(fetcher).not.toHaveBeenCalled();
    });
    it("recovers identity and user-id misses without advancing the replication cursor", async () => {
      await authority.bind({
        id: "user_late",
        email: "late@example.com",
        emailVerified: true,
      });
      const row = await authority.assign("late@example.com", "us");
      const cursor = await stores[1].cursor();
      expect(await replica.readIdentity("user_late")).toBe("late@example.com");
      const second = await authority.assign("later@example.com", "us");
      expect(await replica.lookupUser(second.user_id)).toEqual(second);
      expect(await stores[1].cursor()).toBe(cursor);
      expect(await sharedLookupUser(replica, row.user_id)).toEqual(row);
      await expect(replica.readIdentity("user_unknown")).rejects.toThrow(
        "not linked",
      );
      expect(await replica.lookupUser("usr_unknown")).toBeNull();
      expect(await replica.lookup("unknown@example.com")).toBeNull();
    });
    it("rejects unverified binding, unauthorized replica writes and bad peer responses", async () => {
      await expect(
        replica.bind({
          id: "user_bad",
          email: "bad@example.com",
          emailVerified: false,
        }),
      ).rejects.toThrow();
      await expect(
        replica.bind({ id: "user_bad", email: "bad", emailVerified: true }),
      ).rejects.toThrow();
      await expect(
        replica.execute(
          { op: "assign", identity: "x@example.com", region: "hk" },
          async () => {},
        ),
      ).rejects.toThrow("authority");
      await expect(
        authority.execute({ op: "bad" } as unknown as Command, async () => {}),
      ).rejects.toThrow();
      await expect(
        authority.execute(
          { op: "prepare", identity: "unknown@example.com" },
          async () => {},
        ),
      ).rejects.toThrow();
      const prepare = vi.fn(async () => {});
      await replica.execute(
        { op: "prepare", identity: "pair@example.com" },
        prepare,
      );
      expect(prepare).toHaveBeenCalledWith(
        expect.objectContaining({ region: "hk" }),
      );
      await replica.preparePeer("other@example.com");
      fetcher.mockResolvedValueOnce(new Response(null, { status: 503 }));
      await expect(replica.lookup("new@example.com")).rejects.toThrow(
        "unavailable",
      );
      fetcher.mockResolvedValueOnce(Response.json({}));
      await expect(replica.lookup("new@example.com")).rejects.toThrow(
        "Invalid",
      );
      fetcher.mockResolvedValueOnce(Response.json({}));
      await expect(replica.sync()).rejects.toThrow("Invalid");
    });
  },
);
