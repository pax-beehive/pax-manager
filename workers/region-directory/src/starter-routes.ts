// Owner operations keep the Manager's existing paths and authentication.
export function allowedStarterRoute(method: string, path: string): boolean {
  return (
    (path === "/api/v1/starter" && (method === "GET" || method === "POST")) ||
    (path === "/api/v1/starter/wake" && method === "POST")
  );
}
