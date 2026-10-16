import type { User } from "@workos-inc/node";
import type { Assignment, Region } from "./directory";

// Regional gateways use a durable local index. D1 remains an explicit legacy
// implementation until existing deployments complete their migration.
export interface AccountDirectory {
  lookup(identity: string): Promise<Assignment | null>;
  lookupUser(
    userID: string,
  ): Promise<Pick<Assignment, "user_id" | "region"> | null>;
  assign(identity: string, region: Region): Promise<Assignment>;
  bind(user: Pick<User, "id" | "email" | "emailVerified">): Promise<string>;
  readIdentity(subject: string): Promise<string>;
}

export type DirectoryDatabase = D1Database | AccountDirectory;

export function isAccountDirectory(
  db: DirectoryDatabase,
): db is AccountDirectory {
  return "lookup" in db;
}
