// Fakes the tests share: a fetch that records each call and answers from a function, and the
// settings of one Application. Only the tests import it; the build leaves it out.

import type { Config } from "./config.js";
import type { Params } from "./params.js";

export interface Seen {
  url: string;
  method: string;
  headers: Headers;
  body: BodyInit | null | undefined;
}

export function fakeFetch(answer: (req: Seen, n: number) => Response | Promise<Response>): { fetch: typeof fetch; seen: Seen[] } {
  const seen: Seen[] = [];
  const f = async (input: RequestInfo | URL, init: RequestInit = {}): Promise<Response> => {
    const req = { url: String(input), method: init.method ?? "GET", headers: new Headers(init.headers), body: init.body };
    seen.push(req);
    return answer(req, seen.length - 1);
  };
  return { fetch: f as typeof fetch, seen };
}

export function json(status: number, body: unknown, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json", ...headers } });
}

export const CFG: Config = {
  issuerURL: "https://auth.acme.example/realms/acme",
  clientId: "cortex",
  cortexURL: "https://cortex.acme.example",
  platformURL: "https://console.sovereign.example",
  sovereign: "s1",
  application: "cortex",
  namespace: "acme",
};

export const APP_URL = "https://console.sovereign.example/api/v1/sovereigns/s1/applications/cortex?namespace=acme";

// params is the spec.parameters of an installed cortex: bindings written by the install route.
export function params(): Params {
  return {
    bindings: { db: { secret: "cortex-db", host: "pg.acme" }, originals: { bucket: "acme-originals" } },
    models: { embed: { alias: "embed-default", dimensions: 1024 }, chat: { alias: "agent-fast" } },
    sources: [
      { id: "hr-policies", title: "HR policies", connector: { type: "upload" }, access: { readers: ["org:members"], managers: ["group:hr-admins"] } },
    ],
    assistants: [
      { id: "staff-helpdesk", title: "Staff helpdesk", instructions: "Cite.", sources: [{ id: "hr-policies" }], users: ["org:members"] },
    ],
  };
}

// A page small enough for the tests and close enough to a browser's for what dom.ts does: an
// element keeps its children, attributes, listeners, the value typed into it, its chosen files and
// its selection; an element taken out of the page takes the focus with it, as in a browser.
export class FakeNode {
  parentNode: FakeNode | null = null;
  readonly childNodes: FakeNode[] = [];
  readonly attrs: Record<string, string> = {};
  readonly listeners: Record<string, ((e: unknown) => void)[]> = {};
  value = "";
  files: File[] = [];
  selectionStart = 0;
  selectionEnd = 0;
  constructor(
    readonly ownerDocument: FakePage,
    readonly nodeType: number,
    readonly tagName: string,
    public nodeValue: string | null,
  ) {}
  appendChild(c: FakeNode): FakeNode {
    return this.insertBefore(c, null);
  }
  insertBefore(c: FakeNode, ref: FakeNode | null): FakeNode {
    if (c.parentNode) c.parentNode.removeChild(c);
    this.childNodes.splice(ref ? this.childNodes.indexOf(ref) : this.childNodes.length, 0, c);
    c.parentNode = this;
    return c;
  }
  removeChild(c: FakeNode): FakeNode {
    this.childNodes.splice(this.childNodes.indexOf(c), 1);
    c.parentNode = null;
    if (c.contains(this.ownerDocument.activeElement)) this.ownerDocument.activeElement = null;
    return c;
  }
  contains(n: FakeNode | null): boolean {
    for (let x = n; x; x = x.parentNode) if (x === this) return true;
    return false;
  }
  setAttribute(k: string, v: string): void {
    this.attrs[k] = v;
  }
  removeAttribute(k: string): void {
    delete this.attrs[k];
  }
  addEventListener(type: string, fn: (e: unknown) => void): void {
    (this.listeners[type] ??= []).push(fn);
  }
  dispatch(type: string, e: Record<string, unknown> = {}): void {
    for (const fn of this.listeners[type] ?? []) fn({ type, currentTarget: this, preventDefault() {}, ...e });
  }
  focus(): void {
    this.ownerDocument.activeElement = this;
  }
  get textContent(): string {
    return this.nodeType === 3 ? (this.nodeValue ?? "") : this.childNodes.map((c) => c.textContent).join("");
  }
  // find lists the elements under this one, itself included, that match.
  find(match: (n: FakeNode) => boolean): FakeNode[] {
    const own: FakeNode[] = match(this) ? [this] : [];
    return own.concat(...this.childNodes.map((c) => c.find(match)));
  }
  // one is the one element under this one with the attribute k equal to v.
  one(k: string, v: string): FakeNode {
    const found = this.find((n) => n.attrs[k] === v);
    if (found.length !== 1) throw new Error(`${k}=${v}: ${found.length} elements`);
    return found[0];
  }
}

export class FakePage {
  activeElement: FakeNode | null = null;
  createElement(tag: string): FakeNode {
    return new FakeNode(this, 1, tag.toUpperCase(), null);
  }
  createTextNode(text: string): FakeNode {
    return new FakeNode(this, 3, "#text", text);
  }
  // root is an element of this page to mount the screens in.
  root(): HTMLElement {
    return this.createElement("div") as unknown as HTMLElement;
  }
}
