// The one owner of the simulation credential in the Sovereign. The Axon parent
// process reads the document once at start, refreshes it with one timer, writes
// each refreshed document back to OpenBao with check-and-set, and hands the
// access token alone to each run. Nothing here logs any part of the document.
import { readFileSync } from "node:fs";
import type { CredentialConfig } from "./config.js";
import { HttpError, type TurnManager } from "./turns.js";

/** The key of the provider tool's credential document; the document is stored unchanged. */
export const DOCUMENT_KEY = "claudeAiOauth";
/** The token endpoint and the client identifier of the provider tool (moved here, not changed). */
export const TOKEN_URL = "https://console.anthropic.com/v1/oauth/token";
const CLIENT_ID = "9d1c250a-e61b-44d9-88ed-5944d1962f5e";
/** The projected service-account token (audience openbao) of the OpenBao login. */
export const SA_TOKEN_FILE = "/var/run/secrets/openova-token/token";
/** A failed refresh is retried after these delays, then every RETRY_LAST_MS. */
export const RETRY_MS = [60000, 120000, 240000];
export const RETRY_LAST_MS = 600000;
/** A failed write-back other than check-and-set is retried this often; the new token is used meanwhile. */
export const PERSIST_RETRY_MS = 30000;
const REQUEST_TIMEOUT_MS = 30000;
/** setTimeout fires at once beyond this delay; a longer wait is armed again when it ends. */
const MAX_TIMER_MS = 2147483647;

export type CredentialState = "absent" | "valid" | "expired" | "rejected" | "conflict";

/** The credential object of GET /stats; the times are ISO strings, "" when not yet. */
export interface CredentialReport { state: CredentialState; expiresAt?: string; lastRefreshAt?: string; lastPersistAt?: string; lastPersistError?: string }

interface OAuth { accessToken: string; refreshToken: string; expiresAt: number; [field: string]: unknown }

export interface Timers { set(fn: () => void, ms: number): unknown; clear(handle: unknown): void }

export interface CredentialOptions extends CredentialConfig {
  saTokenFile?: string;
  fetch?: typeof fetch;
  now?: () => number;
  /** The text of a file, or undefined when it does not exist. */
  readFile?: (path: string) => string | undefined;
  timers?: Timers;
  log?: (line: string) => void;
}

function readText(path: string): string | undefined {
  try {
    return readFileSync(path, "utf-8");
  } catch (err) {
    if ((err as NodeJS.ErrnoException).code === "ENOENT") return undefined;
    throw err;
  }
}

const realTimers: Timers = {
  set: (fn, ms) => setTimeout(fn, ms).unref(),
  clear: (handle) => clearTimeout(handle as ReturnType<typeof setTimeout>),
};

/** The OAuth object of a document, or undefined when the text is not one. A parse error quotes the text: never logged. */
function oauthOf(text: string): OAuth | undefined {
  let o: Record<string, unknown> | undefined;
  try {
    o = (JSON.parse(text) as Record<string, Record<string, unknown>> | null)?.[DOCUMENT_KEY];
  } catch {
    return undefined;
  }
  if (typeof o !== "object" || o === null || !Number.isInteger(o.expiresAt)) return undefined;
  const set = (v: unknown) => typeof v === "string" && v !== "";
  return set(o.accessToken) && set(o.refreshToken) ? (o as OAuth) : undefined;
}

const iso = (ms: number | undefined) => (ms === undefined ? "" : new Date(ms).toISOString());

/** A failed request; its message names the step and the status, never a body. */
class Failed extends Error {}

export class Credential {
  private oauth: OAuth | undefined;
  /** Refreshing has stopped for the life of the process. */
  private halted: "rejected" | "conflict" | undefined;
  /** The KV version last read or written; check-and-set is done on it. */
  private version: number | undefined;
  private nextRefreshAt = 0;
  /** Set while the document in use is not yet written back. */
  private nextPersistAt: number | undefined;
  private failures = 0;
  private lastRefreshAt: number | undefined;
  private lastPersistAt: number | undefined;
  private lastPersistError = "";
  private timer: unknown;
  private busy = false;
  private started = false;
  private stopped = false;
  private o: Required<CredentialOptions>;

