import assert from "node:assert/strict";
import { test } from "node:test";
import { byTest, findAll, h, textOf, toDOM } from "./dom.js";
import { params } from "./fakes.js";
import { shellView } from "./screens/shell.js";
import { emptyState, type Actions, type State } from "./state.js";

const none = new Proxy({} as Actions, { get: () => async () => {} });
const state = (o: Partial<State>): State => ({ ...emptyState(), app: { parameters: params(), etag: '"v1"' }, ...o });

test("sources: each definition beside the state cortex reports; a source not reported yet says so", () => {
  const s = state({
    sources: [
      { id: "hr-policies", title: "HR policies", state: "error", documents: 3, chunks: 12, lastSyncAt: "2026-11-20T09:00:00Z", lastError: "parse failed" },
      { id: "old", title: "Old", state: "idle", documents: 0, chunks: 0, lastSyncAt: null, lastError: null },
    ],
  });
  s.app!.parameters.sources!.push({ id: "board", title: "Board", connector: { type: "upload" }, access: { readers: [], managers: ["group:b"] } });
  const rows = findAll(byTest(shellView(s, none), "sources"), (n) => n.tag === "tr").slice(1).map(textOf);
  assert.deepEqual(rows, [
    "hr-policiesHR policiesorg:membersgroup:hr-adminserror3122026-11-20 09:00:00parse failedRemove",
    "boardBoardgroup:bnot reported yetRemove",
    "oldOldidle00neverremoved",
  ]);
});

test("upload: the upload sources to choose from, the size limit, or why nothing can be uploaded", () => {
  const s = state({ tab: "upload" });
  s.app!.parameters.policy = { maxUploadMb: 20 };
  const form = byTest(shellView(s, none), "upload");
  assert.deepEqual(findAll(form, (n) => n.tag === "option").map(textOf), ["HR policies (hr-policies)"]);
  assert.match(textOf(form), /at most 20 MB/);
  const empty = state({ tab: "upload" });
  empty.app!.parameters.sources = [];
  assert.match(textOf(byTest(shellView(empty, none), "no-upload-source")), /No upload source is defined/);
});

test("documents: search hits with page and score, and the documents of this page", () => {
  const s = state({
    tab: "documents",
    hits: [{ documentId: "d1", sourceId: "hr-policies", title: "Leave", uri: "", chunk: { id: 1, text: "Thirty days.", page: 4, score: 0.8123 } }],
  });
  const screen = shellView(s, none);
  assert.equal(textOf(findAll(byTest(screen, "hits"), (n) => n.tag === "tr")[1]), "Leavehr-policiespage 40.81Thirty days.Open");
  assert.match(textOf(byTest(screen, "no-documents")), /None yet/);
});

test("jobs: newest first and whether the stream is open", () => {
  const job = (state: string) => ({ jobId: "j1", sourceId: "hr-policies", documentId: "d1", kind: "index", state, at: "2026-11-20T10:00:00Z" });
  const screen = shellView(state({ tab: "jobs", live: true, jobs: [job("done"), job("running")] }), none);
  assert.match(textOf(byTest(screen, "live")), /^Live/);
  assert.deepEqual(findAll(byTest(screen, "jobs"), (n) => n.tag === "tr").slice(1).map((r) => textOf(r.children[2] as never)), ["done", "running"]);
});

test("assistants: definitions with tiers, and no ask form when no assistant is open to the user", () => {
  const screen = shellView(state({ tab: "assistants", models: [] }), none);
  assert.match(textOf(byTest(screen, "assistants")), /staff-helpdeskStaff helpdeskhr-policies \(primary\)org:members/);
  assert.match(textOf(byTest(screen, "no-models")), /No assistant is open to you/);
  assert.equal(byTest(screen, "tab-assistants").props["aria-current"], "page");
  assert.equal(byTest(screen, "tab-sources").props["aria-current"], null);
  // A list cortex did not give is not an empty list: the screen says why it has none.
  const unread = shellView(state({ tab: "assistants", models: null, unread: { models: "http-503" } }), none);
  assert.equal(textOf(byTest(unread, "models-not-read")), "The assistants open to you could not be read: http-503.");
  assert.equal(findAll(unread, (n) => n.props["data-test"] === "no-models" || n.props["data-test"] === "ask").length, 0);
});

test("toDOM: handlers become listeners, value is set after the options, absent attributes are left out", () => {
  type El = { tag: string; attrs: Record<string, string>; on: string[]; kids: unknown[]; value?: string; optionsWhenValueSet?: number };
  const doc = {
    createTextNode: (t: string) => t,
    createElement: (tag: string): El & Record<string, unknown> => {
      const el: El & Record<string, unknown> = {
        tag,
        attrs: {},
        on: [],
        kids: [],
        setAttribute: (k: string, v: string) => void (el.attrs[k] = v),
        addEventListener: (k: string) => void el.on.push(k),
        appendChild: (c: unknown) => void el.kids.push(c),
      };
      Object.defineProperty(el, "value", { set: (v: string) => ((el.optionsWhenValueSet = el.kids.length), (el.attrs["(value)"] = v)) });
      return el;
    },
  } as unknown as Document;
  const node = h("select", { name: "tier", value: "secondary", required: true, disabled: false, title: null, onchange: () => {} }, h("option", null, "a"), h("option", null, "b"));
  const el = toDOM(node, doc) as unknown as El;
  assert.deepEqual(el.attrs, { name: "tier", "(value)": "secondary", required: "" });
  assert.deepEqual(el.on, ["change"]);
  assert.equal(el.optionsWhenValueSet, 2);
});
