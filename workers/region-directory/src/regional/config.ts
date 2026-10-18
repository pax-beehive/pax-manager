import type { Region } from "../directory";
export interface GatewayConfig {
  region: Region;
  authority: boolean;
  origins: Record<Region, string>;
  managerURL: string;
  consoleURL: string;
  peerURL: string;
  databaseURL: string;
  secret: string;
  provisioningSecret: string;
  clientID: string;
  apiKey: string;
  cookiePassword: string;
  port: number;
  entry: boolean;
}
function required(env: NodeJS.ProcessEnv, name: string) {
  const value = env[name];
  if (!value) throw new Error(`Missing ${name}`);
  return value;
}
function origin(value: string, internal = false) {
  const url = new URL(value);
  if (
    url.origin !== value ||
    url.username ||
    url.password ||
    (url.protocol !== "https:" && !(internal && url.protocol === "http:"))
  )
    throw new Error("Invalid gateway origin");
  return value;
}
export function gatewayConfig(env: NodeJS.ProcessEnv): GatewayConfig {
  if (env.REGIONAL_DIRECTORY_READY !== "true")
    throw new Error(
      "Import and verify the account directory before enabling this gateway",
    );
  const region = required(env, "PAX_REGION");
  const authorityRegion = required(env, "DIRECTORY_AUTHORITY_REGION");
  if (!["us", "hk"].includes(region) || !["us", "hk"].includes(authorityRegion))
    throw new Error("Invalid directory region");
  const secret = required(env, "DIRECTORY_REPLICATION_SECRET");
  const provisioningSecret = required(env, "REGION_PROVISIONING_SECRET");
  const cookiePassword = required(env, "WORKOS_COOKIE_PASSWORD");
  if (
    [secret, provisioningSecret, cookiePassword].some(
      (value) => value.length < 32,
    ) ||
    new Set([secret, provisioningSecret, cookiePassword]).size !== 3
  )
    throw new Error(
      "Gateway secrets must be distinct and at least 32 characters",
    );
  const origins = {
    us: origin(required(env, "US_PUBLIC_ORIGIN")),
    hk: origin(required(env, "HK_PUBLIC_ORIGIN")),
  };
  if (origins.us === origins.hk)
    throw new Error("Regional origins must differ");
  const port = Number(env.PORT ?? "8080");
  if (!Number.isInteger(port) || port < 1 || port > 65535)
    throw new Error("Invalid port");
  const peerURL = origin(required(env, "DIRECTORY_PEER_URL"));
  if (peerURL === origins[region as Region])
    throw new Error("Directory peer cannot be self");
  return {
    region: region as Region,
    authority: region === authorityRegion,
    origins,
    secret,
    entry: env.REGIONAL_ENTRY_ORIGIN === origins[region as Region],
    provisioningSecret,
    cookiePassword,
    peerURL,
    port,
    managerURL: origin(required(env, "REGIONAL_MANAGER_URL"), true),
    consoleURL: origin(required(env, "REGIONAL_CONSOLE_URL"), true),
    databaseURL: required(env, "DIRECTORY_DATABASE_URL"),
    clientID: required(env, "WORKOS_CLIENT_ID"),
    apiKey: required(env, "WORKOS_API_KEY"),
  };
}
