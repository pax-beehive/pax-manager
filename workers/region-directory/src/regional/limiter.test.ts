import { expect, it } from "vitest";
import { nativeLimiter } from "./limiter";
it("bounds per-subject requests and memory, then recovers at the next window", () => {
  let time = 0;
  const limit = nativeLimiter(2, 2, () => time);
  expect(limit("a")).toBe(true);
  expect(limit("a")).toBe(true);
  expect(limit("a")).toBe(false);
  expect(limit("b")).toBe(true);
  expect(limit("c")).toBe(false);
  time = 60000;
  expect(limit("a")).toBe(true);
  expect(limit("c")).toBe(true);
});
