import type { Assignment, Region } from "./directory";
interface ExportedUser {
  user_id: string;
  email: string;
}
interface ImportConflict {
  kind: "identity" | "user_id";
  existing: Assignment;
  incoming: Assignment;
}
export class ImportConflictError extends Error {
  conflicts: ImportConflict[];
  constructor(conflicts: ImportConflict[]) {
    super(
      `${conflicts.length} inventory conflict(s): Identity conflict or User ID conflict requires reconciliation`,
    );
    this.conflicts = conflicts;
  }
}
interface ImportResolution {
  reason: "existing_us_account";
  preferred: Assignment;
  other: Assignment;
}
export function buildImport(
  us: ExportedUser[],
  hk: ExportedUser[],
): { count: number; sql: string; resolutions: ImportResolution[] } {
  const byIdentity = new Map<string, Assignment>();
  const byID = new Map<string, Assignment>();
  const conflicts: ImportConflict[] = [];
  const resolutions: ImportResolution[] = [];
  const byRegionalIdentity = new Map<string, Assignment>();
  for (const [region, users] of [
    ["us", us],
    ["hk", hk],
  ] as [Region, ExportedUser[]][]) {
    if (!Array.isArray(users))
      throw new Error("Expected a JSON array of users");
    for (const user of users) {
      if (
        !user ||
        typeof user.email !== "string" ||
        typeof user.user_id !== "string"
      )
        throw new Error("Invalid exported user");
      const identity = user.email.trim().toLowerCase();
      if (
        !/^[^\s@]+@[^\s@]+$/.test(identity) ||
        identity.length > 254 ||
        !/^usr_[A-Za-z0-9_-]{1,124}$/.test(user.user_id)
      )
        throw new Error("Invalid exported identity");
      const incoming: Assignment = {
        user_id: user.user_id,
        identity_key: identity,
        region,
      };
      const regionalKey = region + ":" + identity;
      const regionalExisting = byRegionalIdentity.get(regionalKey);
      if (regionalExisting) {
        if (regionalExisting.user_id !== incoming.user_id) {
          conflicts.push({
            kind: "identity",
            existing: regionalExisting,
            incoming,
          });
        }
        continue;
      }
      const sameID = byID.get(user.user_id);
      if (sameID && sameID.identity_key !== identity) {
        conflicts.push({ kind: "user_id", existing: sameID, incoming });
        continue;
      }
      byRegionalIdentity.set(regionalKey, incoming);
      byID.set(user.user_id, incoming);
      const existing = byIdentity.get(identity);
      if (existing) {
        // US inventory is processed first. Never rename either regional account.
        resolutions.push({
          reason: "existing_us_account",
          preferred: existing,
          other: incoming,
        });
        continue;
      }
      byIdentity.set(identity, incoming);
    }
  }
  if (conflicts.length) throw new ImportConflictError(conflicts);
  const quote = (s: string) => "'" + s.replaceAll("'", "''") + "'";
  const values = [...byIdentity.values()].map(
    (a) =>
      "(" + [a.user_id, a.identity_key, a.region].map(quote).join(",") + ")",
  );
  // Existing D1 assignments remain authoritative even over the US inventory.
  const sql = values.length
    ? "INSERT INTO user_regions (user_id, identity_key, region) VALUES\n" +
      values.join(",\n") +
      "\nON CONFLICT(identity_key) DO NOTHING;\n"
    : "-- No users to import.\n";
  if (new TextEncoder().encode(sql).length > 90000)
    throw new Error(
      "Inventory exceeds one atomic import statement; use a reviewed staged import",
    );
  return { count: values.length, sql, resolutions };
}