  constructor(options: CredentialOptions) {
    this.o = { saTokenFile: SA_TOKEN_FILE, fetch, now: Date.now, readFile: readText, timers: realTimers, log: () => {}, ...options };
  }

  /** Reads the document once. No file, an empty file or a malformed document: state absent. */
  load(): void {
    let text: string | undefined;
    try {
      text = this.o.readFile(this.o.file);
    } catch (err) {
      return this.o.log(`${this.o.file} cannot be read (${(err as NodeJS.ErrnoException).code}): every step answers 503`);
    }
    this.oauth = text === undefined ? undefined : oauthOf(text);
    if (!this.oauth) return this.o.log(`no credential document at ${this.o.file}: every step answers 503`);
    this.nextRefreshAt = this.oauth.expiresAt - this.o.refreshMarginMs;
    this.o.log(`credential loaded, valid until ${iso(this.oauth.expiresAt)}`);
  }

  state(): CredentialReport {
    if (!this.oauth) return { state: "absent" };
    const { lastRefreshAt, lastPersistAt, lastPersistError } = this;
    return { state: this.current(), expiresAt: iso(this.oauth.expiresAt), lastRefreshAt: iso(lastRefreshAt), lastPersistAt: iso(lastPersistAt), lastPersistError };
  }

  /** The access token for one run; 503 simulation_credential_invalid unless the state is valid. */
  accessToken(): string {
    const state = this.current();
    if (state !== "valid") throw new HttpError(503, "simulation_credential_invalid", `the simulation credential is ${state}`);
    return (this.oauth as OAuth).accessToken;
  }

  /** Reads the KV version once, then arms the one refresh timer. */
  async start(): Promise<void> {
    if (!this.oauth || this.stopped || this.started) return;
    this.started = true;
    await this.readVersion().catch((err) => this.persistFailed(err));
    this.arm();
  }

  stop(): void {
    this.stopped = true;
    this.disarm();
  }

  private current(): CredentialState {
    if (!this.oauth) return "absent";
    return this.halted ?? (this.o.now() >= this.oauth.expiresAt ? "expired" : "valid");
  }

  private disarm(): void {
    if (this.timer !== undefined) this.o.timers.clear(this.timer);
    this.timer = undefined;
  }

  /** The one timer: at the next refresh, or at the next write-back retry when that comes first. */
  private arm(): void {
    this.disarm();
    if (this.stopped || this.halted || this.busy || !this.oauth) return;
    const at = Math.min(this.nextRefreshAt, this.nextPersistAt ?? Infinity);
    this.timer = this.o.timers.set(() => {
      this.timer = undefined;
      void this.tick();
    }, Math.min(Math.max(0, at - this.o.now()), MAX_TIMER_MS));
  }

  private async tick(): Promise<void> {
    if (this.busy || this.halted || this.stopped || !this.oauth) return;
    this.busy = true;
    try {
      const now = this.o.now();
      if (now >= this.nextRefreshAt) await this.refresh(this.oauth);
      else if (this.nextPersistAt !== undefined && now >= this.nextPersistAt) await this.persist(this.oauth);
    } finally {
      this.busy = false;
      this.arm();
    }
  }

  private async refresh(old: OAuth): Promise<void> {
    const body = JSON.stringify({ grant_type: "refresh_token", refresh_token: old.refreshToken, client_id: CLIENT_ID });
    const res = await this.request(TOKEN_URL, { method: "POST", headers: { "content-type": "application/json" }, body }).catch(() => undefined);
    if (res?.status === 400 || res?.status === 401) {
      this.halted = "rejected";
      return this.o.log(`the provider refused the refresh token (HTTP ${res.status}): refreshing stops, every step answers 503`);
    }
    if (!res?.ok) return this.refreshFailed(res ? `HTTP ${res.status}` : "not reached");
    const data = (await res.json().catch(() => ({}))) as { access_token?: unknown; refresh_token?: unknown; expires_in?: unknown };
    if (typeof data.access_token !== "string" || data.access_token === "" || typeof data.expires_in !== "number") {
      return this.refreshFailed("no token in the answer");
    }
    const next: OAuth = { ...old, accessToken: data.access_token, expiresAt: this.o.now() + data.expires_in * 1000 };
    if (typeof data.refresh_token === "string" && data.refresh_token !== "") next.refreshToken = data.refresh_token;
    this.failures = 0;
    this.lastRefreshAt = this.o.now();
    this.o.log(`credential refreshed, valid until ${iso(next.expiresAt)}`);
    await this.persist(next);
  }

