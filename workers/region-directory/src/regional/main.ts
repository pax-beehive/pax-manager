import { Pool } from "pg";
import { readFile } from "node:fs/promises";
import { gatewayConfig } from "./config";
import { DirectoryStore, type Snapshot } from "./store";
import { ReplicatedDirectory } from "./directory";
import { createGateway } from "./gateway";
import { gatewayServer } from "./server";
import { d1Snapshot } from "./migration";

async function main() {
  const importing = ["import", "import-d1"].includes(process.argv[2]);
  const config = importing ? undefined : gatewayConfig(process.env);
  if (!process.env.DIRECTORY_DATABASE_URL)
    throw new Error("Missing directory database URL");
  const store = new DirectoryStore(
    new Pool({ connectionString: process.env.DIRECTORY_DATABASE_URL, max: 10 }),
  );
  try {
    await store.migrate();
    if (importing) {
      if (
        !["us", "hk"].includes(process.env.PAX_REGION ?? "") ||
        process.env.PAX_REGION !== process.env.DIRECTORY_AUTHORITY_REGION
      )
        throw new Error("Import only into the authority");
      const source = JSON.parse(await readFile(process.argv[3], "utf8"));
      const snapshot: Snapshot =
        process.argv[2] === "import-d1"
          ? d1Snapshot(
              source,
              JSON.parse(await readFile(process.argv[4], "utf8")),
            )
          : source;
      await store.import(snapshot);
      await store.close();
      console.log(
        `Imported ${snapshot.routes.length} account routes and ${snapshot.bindings.length} identity bindings`,
      );
      return;
    }
    if (!config) throw new Error("Missing gateway configuration");
    const directory = new ReplicatedDirectory(
      store,
      config.authority,
      config.peerURL,
      config.secret,
    );
    try {
      await directory.sync();
    } catch {
      console.error(
        "Initial directory sync unavailable; using durable local records",
      );
    }
    const app = createGateway(config, directory);
    const server = gatewayServer(config, app.handle);
    await new Promise<void>((resolve, reject) => {
      server.once("error", reject);
      server.listen(
        config.port,
        process.env.LISTEN_HOST ?? "127.0.0.1",
        resolve,
      );
    });
    let stopped = false;
    let timer: ReturnType<typeof setTimeout>;
    async function replicate() {
      try {
        await directory.sync();
      } catch {
        console.error(
          "Account directory replication failed; local accounts remain available",
        );
      }
      if (!stopped) timer = setTimeout(replicate, 5000);
    }
    timer = setTimeout(replicate, 5000);
    for (const signal of ["SIGTERM", "SIGINT"])
      process.once(signal, () => {
        stopped = true;
        clearTimeout(timer);
        server.close(() => void store.close());
      });
  } catch (error) {
    await store.close();
    throw error;
  }
}
main().catch(() => {
  console.error(
    "Regional gateway startup failed; check configuration, directory import and peer availability",
  );
  process.exitCode = 1;
});
