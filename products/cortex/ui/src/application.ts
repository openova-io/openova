// Every save of a definition goes through the platform's Application update route: the whole
// spec.parameters, the version the screen read in If-Match, and bindings exactly as read. When
// someone saved in between, the route refuses (409 conflict, or 412); the screen then reloads and
// shows the difference. Any other refusal, such as 409 managed-by-git, is passed on as it is.

import type { Config } from "./config.js";
import { ApiError, call, callJSON, type Fetch } from "./http.js";
import { diff, type Difference, type Params } from "./params.js";

export interface Loaded {
  parameters: Params;
  etag: string; // the version the route returned; the next save sends it in If-Match
}

// Conflict is a save refused because the Application changed after it was read.
export class Conflict extends Error {
  constructor(
    readonly current: Loaded,
    readonly differences: Difference[],
  ) {
    super("the Application changed since it was read");
  }
}

export class Applications {
  constructor(
    private readonly cfg: Config,
    private readonly token: () => string,
    private readonly fetchFn: Fetch,
  ) {}

  url(): string {
    const c = this.cfg;
    const path = `/api/v1/sovereigns/${encodeURIComponent(c.sovereign)}/applications/${encodeURIComponent(c.application)}`;
    return `${c.platformURL}${path}?namespace=${encodeURIComponent(c.namespace)}`;
  }

  async load(): Promise<Loaded> {
    const res = await call(this.fetchFn, this.token, this.url());
    const etag = res.headers.get("ETag");
    // Without a version a save could overwrite another person's; the screens then do not save.
    if (!etag) throw new ApiError(res.status, "no-version-from-the-application-route");
    const body = (await res.json()) as { parameters?: unknown };
    // The save sends the parameters whole: a read without them would replace what is stored.
    const p = body.parameters;
    if (typeof p !== "object" || p === null || Array.isArray(p)) throw new ApiError(res.status, "no-parameters-from-the-application-route");
    return { parameters: p as Params, etag };
  }

  // save sends edit applied to what was read. It returns the Application as stored after the
  // save, or throws Conflict with the Application as it is now.
  async save(read: Loaded, edit: (p: Params) => Params): Promise<Loaded> {
    const next: Params = { ...edit(structuredClone(read.parameters)) };
    if ("bindings" in read.parameters) next.bindings = structuredClone(read.parameters.bindings);
    else delete next.bindings;
    try {
      await callJSON(this.fetchFn, this.token, this.url(), {
        method: "PUT",
        headers: { "Content-Type": "application/json", "If-Match": read.etag },
        body: JSON.stringify({ parameters: next }),
      });
    } catch (err) {
      if (err instanceof ApiError && (err.status === 412 || (err.status === 409 && err.code === "conflict"))) {
        const current = await this.load();
        throw new Conflict(current, diff(next, current.parameters));
      }
      throw err;
    }
    return this.load();
  }
}
