import { createHmac } from "node:crypto";
import { afterEach, expect, it, vi } from "vitest";
import { provision } from "../src/provision";
import type { Env } from "../src/index";
const assignment = {
  user_id: "usr_global",
  identity_key: "owner@example.com",
  region: "hk" as const,
};
const env = {
  HK_MANAGER_URL: "https://hk.example",
  US_MANAGER_URL: "https://us.example",
  HK_PROVISIONING_SECRET: "h".repeat(40),
  US_PROVISIONING_SECRET: "u".repeat(40),
  HK_ACCESS_CLIENT_ID: "client",
  HK_ACCESS_CLIENT_SECRET: "secret",
} as Env;
afterEach(() => vi.unstubAllGlobals());
it("signs the exact body, short timestamp and purpose and sends only to the chosen manager", async () => {
  const fetch = vi.fn(async () => Response.json({ data: assignment }));
  vi.stubGlobal("fetch", fetch);
  await provision(assignment, env);
  const [url, init] = fetch.mock.calls[0] as unknown as [URL, RequestInit];
  expect(url.href).toBe("https://hk.example/internal/users/ensure");
  const headers = init.headers as Record<string, string>;
  expect(headers["X-Pax-Signature"]).toBe(
    createHmac("sha256", env.HK_PROVISIONING_SECRET)
      .update(
        "pax-region-ensure-v1\n" +
          headers["X-Pax-Timestamp"] +
          "\n" +
          init.body,
      )
      .digest("hex"),
  );
  expect(headers["CF-Access-Client-Id"]).toBe("client");
  expect(init.redirect).toBe("error");
  expect(
    Math.abs(Date.now() / 1000 - Number(headers["X-Pax-Timestamp"])),
  ).toBeLessThan(2);
  fetch.mockResolvedValue(
    Response.json({ data: { ...assignment, region: "us" } }),
  );
  await provision({ ...assignment, region: "us" }, env);
});
it("does not treat errors, login HTML, mismatched identity or redirects as successful provisioning", async () => {
  for (const response of [
    new Response("failure", { status: 503 }),
    new Response("login"),
    Response.json({}),
    Response.json({ data: { ...assignment, user_id: "usr_other" } }),
    Response.json({ data: { ...assignment, region: "us" } }),
    Response.json({
      data: { ...assignment, identity_key: "other@example.com" },
    }),
  ]) {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => response),
    );
    await expect(provision(assignment, env)).rejects.toThrow();
  }
});