  private refreshFailed(why: string): void {
    const delay = RETRY_MS[this.failures++] ?? RETRY_LAST_MS;
    this.nextRefreshAt = this.o.now() + delay;
    this.o.log(`refresh failed (${why}); next try in ${delay / 1000} s`);
  }

  /** Writes next back with check-and-set, then puts it in use. Only a lost check-and-set keeps it out. */
  private async persist(next: OAuth): Promise<void> {
    try {
      if (this.version === undefined) await this.readVersion();
      const document = JSON.stringify({ [DOCUMENT_KEY]: next });
      const res = await this.request(`${this.o.openbaoAddr}/v1/${this.o.kvPath}`, {
        method: "POST",
        headers: { "content-type": "application/json", "x-vault-token": await this.login() },
        body: JSON.stringify({ options: { cas: this.version }, data: { document } }),
      });
      if (res.status === 400 && (await res.text().catch(() => "")).includes("check-and-set")) {
        this.halted = "conflict";
        return this.o.log("the write-back lost check-and-set, another writer exists: refreshing stops, every step answers 503");
      }
      const version = res.ok ? ((await res.json().catch(() => ({}))) as { data?: { version?: unknown } }).data?.version : undefined;
      if (typeof version !== "number") throw new Failed(`write failed: HTTP ${res.status}`);
      this.version = version;
      this.lastPersistAt = this.o.now();
      this.lastPersistError = "";
      this.nextPersistAt = undefined;
      this.o.log(`credential written back, version ${version}`);
    } catch (err) {
      this.persistFailed(err);
      this.nextPersistAt = this.o.now() + PERSIST_RETRY_MS;
    }
    this.oauth = next;
    this.nextRefreshAt = next.expiresAt - this.o.refreshMarginMs;
  }

  private persistFailed(err: unknown): void {
    this.lastPersistError = err instanceof Failed ? err.message : "OpenBao was not reached";
    this.o.log(this.lastPersistError);
  }

  private async readVersion(): Promise<void> {
    const path = this.o.kvPath.replace("/data/", "/metadata/");
    const res = await this.request(`${this.o.openbaoAddr}/v1/${path}`, { headers: { "x-vault-token": await this.login() } });
    if (res.status === 404) {
      this.version = 0;
      return;
    }
    const version = res.ok ? ((await res.json().catch(() => ({}))) as { data?: { current_version?: unknown } }).data?.current_version : undefined;
    if (typeof version !== "number") throw new Failed(`version read failed: HTTP ${res.status}`);
    this.version = version;
  }

  private async login(): Promise<string> {
    const jwt = this.o.readFile(this.o.saTokenFile)?.trim();
    if (!jwt) throw new Failed("login failed: no service-account token");
    const body = JSON.stringify({ role: this.o.role, jwt });
    const res = await this.request(`${this.o.openbaoAddr}/v1/auth/kubernetes/login`, { method: "POST", headers: { "content-type": "application/json" }, body });
    const token = res.ok ? ((await res.json().catch(() => ({}))) as { auth?: { client_token?: unknown } }).auth?.client_token : undefined;
    if (typeof token !== "string") throw new Failed(`login failed: HTTP ${res.status}`);
    return token;
  }

  private request(url: string, init: RequestInit): Promise<Response> {
    const f = this.o.fetch;
    return f(url, { ...init, signal: AbortSignal.timeout(REQUEST_TIMEOUT_MS) });
  }
}

/** Every step needs a valid credential: until there is one, each step answers 503 simulation_credential_invalid. */
export function guarded(turns: TurnManager, credential: Pick<Credential, "accessToken">): TurnManager {
  return {
    async step(req, organization, sink, signal) {
      credential.accessToken();
      return turns.step(req, organization, sink, signal);
    },
    stats: () => turns.stats(),
    counters: () => turns.counters(),
    shutdown: () => turns.shutdown(),
  };
}
