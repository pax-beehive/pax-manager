import { addOriginAssertion } from "./origin-assertion";
import { originIdentityHeaders } from "./browser-proxy";
import { managerURL } from "./provision";
import type { Env } from "./index";
import type { Region } from "./directory";

export const notificationSubscriptionsPath =
  "/api/v1/user/self/notification-subscriptions";
const states = ["enabled", "disabled", "revoked", "expired", "invalidated"];
const summaryKeys = ["total", ...states, "enabled_users"];

function metadata(row: Record<string, unknown>) {
  const result: Record<string, unknown> = {};
  for (const key of [
    "receiver_id",
    "user_id",
    "email",
    "device_label",
    "channel",
    "status",
    "permission",
  ]) {
    if (typeof row[key] !== "string")
      throw new Error("Invalid receiver metadata");
    result[key] = row[key];
  }
  if (
    !states.includes(String(row.status)) ||
    !Number.isSafeInteger(row.revision) ||
    Number(row.revision) < 1
  )
    throw new Error("Invalid receiver state");
  result.revision = row.revision;
  for (const key of [
    "permission_reported_at",
    "created_at",
    "last_registered_at",
    "expires_at",
  ]) {
    if (key === "expires_at" && !row[key]) continue;
    if (
      typeof row[key] !== "string" ||
      !Number.isFinite(Date.parse(row[key] as string))
    )
      throw new Error("Invalid receiver date");
    result[key] = row[key];
  }
  result.latest_delivery = null;
  if (row.latest_delivery) {
    const attempt = row.latest_delivery as Record<string, unknown>;
    if (
      !["pending", "accepted", "failed", "expired", "suppressed"].includes(
        String(attempt.status),
      ) ||
      typeof attempt.attempted_at !== "string" ||
      !Number.isFinite(Date.parse(attempt.attempted_at)) ||
      !Number.isSafeInteger(attempt.attempts) ||
      Number(attempt.attempts) < 0
    )
      throw new Error("Invalid delivery metadata");
    const safe: Record<string, unknown> = {
      status: attempt.status,
      attempted_at: attempt.attempted_at,
      attempts: attempt.attempts,
    };
    if (attempt.provider_status !== undefined) {
      if (
        !Number.isSafeInteger(attempt.provider_status) ||
        Number(attempt.provider_status) < 100 ||
        Number(attempt.provider_status) > 599
      )
        throw new Error("Invalid provider status");
      safe.provider_status = attempt.provider_status;
    }
    if (attempt.error_category !== undefined) {
      if (
        typeof attempt.error_category !== "string" ||
        !/^[a-z0-9_:-]{1,64}$/.test(attempt.error_category)
      )
        throw new Error("Invalid error category");
      safe.error_category = attempt.error_category;
    }
    if (attempt.next_attempt_at !== undefined) {
      if (
        typeof attempt.next_attempt_at !== "string" ||
        !Number.isFinite(Date.parse(attempt.next_attempt_at))
      )
        throw new Error("Invalid retry time");
      safe.next_attempt_at = attempt.next_attempt_at;
    }
    result.latest_delivery = safe;
  }
  return result;
}

export async function notificationSubscriptions(
  request: Request,
  env: Env,
): Promise<Response> {
  const headers = {
    "Content-Type": "application/json",
    "Cache-Control": "private, no-store",
    Vary: "Cookie, Cf-Access-Jwt-Assertion",
  };
  const respond = (status: number, message: string) =>
    Response.json({ code: status, message }, { status, headers });
  if (request.method !== "GET") return respond(405, "method not allowed");
  const query = new URL(request.url).searchParams;
  const selected = query.get("region");
  if (selected && selected !== "all" && selected !== "us" && selected !== "hk")
    return respond(400, "invalid region");
  const regions = (
    selected && selected !== "all" ? [selected] : ["us", "hk"]
  ) as Region[];
  const results = await Promise.all(
    regions.map(async (region) => {
      try {
        const target = new URL(
          notificationSubscriptionsPath,
          managerURL(env, region),
        );
        for (const key of ["search", "user_id", "status", "channel", "limit"])
          if (query.has(key)) target.searchParams.set(key, query.get(key)!);
        const cursor = query.get(`after_${region}`) ?? query.get("after");
        if (cursor) target.searchParams.set("after", cursor);
        const response = await fetch(target, {
          method: "GET",
          headers: await addOriginAssertion(
            originIdentityHeaders(request),
            request,
            env,
            region,
            notificationSubscriptionsPath,
          ),
          redirect: "manual",
          cache: "no-store",
          signal: AbortSignal.any([request.signal, AbortSignal.timeout(6000)]),
        });
        if (!response.ok) {
          await response.body?.cancel();
          return {
            region,
            available: false,
            error:
              response.status === 401 || response.status === 403
                ? "forbidden"
                : "unavailable",
          };
        }
        const body = (await response.json()) as {
          data?: { regions?: Record<string, unknown>[] };
        };
        const source = body.data?.regions?.find(
          (source) => source.region === region,
        );
        if (
          !source?.available ||
          typeof source.updated_at !== "string" ||
          !Number.isFinite(Date.parse(source.updated_at)) ||
          !Array.isArray(source.receivers) ||
          source.receivers.length > 100 ||
          !source.summary
        )
          throw new Error("Invalid snapshot");
        const summary = source.summary as Record<string, unknown>;
        if (
          summaryKeys.some(
            (key) =>
              !Number.isSafeInteger(summary[key]) || Number(summary[key]) < 0,
          )
        )
          throw new Error("Invalid totals");
        return {
          region,
          available: true,
          registration_enabled:
            typeof source.registration_enabled === "boolean"
              ? source.registration_enabled
              : undefined,
          updated_at: source.updated_at,
          summary: Object.fromEntries(
            summaryKeys.map((key) => [key, summary[key]]),
          ),
          receivers: source.receivers.map((row) =>
            metadata(row as Record<string, unknown>),
          ),
          next_cursor:
            typeof source.next_cursor === "string" ? source.next_cursor : "",
        };
      } catch {
        return { region, available: false, error: "unavailable" };
      }
    }),
  );
  if (
    results.some((result) => "error" in result && result.error === "forbidden")
  )
    return respond(403, "administrator access required in both regions");
  return Response.json({ code: 200, data: { regions: results } }, { headers });
}
