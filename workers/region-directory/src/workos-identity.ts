import type { User } from "@workos-inc/node";

// Email is used exactly once, after WorkOS verifies it. Subsequent logins use
// the immutable subject binding, including when the provider email changes.
export async function bindWorkosIdentity(
  db: D1Database,
  user: Pick<User, "id" | "email" | "emailVerified">,
): Promise<string> {
  if (!user.emailVerified || !/^user_[A-Za-z0-9]+$/.test(user.id))
    throw new Error("Unverified identity");
  const email = user.email.trim().toLowerCase();
  if (
    !/^[^\s@]+@[^\s@]+$/.test(email) ||
    email.length > 254 ||
    email === "local@example.local"
  )
    throw new Error("Invalid identity");
  const session = db.withSession("first-primary");
  const existing = await session
    .prepare(
      "SELECT identity_key FROM workos_identities WHERE workos_user_id = ?",
    )
    .bind(user.id)
    .first<{ identity_key: string }>();
  if (existing) return existing.identity_key;
  // Both unique constraints are deliberate. A second subject cannot silently
  // take over an account, and concurrent first callbacks remain idempotent.
  await session
    .prepare(
      "INSERT INTO workos_identities (workos_user_id, identity_key) VALUES (?, ?) ON CONFLICT DO NOTHING",
    )
    .bind(user.id, email)
    .run();
  const linked = await session
    .prepare(
      "SELECT identity_key FROM workos_identities WHERE workos_user_id = ?",
    )
    .bind(user.id)
    .first<{ identity_key: string }>();
  if (!linked) throw new Error("Identity already linked");
  return linked.identity_key;
}

export async function readWorkosIdentity(
  db: D1Database,
  subject: string,
): Promise<string> {
  const row = await db
    .withSession("first-primary")
    .prepare(
      "SELECT identity_key FROM workos_identities WHERE workos_user_id = ?",
    )
    .bind(subject)
    .first<{ identity_key: string }>();
  if (!row) throw new Error("Identity not linked");
  return row.identity_key;
}
