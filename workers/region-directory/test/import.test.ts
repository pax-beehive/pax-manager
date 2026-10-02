import { expect, it } from "vitest";
import { buildImport } from "../src/import";
const user = { user_id: "usr_existing", email: " OWNER@Example.com " };
it("preserves imported IDs and normalizes identities with an idempotent statement", () => {
  const result = buildImport([user, user], []);
  expect(result.count).toBe(1);
  expect(result.sql).toContain("('usr_existing','owner@example.com','us')");
  expect(result.sql).toContain("ON CONFLICT");
  expect(buildImport([], []).count).toBe(0);
  expect(
    buildImport([{ user_id: "usr_quote", email: "o'brien@example.com" }], [])
      .sql,
  ).toContain("o''brien@example.com");
});
it("refuses inconsistent single-region inventories and reused IDs", () => {
  expect(() =>
    buildImport([user, { ...user, user_id: "usr_different" }], []),
  ).toThrow("Identity conflict");
  expect(() =>
    buildImport([user, { ...user, email: "other@example.com" }], []),
  ).toThrow("User ID conflict");
  for (const input of [
    null,
    {},
    [null],
    [{}],
    [{ user_id: "bad", email: "owner@example.com" }],
    [{ user_id: "usr_good", email: "noemail" }],
  ])
    expect(() => buildImport(input as never, [])).toThrow();
  expect(() =>
    buildImport(
      Array.from({ length: 3000 }, (_, i) => ({
        user_id: "usr_" + i,
        email: i + "@example.com",
      })),
      [],
    ),
  ).toThrow("atomic");
});
it("writes private conflict reports and never applies SQL or overwrites outputs", async () => {
  const { mkdtemp, writeFile, readFile, stat, rm } =
    await import("node:fs/promises");
  const { tmpdir } = await import("node:os");
  const { join } = await import("node:path");
  const { execFile } = await import("node:child_process");
  const { promisify } = await import("node:util");
  const run = promisify(execFile);
  const dir = await mkdtemp(join(tmpdir(), "pax-import-test-"));
  try {
    const us = join(dir, "us.json"),
      hk = join(dir, "hk.json"),
      output = join(dir, "out.sql");
    await writeFile(us, JSON.stringify([user]));
    await writeFile(
      us,
      JSON.stringify([user, { ...user, user_id: "usr_inconsistent" }]),
    );
    await writeFile(hk, "[]");
    await expect(
      run(process.execPath, ["scripts/import.mjs", us, hk, output]),
    ).rejects.toThrow("conflict");
    const report = JSON.parse(
      await readFile(output + ".conflicts.json", "utf8"),
    );
    expect(report[0]).toMatchObject({
      kind: "identity",
      existing: { user_id: user.user_id, region: "us" },
      incoming: { region: "us" },
    });
    expect((await stat(output + ".conflicts.json")).mode & 0o777).toBe(0o600);
    await expect(stat(output)).rejects.toThrow();
    await writeFile(us, JSON.stringify([user]));
    await writeFile(hk, JSON.stringify([{ ...user, user_id: "usr_hk" }]));
    await run(process.execPath, ["scripts/import.mjs", us, hk, output]);
    const decisions = JSON.parse(
      await readFile(output + ".decisions.json", "utf8"),
    );
    expect(decisions.precedence).toBe("existing_d1_then_us_then_hk");
    expect(decisions.resolutions[0].preferred.user_id).toBe(user.user_id);
    expect((await stat(output + ".decisions.json")).mode & 0o777).toBe(0o600);
    expect((await stat(output)).mode & 0o777).toBe(0o600);
    const sql = await readFile(output, "utf8");
    expect(sql).toContain(user.user_id);
    await expect(
      run(process.execPath, ["scripts/import.mjs", us, hk, output]),
    ).rejects.toThrow("EEXIST");
    expect(await readFile(output, "utf8")).toBe(sql);
  } finally {
    await rm(dir, { recursive: true, force: true });
  }
});

it("keeps the US account for duplicate identities and HK accounts for HK-only identities", () => {
  const result = buildImport(
    [user],
    [
      { ...user, user_id: "usr_hk_duplicate" },
      { user_id: "usr_hk_only", email: "hk@example.com" },
    ],
  );
  expect(result.count).toBe(2);
  expect(result.sql).toContain("('usr_existing','owner@example.com','us')");
  expect(result.sql).toContain("('usr_hk_only','hk@example.com','hk')");
  expect(result.sql).not.toContain("usr_hk_duplicate");
  expect(result.resolutions).toEqual([
    {
      reason: "existing_us_account",
      preferred: {
        user_id: "usr_existing",
        identity_key: "owner@example.com",
        region: "us",
      },
      other: {
        user_id: "usr_hk_duplicate",
        identity_key: "owner@example.com",
        region: "hk",
      },
    },
  ]);
  expect(buildImport([user], [user]).resolutions).toHaveLength(1);
});
it("still detects inconsistent HK records when a US account takes precedence", () => {
  expect(() =>
    buildImport(
      [user],
      [
        { ...user, user_id: "usr_hk_one" },
        { ...user, user_id: "usr_hk_two" },
      ],
    ),
  ).toThrow("Identity conflict");
  expect(() =>
    buildImport(
      [user],
      [
        { ...user, user_id: "usr_hk_one" },
        { user_id: "usr_hk_one", email: "other@example.com" },
      ],
    ),
  ).toThrow("User ID conflict");
});
