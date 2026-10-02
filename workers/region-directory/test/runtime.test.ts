import { afterAll, beforeAll, expect, it } from "vitest";
import { Miniflare } from "miniflare";
import { readFile } from "node:fs/promises";
import { generateKeyPair, exportJWK, SignJWT } from "jose";
let mf: Miniflare;
let access: string;
beforeAll(async () => {
  const keys = await generateKeyPair("RS256");
  const jwk = await exportJWK(keys.publicKey);
  access = await new SignJWT({ email: "runtime@example.com" })
    .setProtectedHeader({ alg: "RS256", kid: "access" })
    .setIssuer("https://test.cloudflareaccess.com")
    .setAudience("browser")
    .setIssuedAt()
    .setExpirationTime("1h")
    .sign(keys.privateKey);
  mf = new Miniflare({
    workers: [
      {
        name: "directory",
        modules: true,
        scriptPath: "dist/index.js",
        compatibilityDate: "2026-07-01",
        d1Databases: ["DB"],
        outboundService: "origin",
        bindings: {
          BOOTSTRAP_ENABLED: "true",
          PUBLIC_ORIGIN: "https://pax.example",
          ACCESS_PROVIDERS: JSON.stringify([
            {
              issuer: "https://test.cloudflareaccess.com",
              audience: "browser",
            },
          ]),
          DIRECTORY_SECRET: "d".repeat(40),
          ROUTING_KEY_ID: "v1",
          US_MANAGER_URL: "https://us.example",
          HK_MANAGER_URL: "https://hk.example",
          US_PROVISIONING_SECRET: "u".repeat(40),
          HK_PROVISIONING_SECRET: "h".repeat(40),
        },
      },
      {
        name: "origin",
        modules: true,
        compatibilityDate: "2026-07-01",
        bindings: { JWKS: { keys: [{ ...jwk, kid: "access", alg: "RS256" }] } },
        script: `export default {async fetch(request,env){
   const url=new URL(request.url);
   if(url.pathname==="/cdn-cgi/access/certs") return Response.json(env.JWKS);
   if(url.hostname!=="hk.example") return new Response("wrong region",{status:500});
   if(!request.headers.get("Cookie")?.startsWith("CF_Authorization="))return new Response("missing identity",{status:401});
   if(url.pathname==="/internal/users/ensure")return Response.json({data:await request.json()});
   if(request.headers.get("Upgrade")==="websocket"){
    const pair=new WebSocketPair();pair[1].accept();pair[1].addEventListener("message",event=>pair[1].send("hk:"+event.data));return new Response(null,{status:101,webSocket:pair[0]});
   }
   return new Response("data: hk\\n\\n",{headers:{"Content-Type":"text/event-stream","Cache-Control":"public"}});
  }}`,
      },
    ],
  });
  const db = await mf.getD1Database("DB", "directory");
  await db.exec(
    (await readFile("migrations/0001_directory.sql", "utf8")).replace(
      /\n/g,
      " ",
    ),
  );
}, 20000);
afterAll(async () => {
  await mf?.dispose();
});
it("runs verified HK signup, cookie recovery, SSE and real WebSocket frames in workerd", async () => {
  const headers = {
    "Cf-Access-Jwt-Assertion": access,
    Origin: "https://pax.example",
    "Content-Type": "application/json",
  };
  const result = await mf.dispatchFetch(
    "https://pax.example/api/v1/region/bootstrap",
    { method: "POST", headers, body: '{"preferred_region":"hk"}' },
  );
  expect(result.status).toBe(200);
  expect(await result.json()).toMatchObject({ status: "ready", region: "hk" });
  const cookie = result.headers
    .getSetCookie()
    .map((c) => c.split(";")[0])
    .join("; ");
  const stream = await mf.dispatchFetch(
    "https://pax.example/api/pax/api/v1/user/self/events",
    { headers: { ...headers, Cookie: cookie } },
  );
  expect(await stream.text()).toBe("data: hk\n\n");
  expect(stream.headers.get("Cache-Control")).toBe("no-store");
  const upgrade = await mf.dispatchFetch(
    "https://pax.example/api/v1/user/self/agents/agent_one/tunnel",
    { headers: { ...headers, Cookie: cookie, Upgrade: "websocket" } },
  );
  expect(upgrade.status).toBe(101);
  const socket = upgrade.webSocket!;
  socket.accept();
  const received = new Promise((resolve) =>
    socket.addEventListener("message", (event) => resolve(event.data), {
      once: true,
    }),
  );
  socket.send("hello");
  expect(await received).toBe("hk:hello");
  socket.close();
  const restored = await mf.dispatchFetch(
    "https://pax.example/api/v1/region/bootstrap",
    { method: "POST", headers, body: '{"preferred_region":"us"}' },
  );
  expect(await restored.json()).toMatchObject({
    status: "ready",
    region: "hk",
  });
});
