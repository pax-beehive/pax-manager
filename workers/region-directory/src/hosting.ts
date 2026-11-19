/** Exact service-bearer ingress; Manager remains the role and ledger authority. */
export interface HostingEnv {
  HOSTING_SERVICE_REGION?: string;
  PUBLIC_ORIGIN?: string;
  US_MANAGER_URL?: string;
  US_ACCESS_CLIENT_ID?: string;
  US_ACCESS_CLIENT_SECRET?: string;
}

const enrollPath = "/api/v1/hosting/jobs/enroll";
const paths = new Set([
  "/api/v1/hosting/gateway/accounting",
  enrollPath,
  "/api/v1/hosting/jobs/claim",
  "/api/v1/hosting/jobs/heartbeat",
  "/api/v1/hosting/jobs/complete",
]);
const maxRequestBytes = 64 * 1024;
const maxResponseBytes = 2 * 1024 * 1024;

class IngressFailure extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
  ) {
    super(code);
  }
}

const json = (code: string, status: number) =>
  Response.json(
    { error: code },
    { status, headers: { "Cache-Control": "no-store" } },
  );

function origin(value: string | undefined): string {
  const url = new URL(value ?? "");
  if (
    url.protocol !== "https:" ||
    url.username ||
    url.password ||
    url.pathname !== "/" ||
    url.search ||
    url.hash
  )
    throw new Error("Invalid hosting origin");
  return url.origin;
}

function configuration(env: HostingEnv) {
  if (env.HOSTING_SERVICE_REGION !== "us")
    throw new Error("Hosting service ingress is disabled");
  const publicOrigin = origin(env.PUBLIC_ORIGIN);
  const managerOrigin = origin(env.US_MANAGER_URL);
  if (publicOrigin === managerOrigin)
    throw new Error("Hosting service origin cannot be self");
  const credentials = [env.US_ACCESS_CLIENT_ID, env.US_ACCESS_CLIENT_SECRET];
  if (
    credentials.some((value) => !value || !/^[\x21-\x7e]{1,4096}$/.test(value))
  )
    throw new Error("Hosting origin credentials are unavailable");
  return {
    publicOrigin,
    managerOrigin,
    clientID: credentials[0]!,
    clientSecret: credentials[1]!,
  };
}

async function boundedBody(
  stream: ReadableStream<Uint8Array> | null,
  maximum: number,
  signal: AbortSignal,
): Promise<Uint8Array<ArrayBuffer>> {
  if (signal.aborted) throw new Error("Hosting deadline exceeded");
  if (!stream) return new Uint8Array();
  const reader = stream.getReader();
  const cancel = () => {
    void reader.cancel().catch(() => {});
  };
  signal.addEventListener("abort", cancel, { once: true });
  const chunks: Uint8Array[] = [];
  let length = 0;
  try {
    for (;;) {
      const { value, done } = await reader.read();
      if (signal.aborted) throw new Error("Hosting deadline exceeded");
      if (done) break;
      length += value.length;
      if (length > maximum) throw new IngressFailure(413, "body_too_large");
      chunks.push(value);
    }
  } finally {
    signal.removeEventListener("abort", cancel);
    cancel();
  }
  const bytes = new Uint8Array(length);
  let offset = 0;
  for (const chunk of chunks) {
    bytes.set(chunk, offset);
    offset += chunk.length;
  }
  return bytes;
}

function contentLength(headers: Headers): number | undefined {
  const value = headers.get("Content-Length");
  if (value === null) return;
  if (!/^\d+$/.test(value)) throw new IngressFailure(400, "invalid_request");
  const length = Number(value);
  if (!Number.isSafeInteger(length) || length > maxRequestBytes)
    throw new IngressFailure(413, "body_too_large");
  return length;
}

