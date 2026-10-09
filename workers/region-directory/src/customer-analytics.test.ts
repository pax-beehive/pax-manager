import { afterEach, describe, expect, it, vi } from "vitest";
import { customerAnalytics } from "./customer-analytics";
import type { Env } from "./index";
import { createWorker } from "./index";

const env = {
  US_MANAGER_URL: "https://us.example.invalid",
  HK_MANAGER_URL: "https://hk.example.invalid",
} as Env;
const request = () =>
  new Request(
    "https://app.example.invalid/api/pax/api/v1/user/self/customer-analytics",
    {
      headers: {
        "Cf-Access-Jwt-Assertion": "test-identity",
        "X-User-Email": "forged@example.invalid",
        Authorization: "Bearer untrusted",
      },
    },
  );
const good = (region: string) =>
  Response.json({
    data: {
      regions: [
        {
          region,
          available: true,
          updated_at: "2026-10-09T12:00:00Z",
          users: [],
        },
      ],
    },
  });
afterEach(() => vi.unstubAllGlobals());
describe("Given an authenticated analytics request", () => {
  it("routes through the identity and origin guards without D1 lookups or provisioning", async () => {
    const configured = {
      ...env,
      BOOTSTRAP_ENABLED: "true",
      PUBLIC_ORIGIN: "https://app.example.invalid",
      DIRECTORY_SECRET: "d".repeat(40),
      US_PROVISIONING_SECRET: "u".repeat(40),
      HK_PROVISIONING_SECRET: "h".repeat(40),
    } as Env;
    const identity = vi.fn().mockResolvedValue("admin@example.invalid");
    const lookup = vi.fn();
    const provision = vi.fn();
    const worker = createWorker({ identity, lookup, provision });
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(good("us"))
      .mockResolvedValueOnce(good("hk"));
    vi.stubGlobal("fetch", fetch);
    expect((await worker.fetch(request(), configured)).status).toBe(200);
    expect(lookup).not.toHaveBeenCalled();
    expect(provision).not.toHaveBeenCalled();
    identity.mockRejectedValue(new Error("invalid identity"));
    expect((await worker.fetch(request(), configured)).status).toBe(401);
    const cross = request();
    cross.headers.set("Origin", "https://other.example.invalid");
    expect((await worker.fetch(cross, configured)).status).toBe(403);
    expect(fetch).toHaveBeenCalledTimes(2);
  });
  it("fans out once per fixed region with only the original identity", async () => {
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(good("us"))
      .mockResolvedValueOnce(good("hk"));
    vi.stubGlobal("fetch", fetch);
    const response = await customerAnalytics(request(), env);
    expect(response.status).toBe(200);
    expect(((await response.json()) as any).data.regions).toHaveLength(2);
    expect(fetch).toHaveBeenCalledTimes(2);
    for (const [url, options] of fetch.mock.calls) {
      expect([env.US_MANAGER_URL, env.HK_MANAGER_URL]).toContain(url.origin);
      expect(options.headers.get("Cf-Access-Jwt-Assertion")).toBe(
        "test-identity",
      );
      expect(options.headers.has("X-User-Email")).toBe(false);
      expect(options.headers.has("Authorization")).toBe(false);
      expect(options.redirect).toBe("manual");
    }
    expect(response.headers.get("Cache-Control")).toContain("no-store");
  });
  it.each([401, 403])(
    "does not disclose the other region when one denies %s",
    async (status) => {
      vi.stubGlobal(
        "fetch",
        vi
          .fn()
          .mockResolvedValueOnce(good("us"))
          .mockResolvedValueOnce(new Response("", { status })),
      );
      const response = await customerAnalytics(request(), env);
      expect(response.status).toBe(403);
      expect(await response.text()).not.toContain('"users"');
    },
  );
  it.each(["network", "http", "shape", "time", "wrong-region"])(
    "marks %s failures unavailable, never as empty users",
    async (failure) => {
      const fetch = vi.fn().mockResolvedValueOnce(good("us"));
      if (failure === "network")
        fetch.mockRejectedValueOnce(new Error("offline"));
      else
        fetch.mockResolvedValueOnce(
          failure === "http"
            ? new Response("", { status: 503 })
            : failure === "shape"
              ? Response.json({ data: {} })
              : failure === "wrong-region"
                ? good("us")
                : Response.json({
                    data: {
                      regions: [
                        {
                          region: "hk",
                          available: true,
                          users: [],
                          updated_at: "invalid",
                        },
                      ],
                    },
                  }),
        );
      vi.stubGlobal("fetch", fetch);
      const response = await customerAnalytics(request(), env);
      const body = (await response.json()) as any;
      expect(body.data.regions[0].available).toBe(true);
      expect(body.data.regions[1]).toEqual({
        region: "hk",
        available: false,
        error: "unavailable",
      });
    },
  );
  it("rejects writes without sending upstream requests", async () => {
    const fetch = vi.fn();
    vi.stubGlobal("fetch", fetch);
    expect(
      (await customerAnalytics(new Request(request(), { method: "POST" }), env))
        .status,
    ).toBe(405);
    expect(fetch).not.toHaveBeenCalled();
  });
});
