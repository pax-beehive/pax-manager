import { afterAll, beforeAll, expect, it } from "vitest";
import { Miniflare } from "miniflare";
import { readFile } from "node:fs/promises";
let mf: Miniflare;
beforeAll(async () => {
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
          MACHINE_ROUTING_ENABLED: "true",
          MACHINE_PUBLIC_ORIGIN: "https://machine.example",
          US_MACHINE_URL: "https://us-node.example",
          HK_MACHINE_URL: "https://hk-node.example",
        },
      },
      {
        name: "origin",
        modules: true,
        compatibilityDate: "2026-07-01",
        script: `export default {async fetch(request){
   const u=new URL(request.url);
   if(u.pathname==='/api/v1/node/identity'){
    if(u.hostname!=='hk-node.example'||request.headers.get('X-Pax-Key')!=='valid-key')return new Response(null,{status:401});
    return Response.json({data:{node_id:'node_real',user_id:'usr_hk',region:'hk'}});
   }
   if(u.hostname!=='hk-node.example')return new Response('business reached wrong region',{status:500});
   if(request.headers.get('Cookie')||request.headers.get('X-User-Email'))return new Response('untrusted identity',{status:500});
   if(request.headers.get('Upgrade')==='websocket'){
    const pair=new WebSocketPair();pair[1].accept();pair[1].addEventListener('message',e=>pair[1].send('hk:'+e.data));
    return new Response(null,{status:101,webSocket:pair[0]});
   }
   if(u.pathname==='/api/v1/node/status')return new Response(await request.text(),{headers:{'Content-Type':'application/json'}});
   return new Response('data: hk\\n\\n',{headers:{'Content-Type':'text/event-stream'}});
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
  await db.exec(
    "INSERT INTO user_regions (user_id,identity_key,region) VALUES ('usr_hk','hk@example.test','hk'),('usr_us','us@example.test','us');",
  );
}, 20000);
afterAll(async () => mf?.dispose());
it("recovers a wrong routing hint with real D1 and preserves POST bodies", async () => {
  const response = await mf.dispatchFetch(
    "https://machine.example/api/v1/node/status",
    {
      method: "POST",
      headers: {
        "X-Pax-Key": "valid-key",
        "X-Pax-User-ID": "usr_us",
        Cookie: "ignored=1",
      },
      body: '{"agents":[]}',
    },
  );
  expect(response.status).toBe(200);
  expect(response.headers.get("X-Pax-User-ID")).toBe("usr_hk");
  expect(await response.json()).toEqual({ agents: [] });
});
it("returns repaired hints on a real WebSocket upgrade and streams frames", async () => {
  const response = await mf.dispatchFetch(
    "https://machine.example/api/v1/node/control",
    {
      headers: {
        "X-Pax-Key": "valid-key",
        "X-Pax-User-ID": "usr_us",
        Upgrade: "websocket",
      },
    },
  );
  expect(response.status).toBe(101);
  expect(response.headers.get("X-Pax-User-ID")).toBe("usr_hk");
  const socket = response.webSocket!;
  socket.accept();
  const message = new Promise((resolve) =>
    socket.addEventListener("message", (event) => resolve(event.data), {
      once: true,
    }),
  );
  socket.send("hello");
  expect(await message).toBe("hk:hello");
  socket.close();
});
it("streams machine SSE and recovers when the hint is missing", async () => {
  const response = await mf.dispatchFetch(
    "https://machine.example/api/v1/node/mailbox",
    { headers: { "X-Pax-Key": "valid-key" } },
  );
  expect(response.status).toBe(200);
  expect(response.headers.get("X-Pax-User-ID")).toBe("usr_hk");
  expect(await response.text()).toBe("data: hk\n\n");
});
