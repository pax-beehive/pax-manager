import { beforeAll, afterAll, expect, it } from "vitest";
import { Miniflare } from "miniflare";
import { generateKeyPair, exportJWK, SignJWT } from "jose";
import { readFile } from "node:fs/promises";
let mf: Miniflare;
let cookies: string;
beforeAll(async () => {
  const keys = await generateKeyPair("RS256");
  const jwk = await exportJWK(keys.publicKey);
  const token = await new SignJWT({
    sid: "session_test",
    client_id: "client_test",
  })
    .setSubject("user_test")
    .setIssuer("https://api.workos.com/")
    .setIssuedAt()
    .setExpirationTime("5m")
    .setProtectedHeader({ alg: "RS256", kid: "test" })
    .sign(keys.privateKey);
  mf = new Miniflare({
    workers: [
      {
        name: "gateway",
        modules: true,
        scriptPath: "dist/index.js",
        compatibilityDate: "2026-07-01",
        d1Databases: ["DB"],
        outboundService: "upstream",
        bindings: {
          WORKOS_ENABLED: "true",
          WORKOS_BROWSER_ENABLED: "true",
          WORKOS_CLIENT_ID: "client_test",
          WORKOS_API_KEY: "sk_test",
          WORKOS_COOKIE_PASSWORD: "s".repeat(40),
          PUBLIC_ORIGIN: "https://pax.example",
          BOOTSTRAP_ENABLED: "true",
          DIRECTORY_SECRET: "d".repeat(40),
          ACCESS_PROVIDERS: "[]",
          ROUTING_KEY_ID: "test",
          US_MANAGER_URL: "https://us.example",
          HK_MANAGER_URL: "https://hk.example",
          US_PROVISIONING_SECRET: "u".repeat(40),
          HK_PROVISIONING_SECRET: "h".repeat(40),
          US_ACCESS_CLIENT_ID: "us_service",
          US_ACCESS_CLIENT_SECRET: "us_secret",
          HK_ACCESS_CLIENT_ID: "hk_service",
          HK_ACCESS_CLIENT_SECRET: "hk_secret",
        },
      },
      {
        name: "upstream",
        modules: true,
        compatibilityDate: "2026-07-01",
        bindings: {
          JWKS: { keys: [{ ...jwk, kid: "test", alg: "RS256" }] },
          TOKEN: token,
        },
        script: `export default {async fetch(request,env){
 const url=new URL(request.url);
 if(url.hostname==='api.workos.com') {
  if(url.pathname==='/sso/jwks/client_test') return Response.json(env.JWKS);
  if(url.pathname==='/user_management/authenticate') {
   const input=await request.json();
   if(!input.code_verifier || input.code!=='valid_code' || input.client_id!=='client_test' || input.client_secret!=='sk_test') return Response.json({error:'invalid_grant'},{status:400});
   return Response.json({user:{object:'user',id:'user_test',email:'owner@example.com',email_verified:true,first_name:'Test',last_name:'Owner',created_at:'2026-01-01T00:00:00Z',updated_at:'2026-01-01T00:00:00Z'},access_token:env.TOKEN,refresh_token:'refresh_test',authentication_method:'OAuth'});
  }
  return new Response('unexpected provider route',{status:500});
 }
 if(request.headers.get('CF-Access-Client-Id')!=='hk_service' || request.headers.get('CF-Access-Client-Secret')!=='hk_secret') return new Response('missing service auth',{status:403});
 if(url.pathname==='/internal/users/ensure') return Response.json({data:await request.json()});
 if(url.pathname==='/health') return new Response('ok');
 const assertion=request.headers.get('X-Pax-Identity-Assertion');if(!assertion)return new Response('missing proof',{status:401});
 const proof=JSON.parse(atob(assertion.split('.')[0].replaceAll('-','+').replaceAll('_','/')));
 if(proof.user_id!=='usr_original' || proof.region!=='hk' || proof.path!==url.pathname || proof.method!==request.method)return new Response('wrong proof',{status:401});
 if(request.headers.has('Cookie')||request.headers.has('Cf-Access-Jwt-Assertion')||request.headers.has('Authorization'))return new Response('leaked browser credentials',{status:500});
 return Response.json({code:200,data:{user_id:proof.user_id,email:proof.identity_key}});
 }}`,
      },
    ],
  });
  const db = await mf.getD1Database("DB", "gateway");
  for (const file of ["0001_directory.sql", "0002_workos_identities.sql"])
    await db.exec(
      (await readFile("migrations/" + file, "utf8"))
        .split("\n")
        .filter((x) => !x.startsWith("--"))
        .join(" "),
    );
  await db
    .prepare(
      "INSERT INTO user_regions(user_id,identity_key,region) VALUES ('usr_original','owner@example.com','hk')",
    )
    .run();
}, 20000);
afterAll(async () => {
  await mf?.dispose();
});
it("completes PKCE, seals an SDK session, and reads the unchanged legacy account in workerd", async () => {
  const login = await mf.dispatchFetch(
    "https://pax.example/auth/login?returnTo=/auth/session",
    { redirect: "manual" },
  );
  expect(login.status).toBe(303);
  const authorization = new URL(login.headers.get("Location")!);
  const state = authorization.searchParams.get("state");
  expect(state).toBeTruthy();
  expect(authorization.searchParams.get("code_challenge_method")).toBe("S256");
  const callback = await mf.dispatchFetch(
    "https://pax.example/auth/callback?code=valid_code&state=" + state,
    {
      headers: { Cookie: login.headers.get("Set-Cookie")!.split(";")[0] },
      redirect: "manual",
    },
  );
  expect(callback.status).toBe(303);
  cookies = callback.headers
    .getSetCookie()
    .map((x) => x.split(";")[0])
    .filter((x) => x.startsWith("__Host-pax_session."))
    .join("; ");
  expect(cookies).not.toContain("refresh_test");
  const session = await mf.dispatchFetch("https://pax.example/auth/session", {
    headers: { Cookie: cookies },
  });
  expect(session.status).toBe(200);
  expect(await session.json()).toMatchObject({
    authenticated: true,
    user_id: "usr_original",
    region: "hk",
  });
});
it("bootstraps and proxies through the signed Manager boundary without forwarding caller credentials", async () => {
  const bootstrap = await mf.dispatchFetch(
    "https://pax.example/api/v1/region/bootstrap",
    {
      method: "POST",
      headers: {
        Cookie: cookies,
        Origin: "https://pax.example",
        "Content-Type": "application/json",
      },
      body: "{}",
    },
  );
  expect(bootstrap.status).toBe(200);
  expect(await bootstrap.json()).toMatchObject({
    user_id: "usr_original",
    region: "hk",
  });
  const profile = await mf.dispatchFetch(
    "https://pax.example/api/pax/api/v1/user/self/me",
    {
      headers: {
        Cookie: cookies,
        Authorization: "Bearer attacker",
        "X-Pax-Identity-Assertion": "attacker",
        "Cf-Access-Jwt-Assertion": "attacker",
      },
    },
  );
  expect(profile.status).toBe(200);
  expect(await profile.json()).toMatchObject({
    data: { user_id: "usr_original", email: "owner@example.com" },
  });
});
it("rejects cross-site writes and unauthenticated browser requests", async () => {
  expect(
    (await mf.dispatchFetch("https://pax.example/api/pax/api/v1/user/self/me"))
      .status,
  ).toBe(401);
  const response = await mf.dispatchFetch(
    "https://pax.example/api/pax/api/v1/user/self/me",
    {
      method: "POST",
      headers: { Cookie: cookies, Origin: "https://evil.example" },
    },
  );
  expect(response.status).toBe(403);
});
