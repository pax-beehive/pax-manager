import { beforeAll, afterAll, expect, it } from "vitest";
import { Miniflare } from "miniflare";
import { readFile } from "node:fs/promises";

let mf: Miniflare;
const gatewayKey = "gateway-" + "g".repeat(40);
const hostKey = "host-" + "h".repeat(40);
const accounting = "/api/v1/hosting/gateway/accounting";
const enrollment = "/api/v1/hosting/jobs/enroll";
beforeAll(async () => {
  const script = await readFile(
    new URL("../dist/index.js", import.meta.url),
    "utf8",
  );
  const bindings = {
    WORKOS_BROWSER_ENABLED: "true",
    HOSTING_SERVICE_REGION: "us",
    PUBLIC_ORIGIN: "https://pax.example",
    US_MANAGER_URL: "https://us.example",
    HK_MANAGER_URL: "https://hk.example",
    US_ACCESS_CLIENT_ID: "existing-us-id",
    US_ACCESS_CLIENT_SECRET: "existing-us-secret",
  };
  mf = new Miniflare({
    workers: [
      {
        name: "entry",
        script,
        modules: true,
        compatibilityDate: "2026-07-01",
        bindings,
        outboundService: "origin",
      },
      {
        name: "disabled",
        script,
        modules: true,
        compatibilityDate: "2026-07-01",
        bindings: { ...bindings, HOSTING_SERVICE_REGION: "" },
        outboundService: "origin",
      },
      {
        name: "origin",
        modules: true,
        compatibilityDate: "2026-07-01",
        script: `export default {async fetch(request) {
 const url=new URL(request.url);
 if(url.origin!=='https://us.example'||request.method!=='POST')return Response.json({error:'wrong_destination'},{status:500});
 if(request.headers.get('CF-Access-Client-Id')!=='existing-us-id'||request.headers.get('CF-Access-Client-Secret')!=='existing-us-secret')return Response.json({error:'wrong_access'},{status:500});
 for(const name of ['Cookie','X-User-Email','X-Pax-User-ID','X-Pax-Identity-Assertion','Cf-Access-Jwt-Assertion'])if(request.headers.has(name))return Response.json({error:'identity_leak'},{status:500});
 const expectedNodeKey=url.pathname==='${enrollment}'?'node-key':null;
 if(request.headers.get('X-Pax-Key')!==expectedNodeKey)return Response.json({error:'wrong_node_key_scope'},{status:500});
 const expected=url.pathname.endsWith('/accounting')?'${gatewayKey}':'${hostKey}';
 if(request.headers.get('Authorization')!=='Bearer '+expected)return Response.json({code:401,message:'unauthorized'},{status:401});
 return Response.json({code:200,data:{path:url.pathname,body:await request.text()}},{headers:{'Set-Cookie':'must-not-leak=1'}});
}}`,
      },
    ],
  });
});
afterAll(async () => mf?.dispose());

function input(key = gatewayKey) {
  return {
    method: "POST",
    headers: {
      Authorization: "Bearer " + key,
      "Content-Type": "application/json",
      Cookie: "forged=1",
      "X-User-Email": "forged@example.invalid",
      "X-Pax-User-ID": "usr_forged",
      "X-Pax-Identity-Assertion": "forged",
      "Cf-Access-Jwt-Assertion": "forged",
      "CF-Access-Client-Secret": "forged",
      "X-Pax-Key": "node-key",
    },
    body: "{ }\n",
  };
}

it("forwards the five service paths through workerd with only the expected credentials", async () => {
  for (const path of [
    accounting,
    enrollment,
    ...["claim", "heartbeat", "complete"].map(
      (name) => "/api/v1/hosting/jobs/" + name,
    ),
  ]) {
    const response = await mf.dispatchFetch(
      "https://pax.example" + path,
      input(path === accounting ? gatewayKey : hostKey),
    );
    expect(response.status).toBe(200);
    expect(await response.json()).toEqual({
      code: 200,
      data: { path, body: "{ }\n" },
    });
    expect(response.headers.has("Set-Cookie")).toBe(false);
    expect(response.headers.get("Cache-Control")).toBe("no-store");
  }
});

it("preserves Manager role rejection and default-disabled workerd configuration", async () => {
  expect(
    (await mf.dispatchFetch("https://pax.example" + accounting, input(hostKey)))
      .status,
  ).toBe(401);
  expect(
    (
      await mf.dispatchFetch(
        "https://pax.example" + enrollment,
        input(gatewayKey),
      )
    ).status,
  ).toBe(401);
  const disabled = await mf.getWorker("disabled");
  expect(
    (await disabled.fetch("https://pax.example" + accounting, input())).status,
  ).toBe(503);
  expect(
    (
      await mf.dispatchFetch(
        "https://pax.example/api/v1/hosting/jobs/delete",
        input(),
      )
    ).status,
  ).toBe(404);
  expect(
    (await mf.dispatchFetch("https://pax.example" + accounting)).status,
  ).toBe(405);
});
