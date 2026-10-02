import { readFile, writeFile } from "node:fs/promises";
import { buildImport, ImportConflictError } from "../src/import.ts";
const [usPath, hkPath, outputPath, ...extra] = process.argv.slice(2);
if (!usPath || !hkPath || !outputPath || extra.length) {
  console.error(
    "Usage: node scripts/import.mjs US_EXPORT.json HK_EXPORT.json OUTPUT.sql",
  );
  process.exitCode = 1;
} else {
  try {
    const result = buildImport(
      JSON.parse(await readFile(usPath, "utf8")),
      JSON.parse(await readFile(hkPath, "utf8")),
    );
    await writeFile(
      outputPath + ".decisions.json",
      JSON.stringify(
        {
          precedence: "existing_d1_then_us_then_hk",
          resolutions: result.resolutions,
        },
        null,
        2,
      ) + "\n",
      { mode: 0o600, flag: "wx" },
    );
    await writeFile(outputPath, result.sql, { mode: 0o600, flag: "wx" });
    console.log(
      `Prepared ${result.count} directory candidates; ${result.resolutions.length} duplicate regional identities resolved. Existing D1 assignments take precedence. SQL and private decision report written; no database was modified.`,
    );
  } catch (error) {
    if (error instanceof ImportConflictError) {
      await writeFile(
        outputPath + ".conflicts.json",
        JSON.stringify(error.conflicts, null, 2) + "\n",
        { mode: 0o600, flag: "wx" },
      );
      console.error(
        "Conflict report written to " +
          outputPath +
          ".conflicts.json; no SQL was generated.",
      );
    }
    console.error(error.message);
    process.exitCode = 1;
  }
}
