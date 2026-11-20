// The screens read their settings from config.json beside index.html. The release that serves
// them writes that file; nothing is built into the bundle.

export interface Config {
  issuerURL: string; // the Organization's realm, the issuer of the sign-on
  clientId: string; // the public sign-on registration of cortex
  cortexURL: string; // base of the cortex routes; "" is this origin
  platformURL: string; // base of the platform API that holds the Application route
  sovereign: string; // {id} of /api/v1/sovereigns/{id}/applications/{name}
  application: string; // the Application name
  namespace: string; // the Application's namespace
}

const FIELDS = ["issuerURL", "clientId", "cortexURL", "platformURL", "sovereign", "application", "namespace"] as const;

export function parseConfig(raw: unknown): Config {
  if (typeof raw !== "object" || raw === null) throw new Error("config.json: not an object");
  const r = raw as Record<string, unknown>;
  const out: Record<string, string> = {};
  for (const f of FIELDS) {
    const v = r[f];
    if (typeof v !== "string" || (v === "" && f !== "cortexURL")) throw new Error(`config.json: ${f} is missing`);
    out[f] = v.replace(/\/+$/, "");
  }
  for (const f of ["issuerURL", "platformURL"] as const) {
    if (!/^https?:\/\/[^/\s]+/.test(out[f])) throw new Error(`config.json: ${f} is not an address`);
  }
  return out as unknown as Config;
}

export async function loadConfig(fetchFn: typeof fetch): Promise<Config> {
  const res = await fetchFn("./config.json", { cache: "no-store" });
  if (!res.ok) throw new Error(`config.json: HTTP ${res.status}`);
  return parseConfig(await res.json());
}
