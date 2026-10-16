import { createHmac, timingSafeEqual } from "node:crypto";
import type { User } from "@workos-inc/node";
import type { AccountDirectory } from "../account-directory";
import type { Assignment, Region } from "../directory";
import {
  DirectoryStore,
  type Snapshot,
  type ChangePage,
  validIdentity,
} from "./store";

export type Command =
  | { op: "lookup"; identity: string }
  | { op: "lookupUser"; userID: string }
  | { op: "identity"; subject: string }
  | { op: "bind"; subject: string; identity: string }
  | { op: "assign"; identity: string; region: Region }
  | { op: "changes"; after: string }
  | { op: "prepare"; identity: string };
interface Reply {
  snapshot?: Snapshot;
  page?: ChangePage;
}
const rpcPath = "/internal/account-directory";
function proof(secret: string, time: string, body: string) {
  return createHmac("sha256", secret)
    .update(`pax-account-directory-v1\nPOST\n${rpcPath}\n${time}\n${body}`)
    .digest("hex");
}
export function directoryHeaders(
  secret: string,
  body: string,
  now = Date.now(),
) {
  const time = Math.floor(now / 1000).toString();
  return {
    "Content-Type": "application/json",
    "X-Pax-Directory-Time": time,
    "X-Pax-Directory-Proof": proof(secret, time, body),
  };
}
export function verifyDirectoryRequest(
  request: Request,
  body: string,
  secret: string,
  now = Date.now(),
) {
  const time = request.headers.get("X-Pax-Directory-Time") ?? "";
  const signature = request.headers.get("X-Pax-Directory-Proof") ?? "";
  const seconds = Number(time);
  if (
    request.method !== "POST" ||
    new URL(request.url).pathname !== rpcPath ||
    secret.length < 32 ||
    !/^[0-9]{10,12}$/.test(time) ||
    !/^[a-f0-9]{64}$/.test(signature) ||
    seconds < now / 1000 - 30 ||
    seconds > now / 1000 + 5
  )
    return false;
  return timingSafeEqual(
    Buffer.from(signature, "hex"),
    Buffer.from(proof(secret, time, body), "hex"),
  );
}
export class ReplicatedDirectory implements AccountDirectory {
  constructor(
    readonly store: DirectoryStore,
    readonly authority: boolean,
    private readonly peer: string,
    readonly secret: string,
    private readonly fetcher: typeof fetch = fetch,
  ) {}
  private async remote(command: Command): Promise<Reply> {
    const body = JSON.stringify(command);
    const response = await this.fetcher(new URL(rpcPath, this.peer), {
      method: "POST",
      headers: directoryHeaders(this.secret, body),
      body,
      redirect: "error",
      signal: AbortSignal.timeout(8000),
    });
    if (!response.ok) throw new Error("Account directory unavailable");
    return response.json() as Promise<Reply>;
  }
  private async recover(command: Command) {
    const reply = await this.remote(command);
    if (!reply.snapshot) throw new Error("Invalid directory reply");
    await this.store.apply(reply.snapshot);
  }
  async lookup(identity: string): Promise<Assignment | null> {
    const local = await this.store.lookup(identity);
    if (local || this.authority) return local;
    await this.recover({ op: "lookup", identity });
    return this.store.lookup(identity);
  }
  async lookupUser(userID: string): Promise<Assignment | null> {
    const local = await this.store.lookupUser(userID);
    if (local || this.authority) return local;
    await this.recover({ op: "lookupUser", userID });
    return this.store.lookupUser(userID);
  }
  async readIdentity(subject: string): Promise<string> {
    let identity = await this.store.identity(subject);
    if (!identity && !this.authority) {
      await this.recover({ op: "identity", subject });
      identity = await this.store.identity(subject);
    }
    if (!identity) throw new Error("Identity not linked");
    return identity;
  }
  async bind(
    user: Pick<User, "id" | "email" | "emailVerified">,
  ): Promise<string> {
    if (!user.emailVerified || !/^user_[A-Za-z0-9]+$/.test(user.id))
      throw new Error("Unverified identity");
    const local = await this.store.identity(user.id);
    if (local) return local;
    const identity = user.email.trim().toLowerCase();
    if (!validIdentity(identity)) throw new Error("Invalid identity");
    if (this.authority) return this.store.bind(user.id, identity);
    await this.recover({ op: "bind", subject: user.id, identity });
    return this.readIdentity(user.id);
  }
  async assign(identity: string, region: Region): Promise<Assignment> {
    const local = await this.store.lookup(identity);
    if (local) return local;
    if (this.authority) return this.store.assign(identity, region);
    await this.recover({ op: "assign", identity, region });
    const assignment = await this.store.lookup(identity);
    if (!assignment) throw new Error("Missing assigned account");
    return assignment;
  }
  async sync() {
    if (this.authority) return;
    for (;;) {
      const { page } = await this.remote({
        op: "changes",
        after: await this.store.cursor(),
      });
      if (!page) throw new Error("Invalid replication reply");
      await this.store.applyPage(page);
      if (page.events.length < 100) return;
    }
  }
  async preparePeer(identity: string) {
    await this.remote({ op: "prepare", identity });
  }
  async execute(
    command: Command,
    prepare: (assignment: Assignment) => Promise<void>,
  ): Promise<Reply> {
    if (command.op === "prepare") {
      const row = await this.lookup(command.identity);
      if (!row) throw new Error("Unknown account");
      await prepare(row);
      return {};
    }
    if (!this.authority)
      throw new Error("Writes and discovery require the authority");
    if (command.op === "changes")
      return { page: await this.store.changes(command.after) };
    let identity: string | null;
    switch (command.op) {
      case "lookup":
        identity = command.identity;
        break;
      case "lookupUser":
        identity =
          (await this.store.lookupUser(command.userID))?.identity_key ?? null;
        break;
      case "identity":
        identity = await this.store.identity(command.subject);
        break;
      case "bind":
        identity = await this.store.bind(command.subject, command.identity);
        break;
      case "assign":
        identity = (await this.store.assign(command.identity, command.region))
          .identity_key;
        break;
      default:
        throw new Error("Unknown directory operation");
    }
    return {
      snapshot: identity
        ? await this.store.records(identity)
        : { routes: [], bindings: [] },
    };
  }
}