export function createHostingWorker(
  options: Partial<{ fetch: typeof fetch; timeoutMs: number }> = {},
) {
  const timeoutMs = options.timeoutMs ?? 15_000;
  return {
    async fetch(request: Request, env: HostingEnv): Promise<Response> {
      const url = new URL(request.url);
      if (!paths.has(url.pathname)) return json("not_found", 404);
      if (request.method !== "POST") return json("method_not_allowed", 405);
      if (url.search || url.hash) return json("invalid_request", 400);
      let config: ReturnType<typeof configuration>;
      try {
        config = configuration(env);
      } catch {
        return json("hosting_ingress_unavailable", 503);
      }
      if (url.origin !== config.publicOrigin) return json("not_found", 404);
      const bearer = /^Bearer ([\x21-\x7e]{32,4096})$/i.exec(
        request.headers.get("Authorization") ?? "",
      );
      if (!bearer) return json("unauthorized", 401);
      const nodeKey =
        url.pathname === enrollPath ? request.headers.get("X-Pax-Key") : null;
      if (
        url.pathname === enrollPath &&
        (!nodeKey || !/^[\x21-\x2b\x2d-\x7e]{1,4096}$/.test(nodeKey))
      )
        return json("unauthorized", 401);
      if (
        request.headers
          .get("Content-Type")
          ?.split(";")[0]
          .trim()
          .toLowerCase() !== "application/json" ||
        (request.headers.has("Content-Encoding") &&
          request.headers.get("Content-Encoding") !== "identity") ||
        request.headers.has("Upgrade")
      )
        return json("json_required", 415);

      const controller = new AbortController();
      const timer = setTimeout(() => controller.abort(), timeoutMs);
      const abort = () => controller.abort();
      request.signal.addEventListener("abort", abort, { once: true });
      if (request.signal.aborted) abort();
      try {
        const declaredLength = contentLength(request.headers);
        const body = await boundedBody(
          request.body,
          maxRequestBytes,
          controller.signal,
        );
        if (declaredLength !== undefined && declaredLength !== body.length)
          throw new IngressFailure(400, "invalid_request");
        const headers = new Headers({
          Authorization: `Bearer ${bearer[1]}`,
          "Content-Type": "application/json",
          Accept: "application/json",
          "Accept-Encoding": "identity",
          "Cache-Control": "no-store",
          "CF-Access-Client-Id": config.clientID,
          "CF-Access-Client-Secret": config.clientSecret,
        });
        if (nodeKey) headers.set("X-Pax-Key", nodeKey);
        const response = await (options.fetch ?? fetch)(
          config.managerOrigin + url.pathname,
          {
            method: "POST",
            headers,
            body,
            redirect: "manual",
            cache: "no-store",
            signal: controller.signal,
          },
        );
        if (
          (response.status !== 200 &&
            (response.status < 400 || response.status > 599)) ||
          response.headers
            .get("Content-Type")
            ?.split(";")[0]
            .trim()
            .toLowerCase() !== "application/json"
        ) {
          void response.body?.cancel().catch(() => {});
          throw new Error("Unexpected hosting origin response");
        }
        let result: Uint8Array<ArrayBuffer>;
        try {
          result = await boundedBody(
            response.body,
            maxResponseBytes,
            controller.signal,
          );
        } catch {
          throw new Error("Invalid hosting origin response");
        }
        JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(result));
        const output = new Headers({
          "Content-Type": "application/json",
          "Cache-Control": "no-store",
        });
        const retry = response.headers.get("Retry-After");
        if (retry && /^\d{1,5}$/.test(retry)) output.set("Retry-After", retry);
        return new Response(result, {
          status: response.status,
          headers: output,
        });
      } catch (error) {
        if (controller.signal.aborted)
          return json("hosting_ingress_timeout", 504);
        if (error instanceof IngressFailure)
          return json(error.code, error.status);
        return json("hosting_origin_unavailable", 502);
      } finally {
        clearTimeout(timer);
        request.signal.removeEventListener("abort", abort);
      }
    },
  };
}

export default createHostingWorker();
