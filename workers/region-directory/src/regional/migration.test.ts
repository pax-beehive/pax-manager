import { expect, it } from "vitest";
import { d1Snapshot } from "./migration";
it("converts successful D1 exports without changing existing account identities", () => {
  const route = {
    user_id: "usr_old",
    identity_key: "old@example.com",
    region: "hk",
  };
  const binding = {
    workos_user_id: "user_old",
    identity_key: route.identity_key,
  };
  expect(
    d1Snapshot(
      [{ success: true, results: [{ ...route, created_at: "old" }] }],
      [{ success: true, results: [binding] }],
    ),
  ).toEqual({ routes: [route], bindings: [binding] });
  for (const invalid of [
    null,
    {},
    [],
    [{ success: false, results: [] }],
    [{ success: true }],
    [{ success: true, results: {} }],
    [
      { success: true, results: [] },
      { success: true, results: [] },
    ],
  ])
    expect(() => d1Snapshot(invalid, [])).toThrow();
});
