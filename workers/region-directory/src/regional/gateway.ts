import type { Env } from "../index";
import type { Assignment } from "../directory";
import {
  authenticateSession,
  authRoute,
  SessionFailure,
  workos,
  withCookies,
} from "../workos-auth";
import { approvePaxlLogin } from "../paxl-login";
import { verifyNativeToken } from "../native-client";
import { allowedNativeUserRoute } from "../native";
import {
  browserPath,
  crossSite,
  originIdentityHeaders,
} from "../browser-proxy";
import { attachWorkosIdentity, addOriginAssertion } from "../origin-assertion";
import { provision } from "../provision";
import {
  ReplicatedDirectory,
  verifyDirectoryRequest,
  type Command,
} from "./directory";
import type { GatewayConfig } from "./config";
import { nativeLimiter } from "./limiter";

export interface ProxyPlan {
  target: string;
  path: string;
  headers?: Headers;
  cookies: string[];
}
const json = (value: unknown, status = 200, cookies: string[] = []) => {
  const headers = new Headers({
    "Content-Type": "application/json",
    "Cache-Control": "no-store",
  });
  for (const cookie of cookies) headers.append("Set-Cookie", cookie);
  return new Response(JSON.stringify(value), { status, headers });
};
export function createGateway(
  config: GatewayConfig,
  directory: ReplicatedDirectory,
  overrides: Partial<{
    authenticate: typeof authenticateSession;
    verifyNative: typeof verifyNativeToken;
    auth: typeof authRoute;
    provision: typeof provision;
    workos: typeof workos;
    approve: typeof approvePaxlLogin;
    limitNative: (subject: string) => boolean;
  }> = {},
) {
  const deps = {
    authenticate: authenticateSession,
    verifyNative: verifyNativeToken,
    auth: authRoute,
    provision,
    workos,
    approve: approvePaxlLogin,
    limitNative: nativeLimiter(),
    ...overrides,
  };
  const env: Env = {
    DB: directory,
    LOCAL_REGION: config.region,
    PUBLIC_ORIGIN: config.origins[config.region],
    WORKOS_ENABLED: "true",
    WORKOS_BROWSER_ENABLED: "true",
    NATIVE_API_ENABLED: "true",
    WORKOS_CLIENT_ID: config.clientID,
    WORKOS_API_KEY: config.apiKey,
    WORKOS_COOKIE_PASSWORD: config.cookiePassword,
    ACCESS_PROVIDERS: "[]",
    DIRECTORY_SECRET: config.secret,
    US_MANAGER_URL: config.managerURL,
    HK_MANAGER_URL: config.managerURL,
    US_PROVISIONING_SECRET: config.provisioningSecret,
    HK_PROVISIONING_SECRET: config.provisioningSecret,
  };
  const prepare = async (assignment: Assignment) => {
    if (assignment.region !== config.region)
      throw new Error("Account belongs to another region");
    await deps.provision(assignment, env);
  };
  const ready = (
    assignment: Assignment,
    native: boolean,
    cookies: string[],
    email?: string,
  ) =>
    json(
      {
        status: "ready",
        ...(email ? { email } : {}),
        user_id: assignment.user_id,
        region: assignment.region,
        origin: config.origins[assignment.region],
        api_url:
          config.origins[assignment.region] +
          (native ? "/api/v1/native/client/" : "/api/pax"),
      },
      200,
      cookies,
    );
  return {
    env,
    async handle(request: Request): Promise<Response | ProxyPlan> {
      const url = new URL(request.url);
      try {
        if (url.origin !== env.PUBLIC_ORIGIN)
          return json({ error: "not_found" }, 404);
        if (url.pathname === "/internal/account-directory") {
          const body = await request.text();
          if (
            body.length > 65536 ||
            !verifyDirectoryRequest(request, body, config.secret)
          )
            return json({ error: "unauthorized" }, 401);
          return json(
            await directory.execute(JSON.parse(body) as Command, prepare),
          );
        }
        if (url.pathname.startsWith("/internal/"))
          return json({ error: "not_found" }, 404);
        if (url.pathname === "/health") return json({ region: config.region });
        if (url.pathname.startsWith("/auth/")) return deps.auth(request, env);
        if (url.pathname === "/api/region-config")
          return json({
            enabled: true,
            mode: "direct",
            region: config.region,
            origin: env.PUBLIC_ORIGIN,
            origins: config.origins,
            entry: config.entry,
          });
        const native = url.pathname.startsWith("/api/v1/native/client/");
        const nativePath = native
          ? url.pathname.slice("/api/v1/native/client/".length)
          : "";
        const bootstrap =
          url.pathname === "/api/v1/region/bootstrap" ||
          (native && nativePath === "bootstrap");
        const logout = native && nativePath === "logout";
        const login = url.pathname === "/api/v1/region/paxl-login/approve";
        const path = native
          ? allowedNativeUserRoute(request.method, nativePath) ||
            (request.method === "GET" && nativePath === "public/paxd/download")
            ? `/api/v1/${nativePath}`
            : null
          : browserPath(url.pathname);
        if (!bootstrap && !logout && !login && !path) {
          if (
            url.pathname.startsWith("/api/v1/") ||
            url.pathname.startsWith("/api/pax")
          )
            return json({ error: "not_found" }, 404);
          return {
            target: config.consoleURL,
            path: url.pathname + url.search,
            cookies: [],
          };
        }
        if (crossSite(request, env.PUBLIC_ORIGIN))
          return json({ error: "invalid_origin" }, 403);
        if (
          ((bootstrap || logout || login) &&
            (request.method !== "POST" || url.search)) ||
          (native &&
            ["key", "api_key", "token", "authorization"].some((key) =>
              url.searchParams.has(key),
            ))
        )
          return json({ error: "invalid_request" }, 400);
        let nativeEmail: string | undefined;
        let identity: string,
          subject: string,
          cookies: string[] = [];
        if (native) {
          const verified = await deps.verifyNative(request, env);
          subject = verified.subject;
          if ((bootstrap || logout) && !deps.limitNative(subject))
            return json({ error: "rate_limited" }, 429);
          if (logout) {
            if (
              request.headers.get("Content-Type")?.split(";")[0] !==
              "application/json"
            )
              return json({ error: "json_required" }, 415);
            try {
              const raw = await request.text();
              if (raw.length > 1024) throw new Error("Large body");
              const body = JSON.parse(raw);
              if (
                !body ||
                typeof body !== "object" ||
                Array.isArray(body) ||
                Object.keys(body).length
              )
                throw new Error("Invalid logout");
            } catch {
              return json({ error: "invalid_request" }, 400);
            }
            await deps
              .workos(env)
              .userManagement.revokeSession({ sessionId: verified.sessionId });
            return json({ status: "signed_out" });
          }
          if (bootstrap) {
            const user = await deps.workos(env).userManagement.getUser(subject);
            if (user.id !== subject || !user.emailVerified)
              return json({ error: "unauthorized" }, 401);
            nativeEmail = user.email;
            identity = await directory.bind(user);
          } else identity = await directory.readIdentity(subject);
        } else {
          const session = await deps.authenticate(request, env);
          ({ identity, subject, cookies } = session);
        }
        let assignment = await directory.lookup(identity);
        if (bootstrap) {
          if (
            request.headers.get("Content-Type")?.split(";")[0] !==
            "application/json"
          )
            return json({ error: "json_required" }, 415, cookies);
          let body: { preferred_region?: "us" | "hk" };
          try {
            const raw = await request.text();
            if (raw.length > 1024) throw new Error("Large body");
            body = JSON.parse(raw);
            if (
              !body ||
              Array.isArray(body) ||
              Object.keys(body).some((key) => key !== "preferred_region") ||
              (body.preferred_region !== undefined &&
                !["us", "hk"].includes(body.preferred_region))
            )
              throw new Error("Invalid preference");
          } catch {
            return json({ error: "invalid_request" }, 400, cookies);
          }
          const newlyAssigned = !assignment && !!body.preferred_region;
          if (newlyAssigned)
            assignment = await directory.assign(
              identity,
              body.preferred_region!,
            );
          if (!assignment)
            return json(
              { status: "selection_required", regions: ["us", "hk"] },
              200,
              cookies,
            );
          if (assignment.region === config.region) await prepare(assignment);
          else await directory.preparePeer(identity);
          return ready(assignment, native, cookies, nativeEmail);
        }
        if (!assignment)
          return json({ error: "region_selection_required" }, 409, cookies);
        if (assignment.region !== config.region)
          return json(
            {
              error: "region_mismatch",
              region: assignment.region,
              origin: config.origins[assignment.region],
              api_url:
                config.origins[assignment.region] +
                (native ? "/api/v1/native/client/" : "/api/pax"),
            },
            409,
            cookies,
          );
        attachWorkosIdentity(request, { identity, subject });
        if (login)
          return withCookies(
            await deps.approve(request, env, identity),
            cookies,
          );
        return {
          target: config.managerURL,
          path: path! + url.search,
          headers: await addOriginAssertion(
            originIdentityHeaders(request),
            request,
            env,
            config.region,
            path!,
          ),
          cookies,
        };
      } catch (error) {
        if (error instanceof SessionFailure)
          return json(
            {
              error: error.status === 401 ? "unauthorized" : "auth_unavailable",
            },
            error.status,
            error.cookies,
          );
        if (
          error &&
          typeof error === "object" &&
          "status" in error &&
          error.status === 401
        )
          return json({ error: "unauthorized" }, 401);
        return json({ error: "directory_unavailable", retryable: true }, 503);
      }
    },
  };
}
