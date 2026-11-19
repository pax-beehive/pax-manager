import { afterEach, expect, it, vi } from "vitest";
import { createWorker, type Env } from "../src/index";

const accounting = "/api/v1/hosting/gateway/accounting";
const key = "gateway-" + "g".repeat(40);
const env = {
  WORKOS_BROWSER_ENABLED: "true",
  HOSTING_SERVICE_REGION: "us",
  PUBLIC_ORIGIN: "https://pax.example",
  US_MANAGER_URL: "https://us.example",
  HK_MANAGER_URL: "https://hk.example",
  US_ACCESS_CLIENT_ID: "existing-us-client",
  US_ACCESS_CLIENT_SECRET: "existing-us-secret",
} as Env;

const request = (path = accounting, method = "POST") =>
  new Request(env.PUBLIC_ORIGIN + path, {
    method,
    headers: {
      Authorization: `Bearer ${key}`,
      "Content-Type": "application/json",
      Cookie: "forged-user-cookie",
      "X-User-Email": "forged@example.invalid",
      "X-Pax-Identity-Assertion": "forged-user-assertion",
    },
    body: method === "POST" ? "{}" : undefined,
  });

afterEach(() => vi.unstubAllGlobals());

it("dispatches only service hosting requests before browser login without directory or user identity", async () => {
  const upstream = vi.fn<typeof fetch>(async () =>
    Response.json({ code: 401, message: "unauthorized" }, { status: 401 }),
  );
  vi.stubGlobal("fetch", upstream);
  const identity = vi.fn(async () => {
    throw new Error("service requests must not authenticate a user");
  });
  const lookup = vi.fn(async () => {
    throw new Error("service requests must not touch the directory");
  });
  const app = createWorker({ identity, lookup, lookupUser: lookup });
  const response = await app.fetch(request(), env);
  expect(response.status).toBe(401);
  expect(response.headers.has("Set-Cookie")).toBe(false);
  expect(response.headers.get("Cache-Control")).toBe("no-store");
  expect(upstream).toHaveBeenCalledTimes(1);
  const [target, options] = upstream.mock.calls[0];
  expect(target).toBe(env.US_MANAGER_URL + accounting);
  const headers = new Headers(options!.headers);
  expect(headers.get("Authorization")).toBe(`Bearer ${key}`);
  expect(headers.get("CF-Access-Client-Id")).toBe(env.US_ACCESS_CLIENT_ID);
  expect(headers.get("CF-Access-Client-Secret")).toBe(
    env.US_ACCESS_CLIENT_SECRET,
  );
  for (const name of ["Cookie", "X-User-Email", "X-Pax-Identity-Assertion"])
    expect(headers.has(name)).toBe(false);
  expect(identity).not.toHaveBeenCalled();
  expect(lookup).not.toHaveBeenCalled();
});

it("keeps hosting disabled without explicit US selection and refuses neighboring capabilities", async () => {
  const upstream = vi.fn<typeof fetch>();
  vi.stubGlobal("fetch", upstream);
  const app = createWorker();
  expect(
    (await app.fetch(request(), { ...env, HOSTING_SERVICE_REGION: undefined }))
      .status,
  ).toBe(503);
  expect(
    (await app.fetch(request(), { ...env, HOSTING_SERVICE_REGION: "hk" }))
      .status,
  ).toBe(503);
  expect((await app.fetch(request(accounting, "GET"), env)).status).toBe(405);
  for (const path of [
    accounting + "/",
    "/api/v1/hosting/jobs/delete",
    "/api/v1/hosting/model",
  ])
    expect((await app.fetch(request(path), env)).status).toBe(404);
  expect(upstream).not.toHaveBeenCalled();
});

it("continues to require browser authentication for ordinary user APIs", async () => {
  const upstream = vi.fn<typeof fetch>();
  vi.stubGlobal("fetch", upstream);
  const response = await createWorker().fetch(
    request("/api/pax/api/v1/user/self/me", "GET"),
    env,
  );
  expect(response.status).toBe(401);
  expect(upstream).not.toHaveBeenCalled();
});
