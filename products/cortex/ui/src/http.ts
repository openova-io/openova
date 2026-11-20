// One call with the signed-in user's token. A refused call becomes an ApiError that carries the
// status, the error code of the body ({"error": "<code>", "detail": "<text>"}) and Retry-After,
// so a screen can name the reason.

export type Fetch = typeof fetch;

export class ApiError extends Error {
  constructor(
    readonly status: number,
    readonly code: string,
    readonly detail = "",
    readonly retryAfter: string | null = null,
  ) {
    super(`${code} (HTTP ${status})`);
  }
}

export async function call(fetchFn: Fetch, token: () => string, url: string, init: RequestInit = {}): Promise<Response> {
  const headers = new Headers(init.headers);
  headers.set("Authorization", `Bearer ${token()}`);
  const res = await fetchFn(url, { ...init, headers });
  if (res.ok) return res;
  let code = `http-${res.status}`;
  let detail = "";
  try {
    const body = (await res.json()) as { error?: unknown; detail?: unknown };
    if (typeof body.error === "string" && body.error !== "") code = body.error;
    if (typeof body.detail === "string") detail = body.detail;
  } catch {
    // a body that is not JSON keeps the code http-<status>
  }
  throw new ApiError(res.status, code, detail, res.headers.get("Retry-After"));
}

export async function callJSON<T>(fetchFn: Fetch, token: () => string, url: string, init: RequestInit = {}): Promise<T> {
  return (await (await call(fetchFn, token, url, init)).json()) as T;
}

// describe turns an error into the line a screen shows.
export function describe(err: unknown): string {
  if (err instanceof ApiError) {
    const detail = err.detail ? `: ${err.detail}` : "";
    const wait = err.retryAfter ? `; try again in ${err.retryAfter} s` : "";
    return `Refused: ${err.code}${detail}${wait}`;
  }
  return err instanceof Error ? err.message : String(err);
}
