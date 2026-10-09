import { afterEach, describe, expect, it, vi } from "vitest";
import { createWorker, type Env } from "../src/index";
const env = {
  PUBLIC_ORIGIN: "https://pax.example",
  RELEASE_PROBE_TOKEN: "r".repeat(40),
  WORKER_VERSION: { id: "version-id", tag: "commit-tag", timestamp: "now" },
  US_MANAGER_URL: "https://us.example",
  HK_MANAGER_URL: "https://hk.example",
  US_ACCESS_CLIENT_ID: "us-id",
  US_ACCESS_CLIENT_SECRET: "us-secret",
  HK_ACCESS_CLIENT_ID: "hk-id",
  HK_ACCESS_CLIENT_SECRET: "hk-secret",
} as unknown as Env;
const request = (region = "us", token = "r".repeat(40), method = "GET") =>
  new Request(`https://pax.example/api/v1/region/release-health/${region}`, {
    method,
    headers: { Authorization: `Bearer ${token}` },
  });
afterEach(() => vi.unstubAllGlobals());
describe("read-only release verification", () => {
  it("requires a dedicated credential before contacting Managers", async () => {
    const fetch = vi.fn();
    vi.stubGlobal("fetch", fetch);
    for (const value of [
      request("us", "wrong"),
      request("us", "r".repeat(40), "POST"),
    ]) {
      expect(
        (await createWorker().fetch(value, env)).status,
      ).toBeGreaterThanOrEqual(400);
    }
    expect(
      (
        await createWorker().fetch(request(), {
          ...env,
          RELEASE_PROBE_TOKEN: undefined,
        })
      ).status,
    ).toBe(503);
    expect(fetch).not.toHaveBeenCalled();
  });
  it.each(["us", "hk"])(
    "verifies %s through the deployed Worker without creating users",
    async (region) => {
      const manager = {
        status: "ok",
        region,
        region_directory_capabilities: ["browser-v1"],
      };
      const fetch = vi.fn().mockResolvedValue(Response.json({ data: manager }));
      vi.stubGlobal("fetch", fetch);
      const response = await createWorker().fetch(request(region), env);
      expect(response.status).toBe(200);
      expect(await response.json()).toEqual({
        version_id: "version-id",
        region,
        manager,
      });
      expect(response.headers.get("cache-control")).toBe("no-store");
      expect(fetch.mock.calls[0][0]).toBe(`https://${region}.example/health`);
      expect(fetch.mock.calls[0][1].redirect).toBe("manual");
      expect(fetch.mock.calls[0][1].headers["CF-Access-Client-Secret"]).toBe(
        `${region}-secret`,
      );
      expect(fetch.mock.calls[0][1].headers.Authorization).toBeUndefined();
    },
  );
  it("fails closed for redirects, wrong regions, malformed health, and network errors", async () => {
    for (const result of [
      new Response("", { status: 302 }),
      Response.json({ data: { status: "ok", region: "hk" } }),
      Response.json({}),
      new Error("private origin body"),
    ]) {
      vi.stubGlobal(
        "fetch",
        vi.fn().mockImplementation(async () => {
          if (result instanceof Error) throw result;
          return result;
        }),
      );
      const response = await createWorker().fetch(request(), env);
      expect(response.status).toBe(502);
      expect(await response.text()).not.toContain("private");
    }
  });
  it("rejects missing version metadata and wrong public origins", async () => {
    expect(
      (
        await createWorker().fetch(request(), {
          ...env,
          WORKER_VERSION: undefined,
        })
      ).status,
    ).toBe(503);
    expect(
      (
        await createWorker().fetch(request(), {
          ...env,
          PUBLIC_ORIGIN: "https://other.example",
        })
      ).status,
    ).toBe(404);
  });
});
