import { afterEach, describe, expect, it, vi } from "vitest";
import { browserPath } from "./browser-proxy";
import { allowedNativeUserRoute, nativeFetch } from "./native";
import { allowedStarterRoute } from "./starter-routes";
import type { Env } from "./index";

const combinations = [
  ["GET", "/api/v1/starter"],
  ["POST", "/api/v1/starter"],
  ["POST", "/api/v1/starter/wake"],
  ["POST", "/api/v1/starter/revoke"],
] as const;
const rejected = [
  ["HEAD", "/api/v1/starter"],
  ["PUT", "/api/v1/starter"],
  ["DELETE", "/api/v1/starter"],
  ["GET", "/api/v1/starter/wake"],
  ["GET", "/api/v1/starter/revoke"],
  ["POST", "/api/v1/starter/revoke/child"],
  ["POST", "/api/v1/starter/"],
  ["POST", "/api/v1/starter/wake/"],
  ["POST", "/api/v1/starter/wake/child"],
  ["POST", "/api/v1/starter/other"],
  ["POST", "/api/v1/starters"],
  ["POST", "/api/v1/%73tarter"],
  ["POST", "/api/v1/starter%2fwake"],
  ["POST", "/api/v1/starter//wake"],
  ["POST", "/api/v1/starter\\wake"],
] as const;
const env = {
  PUBLIC_ORIGIN: "https://app.example.invalid",
  NATIVE_API_ENABLED: "true",
  US_MANAGER_URL: "https://us.example.invalid",
  HK_MANAGER_URL: "https://hk.example.invalid",
  US_ACCESS_CLIENT_ID: "us-service",
  US_ACCESS_CLIENT_SECRET: "us-secret",
  HK_ACCESS_CLIENT_ID: "hk-service",
  HK_ACCESS_CLIENT_SECRET: "hk-secret",
} as Env;

afterEach(() => vi.unstubAllGlobals());

describe("authenticated owner Starter routes", () => {
  it.each(combinations)("permits exactly %s %s", (method, path) => {
    expect(allowedStarterRoute(method, path)).toBe(true);
    expect(browserPath(path, method)).toBe(path);
    expect(browserPath("/api/pax" + path, method)).toBe(path);
    expect(allowedNativeUserRoute(method, path.slice("/api/v1/".length))).toBe(
      true,
    );
  });

  it.each(rejected)("rejects unsupported %s %s", (method, path) => {
    expect(allowedStarterRoute(method, path)).toBe(false);
    expect(browserPath(path, method)).toBeNull();
    expect(browserPath("/api/pax" + path, method)).toBeNull();
    expect(allowedNativeUserRoute(method, path.slice("/api/v1/".length))).toBe(
      false,
    );
  });

  it("retains existing browser callers without opening a methodless Starter route", () => {
    expect(browserPath("/api/v1/user/self/me")).toBe("/api/v1/user/self/me");
    expect(browserPath("/api/pax/api/v1/starter")).toBeNull();
  });

  it.each(["us", "hk"])(
    "forwards only the owner API key and exact Starter target to %s",
    async (region) => {
      const upstream = vi
        .fn()
        .mockImplementation(async () => Response.json({ data: {} }));
      vi.stubGlobal("fetch", upstream);
      for (const [method, path] of combinations) {
        const body =
          path === "/api/v1/starter/revoke"
            ? '{"node_id":"node_fixture","agent_id":"agent_fixture"}'
            : method === "POST"
              ? "{ }\n"
              : undefined;
        const request = new Request(
          `${env.PUBLIC_ORIGIN}/api/v1/native/${region}/${path.slice("/api/v1/".length)}`,
          {
            method,
            headers: {
              Authorization: "Bearer paxu_fixture",
              "Content-Type": "application/json",
              Cookie: "forged=1",
              "X-User-Email": "forged@example.invalid",
              "X-Pax-User-ID": "usr_forged",
              "X-Pax-Identity-Assertion": "forged",
              "CF-Access-Client-Secret": "forged",
            },
            body,
          },
        );
        expect((await nativeFetch(request, env)).status).toBe(200);
        const [url, options] = upstream.mock.calls.at(-1)!;
        expect(String(url)).toBe(`https://${region}.example.invalid${path}`);
        expect(options.method).toBe(method);
        expect(
          options.body === undefined
            ? undefined
            : new TextDecoder().decode(options.body),
        ).toBe(body);
        expect(options.headers.get("Authorization")).toBe(
          "Bearer paxu_fixture",
        );
        expect(options.headers.get("CF-Access-Client-Secret")).toBe(
          `${region}-secret`,
        );
        for (const name of [
          "Cookie",
          "X-User-Email",
          "X-Pax-User-ID",
          "X-Pax-Identity-Assertion",
        ])
          expect(options.headers.has(name)).toBe(false);
      }
      expect(upstream).toHaveBeenCalledTimes(combinations.length);
    },
  );

  it("requires a real user API-key shape before the legacy native origin", async () => {
    const upstream = vi.fn();
    vi.stubGlobal("fetch", upstream);
    const request = new Request(
      `${env.PUBLIC_ORIGIN}/api/v1/native/us/starter`,
      {
        headers: {
          Authorization: "Bearer tenant-or-service-secret",
          "X-User-Email": "owner@example.invalid",
        },
      },
    );
    expect((await nativeFetch(request, env)).status).toBe(401);
    expect(upstream).not.toHaveBeenCalled();
  });
});
