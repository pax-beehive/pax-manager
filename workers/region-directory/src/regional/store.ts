import type { Pool, PoolClient } from "pg";
import type { Assignment, Region } from "../directory";

export interface Binding {
  workos_user_id: string;
  identity_key: string;
}
export interface Snapshot {
  routes: Assignment[];
  bindings: Binding[];
}
export interface ChangePage {
  cursor: string;
  events: { seq: string; data: Snapshot }[];
}
const routeSelect =
  "SELECT user_id, identity_key, region FROM pax_account_routes";
const bindingSelect =
  "SELECT workos_user_id, identity_key FROM pax_account_bindings";
const empty = (): Snapshot => ({ routes: [], bindings: [] });
export function validIdentity(identity: string) {
  return (
    typeof identity === "string" &&
    identity.length <= 254 &&
    identity === identity.trim().toLowerCase() &&
    /^[^\s@]+@[^\s@]+$/.test(identity) &&
    identity !== "local@example.local"
  );
}
function validate(snapshot: Snapshot) {
  if (
    !snapshot ||
    !Array.isArray(snapshot.routes) ||
    !Array.isArray(snapshot.bindings)
  )
    throw new Error("Invalid directory snapshot");
  for (const row of snapshot.routes)
    if (
      !/^usr_[A-Za-z0-9_-]{1,124}$/.test(row.user_id) ||
      !validIdentity(row.identity_key) ||
      !["us", "hk"].includes(row.region)
    )
      throw new Error("Invalid route");
  for (const row of snapshot.bindings)
    if (
      !/^user_[A-Za-z0-9]+$/.test(row.workos_user_id) ||
      !validIdentity(row.identity_key)
    )
      throw new Error("Invalid binding");
}
export class DirectoryStore {
  constructor(private readonly pool: Pool) {}
  close() {
    return this.pool.end();
  }
  async migrate() {
    await this.pool.query(`
      CREATE TABLE IF NOT EXISTS pax_account_routes (
        user_id TEXT PRIMARY KEY, identity_key TEXT NOT NULL UNIQUE,
        region TEXT NOT NULL CHECK (region IN ('us', 'hk')));
      CREATE TABLE IF NOT EXISTS pax_account_bindings (
        workos_user_id TEXT PRIMARY KEY, identity_key TEXT NOT NULL UNIQUE);
      CREATE TABLE IF NOT EXISTS pax_directory_changes (
        seq BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY, data JSONB NOT NULL);
      CREATE TABLE IF NOT EXISTS pax_directory_state (
        id INTEGER PRIMARY KEY CHECK (id = 1), cursor BIGINT NOT NULL DEFAULT 0);
      INSERT INTO pax_directory_state(id) VALUES (1) ON CONFLICT DO NOTHING;
    `);
  }
  private async transaction<T>(
    run: (client: PoolClient) => Promise<T>,
  ): Promise<T> {
    const client = await this.pool.connect();
    try {
      await client.query("BEGIN");
      // Serialize commits before allocating sequence numbers. A consumer must
      // never advance past an earlier sequence still in an uncommitted writer.
      await client.query("SELECT pg_advisory_xact_lock(7162934401)");
      const result = await run(client);
      await client.query("COMMIT");
      return result;
    } catch (error) {
      await client.query("ROLLBACK");
      throw error;
    } finally {
      client.release();
    }
  }
  async lookup(identity: string): Promise<Assignment | null> {
    return (
      (
        await this.pool.query(`${routeSelect} WHERE identity_key=$1`, [
          identity,
        ])
      ).rows[0] ?? null
    );
  }
  async lookupUser(userID: string): Promise<Assignment | null> {
    return (
      (await this.pool.query(`${routeSelect} WHERE user_id=$1`, [userID]))
        .rows[0] ?? null
    );
  }
  async identity(subject: string): Promise<string | null> {
    return (
      (
        await this.pool.query(`${bindingSelect} WHERE workos_user_id=$1`, [
          subject,
        ])
      ).rows[0]?.identity_key ?? null
    );
  }
  async records(identity: string): Promise<Snapshot> {
    const route = await this.lookup(identity);
    const bindings = (
      await this.pool.query(`${bindingSelect} WHERE identity_key=$1`, [
        identity,
      ])
    ).rows;
    return { routes: route ? [route] : [], bindings };
  }
  async bind(subject: string, identity: string): Promise<string> {
    validate({
      routes: [],
      bindings: [{ workos_user_id: subject, identity_key: identity }],
    });
    return this.transaction(async (client) => {
      const existing = (
        await client.query(`${bindingSelect} WHERE workos_user_id=$1`, [
          subject,
        ])
      ).rows[0];
      if (existing) return existing.identity_key;
      const record = {
        routes: [],
        bindings: [{ workos_user_id: subject, identity_key: identity }],
      };
      await this.insert(client, record);
      await client.query(
        "INSERT INTO pax_directory_changes(data) VALUES ($1)",
        [record],
      );
      return identity;
    });
  }
  async assign(identity: string, region: Region): Promise<Assignment> {
    const proposed = {
      user_id: `usr_${crypto.randomUUID().replaceAll("-", "")}`,
      identity_key: identity,
      region,
    };
    validate({ routes: [proposed], bindings: [] });
    return this.transaction(async (client) => {
      const existing = (
        await client.query(`${routeSelect} WHERE identity_key=$1`, [identity])
      ).rows[0];
      if (existing) return existing;
      const record = { routes: [proposed], bindings: [] };
      await this.insert(client, record);
      await client.query(
        "INSERT INTO pax_directory_changes(data) VALUES ($1)",
        [record],
      );
      return proposed;
    });
  }
  private async insert(client: PoolClient, snapshot: Snapshot) {
    for (const row of snapshot.routes) {
      await client.query(
        "INSERT INTO pax_account_routes(user_id,identity_key,region) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING",
        [row.user_id, row.identity_key, row.region],
      );
      const saved = (
        await client.query(`${routeSelect} WHERE identity_key=$1`, [
          row.identity_key,
        ])
      ).rows[0];
      if (
        !saved ||
        saved.user_id !== row.user_id ||
        saved.region !== row.region
      )
        throw new Error("Immutable account route conflict");
    }
    for (const row of snapshot.bindings) {
      await client.query(
        "INSERT INTO pax_account_bindings(workos_user_id,identity_key) VALUES ($1,$2) ON CONFLICT DO NOTHING",
        [row.workos_user_id, row.identity_key],
      );
      const saved = (
        await client.query(`${bindingSelect} WHERE workos_user_id=$1`, [
          row.workos_user_id,
        ])
      ).rows[0];
      if (!saved || saved.identity_key !== row.identity_key)
        throw new Error("Immutable identity binding conflict");
    }
  }
  async import(snapshot: Snapshot) {
    validate(snapshot);
    await this.transaction(async (client) => {
      await this.insert(client, snapshot);
      // Imported historical rows must also reach replicas through the journal.
      for (const row of snapshot.routes)
        await client.query(
          "INSERT INTO pax_directory_changes(data) VALUES ($1)",
          [{ ...empty(), routes: [row] }],
        );
      for (const row of snapshot.bindings)
        await client.query(
          "INSERT INTO pax_directory_changes(data) VALUES ($1)",
          [{ ...empty(), bindings: [row] }],
        );
    });
  }
  async apply(snapshot: Snapshot) {
    validate(snapshot);
    await this.transaction((client) => this.insert(client, snapshot));
  }
  async cursor(): Promise<string> {
    return (
      await this.pool.query("SELECT cursor FROM pax_directory_state WHERE id=1")
    ).rows[0].cursor;
  }
  async changes(after: string): Promise<ChangePage> {
    if (!/^(0|[1-9][0-9]{0,18})$/.test(after))
      throw new Error("Invalid directory cursor");
    const events = (
      await this.pool.query(
        "SELECT seq,data FROM pax_directory_changes WHERE seq>$1 ORDER BY seq LIMIT 100",
        [after],
      )
    ).rows;
    return { cursor: events.at(-1)?.seq ?? after, events };
  }
  async applyPage(page: ChangePage) {
    if (
      !/^(0|[1-9][0-9]{0,18})$/.test(page.cursor) ||
      !Array.isArray(page.events)
    )
      throw new Error("Invalid change page");
    let previous = -1n;
    for (const event of page.events) {
      if (
        !/^[1-9][0-9]{0,18}$/.test(event.seq) ||
        BigInt(event.seq) <= previous ||
        BigInt(event.seq) > BigInt(page.cursor)
      )
        throw new Error("Invalid change sequence");
      validate(event.data);
      previous = BigInt(event.seq);
    }
    if (page.events.length && page.events.at(-1)!.seq !== page.cursor)
      throw new Error("Incomplete change page");
    await this.transaction(async (client) => {
      const cursor = (
        await client.query(
          "SELECT cursor FROM pax_directory_state WHERE id=1 FOR UPDATE",
        )
      ).rows[0].cursor;
      if (BigInt(page.cursor) <= BigInt(cursor)) return;
      for (const event of page.events) await this.insert(client, event.data);
      await client.query(
        "UPDATE pax_directory_state SET cursor=$1 WHERE id=1",
        [page.cursor],
      );
    });
  }
}
