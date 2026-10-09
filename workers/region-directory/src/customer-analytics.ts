import type { Env } from "./index";
import type { Region } from "./directory";
import { originIdentityHeaders } from "./browser-proxy";
import { managerURL } from "./provision";

export const customerAnalyticsPath = "/api/v1/user/self/customer-analytics";

// Both Managers authorize the original browser identity. No service credential
// or caller-selected origin can turn a normal user into an analytics reader.
export async function customerAnalytics(
  request: Request,
  env: Env,
): Promise<Response> {
  const headers = {
    "Content-Type": "application/json",
    "Cache-Control": "private, no-store",
    Vary: "Cookie, Cf-Access-Jwt-Assertion",
  };
  if (request.method !== "GET")
    return Response.json(
      { code: 405, message: "method not allowed" },
      { status: 405, headers },
    );
  const results = await Promise.all(
    (["us", "hk"] as Region[]).map(async (region) => {
      try {
        const response = await fetch(
          new URL(customerAnalyticsPath, managerURL(env, region)),
          {
            method: "GET",
            headers: originIdentityHeaders(request),
            redirect: "manual",
            cache: "no-store",
            signal: AbortSignal.any([
              request.signal,
              AbortSignal.timeout(6_000),
            ]),
          },
        );
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
          data?: {
            regions?: {
              region: string;
              available: boolean;
              updated_at: string;
              users: unknown[];
            }[];
          };
        };
        const item = body.data?.regions?.find((item) => item.region === region);
        if (
          !item?.available ||
          !Array.isArray(item.users) ||
          !Number.isFinite(Date.parse(item.updated_at))
        )
          throw new Error("Invalid snapshot");
        return item;
      } catch {
        return { region, available: false, error: "unavailable" };
      }
    }),
  );
  // Never return partial customer data if either region denies authorization.
  if (results.some((item) => "error" in item && item.error === "forbidden"))
    return Response.json(
      { code: 403, message: "administrator access required in both regions" },
      { status: 403, headers },
    );
  return Response.json({ code: 200, data: { regions: results } }, { headers });
}
