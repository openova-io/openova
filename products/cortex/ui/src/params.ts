// The Application's spec.parameters and the edits the screens make to it. An edit returns a new
// object and never touches bindings, which the install route wrote. The checks are those of the
// Blueprint's configSchema, so a mistake is named before the save; the platform checks again.

export interface SourceDef {
  id: string;
  title: string;
  connector: { type: string };
  parse?: { profile: string };
  access: { readers: string[]; managers: string[] };
}
export interface AssistantSource {
  id: string;
  tier?: string;
}
export interface AssistantDef {
  id: string;
  title: string;
  instructions: string;
  sources: AssistantSource[];
  retrieval?: { topK?: number; rerank?: boolean };
  users: string[];
}
export type Params = Record<string, unknown> & { sources?: SourceDef[]; assistants?: AssistantDef[] };

export const ID = /^[a-z][a-z0-9-]{1,40}$/;
export const READER = /^(user|group|agent):[^\s]{1,200}$|^org:members$/;
export const MANAGER = /^(user|group):[^\s]{1,200}$/;
export const TIERS = ["primary", "secondary", "fallback"];
export const PROFILES = ["documents", "text"];

// principals splits what a person typed: one principal per line or comma.
export function principals(text: string): string[] {
  return text
    .split(/[\n,]/)
    .map((s) => s.trim())
    .filter((s) => s !== "");
}

function badPrincipals(list: string[], form: RegExp, what: string): string[] {
  return list.filter((p) => !form.test(p)).map((p) => `${what} "${p}" is not a principal of that form`);
}

export function sourceProblems(s: SourceDef, p: Params): string[] {
  const out: string[] = [];
  if (!ID.test(s.id)) out.push("id: a lower-case letter, then 1 to 40 letters, digits or dashes");
  if ((p.sources ?? []).some((x) => x.id === s.id)) out.push(`id: a source ${s.id} exists`);
  if (s.title.trim() === "" || s.title.length > 120) out.push("title: 1 to 120 characters");
  if (s.parse && !PROFILES.includes(s.parse.profile)) out.push(`parser: one of ${PROFILES.join(", ")}`);
  if (s.access.managers.length === 0) out.push("managers: at least one");
  out.push(...badPrincipals(s.access.readers, READER, "reader"), ...badPrincipals(s.access.managers, MANAGER, "manager"));
  return out;
}

export function assistantProblems(a: AssistantDef, p: Params): string[] {
  const out: string[] = [];
  const known = new Set((p.sources ?? []).map((s) => s.id));
  if (!ID.test(a.id)) out.push("id: a lower-case letter, then 1 to 40 letters, digits or dashes");
  if ((p.assistants ?? []).some((x) => x.id === a.id)) out.push(`id: an assistant ${a.id} exists`);
  if (a.title.trim() === "" || a.title.length > 120) out.push("title: 1 to 120 characters");
  if (a.instructions.length > 8000) out.push("instructions: at most 8000 characters");
  if (a.sources.length === 0) out.push("sources: at least one");
  for (const s of a.sources) {
    if (!known.has(s.id)) out.push(`source ${s.id} is not defined`);
    if (s.tier !== undefined && !TIERS.includes(s.tier)) out.push(`tier ${s.tier} is not one of ${TIERS.join(", ")}`);
  }
  const topK = a.retrieval?.topK;
  if (topK !== undefined && !(Number.isInteger(topK) && topK >= 1 && topK <= 50)) out.push("topK: a whole number from 1 to 50");
  out.push(...badPrincipals(a.users, READER, "user"));
  return out;
}

export function withSource(p: Params, s: SourceDef): Params {
  return { ...p, sources: [...(p.sources ?? []), s] };
}

// assistantsUsing names the assistants that read source id; such a source is not removed.
export function assistantsUsing(p: Params, id: string): string[] {
  return (p.assistants ?? []).filter((a) => a.sources.some((s) => s.id === id)).map((a) => a.id);
}

export function withoutSource(p: Params, id: string): Params {
  const users = assistantsUsing(p, id);
  if (users.length > 0) throw new Error(`source ${id} is read by assistant ${users.join(", ")}: remove it from them first`);
  return { ...p, sources: (p.sources ?? []).filter((s) => s.id !== id) };
}

export function withAssistant(p: Params, a: AssistantDef): Params {
  return { ...p, assistants: [...(p.assistants ?? []), a] };
}

export function withoutAssistant(p: Params, id: string): Params {
  return { ...p, assistants: (p.assistants ?? []).filter((a) => a.id !== id) };
}

export interface Difference {
  path: string;
  yours: unknown; // in the save that was refused; undefined: absent there
  stored: unknown; // in the Application now; undefined: absent there
}

function keyed(v: unknown): v is { id: string }[] {
  return Array.isArray(v) && v.every((x) => typeof x === "object" && x !== null && typeof (x as { id?: unknown }).id === "string");
}

// diff lists every leaf where yours and stored differ. A list of objects with an id is compared
// by id, so a path reads sources[hr-policies].title rather than a position.
export function diff(yours: unknown, stored: unknown, path = ""): Difference[] {
  if (JSON.stringify(yours) === JSON.stringify(stored)) return [];
  const at = (k: string, list: boolean): string => (list ? `${path}[${k}]` : path === "" ? k : `${path}.${k}`);
  if (keyed(yours) && keyed(stored)) {
    const a = new Map(yours.map((x) => [x.id, x]));
    const b = new Map(stored.map((x) => [x.id, x]));
    const ids = [...new Set([...a.keys(), ...b.keys()])];
    return ids.flatMap((id) => diff(a.get(id), b.get(id), at(id, true)));
  }
  const plain = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null && !Array.isArray(v);
  if (plain(yours) && plain(stored)) {
    const keys = [...new Set([...Object.keys(yours), ...Object.keys(stored)])].sort();
    return keys.flatMap((k) => diff(yours[k], stored[k], at(k, false)));
  }
  return [{ path: path === "" ? "(all)" : path, yours, stored }];
}
