export type Region = "us" | "hk";
export interface Assignment {
  user_id: string;
  identity_key: string;
  region: Region;
}
export interface Result {
  assignment: Assignment | null;
  bookmark: string | null;
}
const select =
  "SELECT user_id, identity_key, region FROM user_regions WHERE identity_key = ?";

export async function lookup(
  db: D1Database,
  identity: string,
  bookmark?: string | null,
): Promise<Result> {
  const session = db.withSession(bookmark || "first-unconstrained");
  const assignment = await session
    .prepare(select)
    .bind(identity)
    .first<Assignment>();
  if (assignment) return { assignment, bookmark: session.getBookmark() };
  // A replica miss is never permission to create an account.
  const primary = db.withSession("first-primary");
  return {
    assignment: await primary
      .prepare(select)
      .bind(identity)
      .first<Assignment>(),
    bookmark: primary.getBookmark(),
  };
}

export async function assign(
  db: D1Database,
  identity: string,
  region: Region,
): Promise<Result & { assignment: Assignment }> {
  const session = db.withSession("first-primary");
  const rows = await session.batch([
    session
      .prepare(
        "INSERT INTO user_regions (user_id, identity_key, region) VALUES (?, ?, ?) ON CONFLICT(identity_key) DO NOTHING",
      )
      .bind("usr_" + crypto.randomUUID().replaceAll("-", ""), identity, region),
    session.prepare(select).bind(identity),
  ]);
  const assignment = rows[1].results[0] as unknown as Assignment | undefined;
  if (!assignment) throw new Error("Directory assignment was not returned.");
  return { assignment, bookmark: session.getBookmark() };
}
