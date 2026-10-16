import type { Snapshot } from "./store";

// Input is the JSON output of two explicit wrangler d1 execute SELECT queries.
export function d1Snapshot(routes: unknown, bindings: unknown): Snapshot {
  function rows<T>(value: unknown): T[] {
    if (
      !Array.isArray(value) ||
      value.length !== 1 ||
      value[0]?.success !== true ||
      !Array.isArray(value[0].results)
    )
      throw new Error("Expected one successful D1 SELECT result");
    return value[0].results;
  }
  return {
    routes: rows<Snapshot["routes"][number]>(routes).map(
      ({ user_id, identity_key, region }) => ({
        user_id,
        identity_key,
        region,
      }),
    ),
    bindings: rows<Snapshot["bindings"][number]>(bindings).map(
      ({ workos_user_id, identity_key }) => ({
        workos_user_id,
        identity_key,
      }),
    ),
  };
}
