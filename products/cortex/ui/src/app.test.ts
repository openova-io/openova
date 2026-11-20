// The screens driven as a person does: a form is submitted or a button clicked on the rendered
// tree, and the test reads the calls that reached the routes and what the screen shows after.

import assert from "node:assert/strict";
import { test } from "node:test";
import { createApp } from "./app.js";
import { Applications } from "./application.js";
import { Cortex } from "./cortex.js";
import { byTest, findAll, mount, textOf, type VNode } from "./dom.js";
import type { JobEvent } from "./events.js";
import { APP_URL, CFG, fakeFetch, FakePage, json, params, type FakeNode, type Seen } from "./fakes.js";
import type { Params, SourceDef } from "./params.js";
import { shellView } from "./screens/shell.js";
import type { Actions, Tab } from "./state.js";

type Route = (req: Seen) => Response | undefined;

// harness answers each request from the first route that knows it. The Application starts at
// version "v1" with params(); a PUT stores what it was sent as the next version unless put says
// otherwise, and replace stores parameters as another person's save would.
function harness(opts: { put?: (req: Seen) => Response | undefined; routes?: Route[]; confirm?: boolean; redraw?: () => void } = {}) {
  let stored: Params = params();
  let version = 1;
  const f = fakeFetch((req) => {
    for (const r of opts.routes ?? []) {
      const res = r(req);
      if (res) return res;
    }
    if (req.url === APP_URL && req.method === "GET") return json(200, { parameters: stored }, { ETag: `"v${version}"` });
    if (req.url === APP_URL && req.method === "PUT") {
      const res = opts.put?.(req);
      if (res) return res;
      stored = JSON.parse(String(req.body)).parameters;
      version++;
      return json(200, {});
    }
    if (req.url.endsWith("/v1/sources")) return json(200, { items: [] });
    if (req.url.endsWith("/v1/models")) return json(200, { data: [{ id: "staff-helpdesk" }] });
    if (req.url.endsWith("/v1/events")) return new Response("", { status: 200 });
    return json(404, { error: "no-route-in-test" });
  });
  const later: (() => void)[] = [];
  let signIns = 0;
  const app = createApp({
    cortex: new Cortex("https://cortex.acme.example", () => "user-token", f.fetch),
    applications: new Applications(CFG, () => "user-token", f.fetch),
    redraw: () => opts.redraw?.(),
    confirm: () => opts.confirm ?? true,
    save: () => {},
    later: (w) => void later.push(w),
    signIn: () => void signIns++,
  });
  const pending: Promise<void>[] = [];
  const act = new Proxy(app.actions, {
    get: (t, k: keyof Actions) => (...a: never[]) => {
      const r = (t[k] as (...x: never[]) => Promise<void> | void)(...a);
      if (r) pending.push(r);
      return r;
    },
  });
  const view = (tab: Tab): VNode => {
    app.state.tab = tab;
    return shellView(app.state, act);
  };
  const settle = async (): Promise<void> => {
    while (pending.length > 0) await pending.shift();
  };
  const submit = async (tab: Tab, form: string, values: Record<string, string | File>): Promise<void> => {
    const fd = new FormData();
    for (const [k, v] of Object.entries(values)) fd.append(k, v);
    (byTest(view(tab), form).props.onsubmit as (e: unknown) => void)({ preventDefault() {}, currentTarget: fd });
    await settle();
  };
  const click = async (tab: Tab, name: string): Promise<void> => {
    (byTest(view(tab), name).props.onclick as (e: unknown) => void)({});
    await settle();
  };
  const replace = (p: Params): void => {
    stored = p;
    version++;
  };
  return { app, f, view, submit, click, settle, later, signIns: () => signIns, stored: () => stored, replace };
}

const puts = (seen: Seen[]): Seen[] => seen.filter((s) => s.method === "PUT");
const BOARD = { id: "board", title: "Board papers", readers: "group:board", managers: "user:sec@acme.example", profile: "documents" };
const BOARD_DEF: SourceDef = {
  id: "board",
  title: "Board papers",
  connector: { type: "upload" },
  parse: { profile: "documents" },
  access: { readers: ["group:board"], managers: ["user:sec@acme.example"] },
};
const DOC = { id: "d1", sourceId: "hr-policies", title: "leave.txt", uri: "", state: "queued", updatedAt: "2026-11-20T10:00:00Z" };

const tick = (): Promise<void> => new Promise((r) => setImmediate(r));
async function until(what: string, done: () => boolean): Promise<void> {
  for (let i = 0; i < 200 && !done(); i++) await tick();
  assert.ok(done(), what);
}

// jobStream is a job stream the test feeds frame by frame. Opened again after it ended, it ends at once.
function jobStream() {
  let feed: ReadableStreamDefaultController<Uint8Array> | null = null;
  let opened = 0;
  const route: Route = (r) => {
    if (!r.url.endsWith("/v1/events")) return undefined;
    if (++opened > 1) return new Response("", { status: 200 });
    return new Response(new ReadableStream<Uint8Array>({ start: (c) => void (feed = c) }), { status: 200 });
  };
  const send = (job: Partial<JobEvent>): void => {
    const frame: JobEvent = { jobId: "j1", sourceId: "hr-policies", documentId: "d1", kind: "index", state: "running", at: "2026-11-20T10:00:00Z", ...job };
    feed!.enqueue(new TextEncoder().encode(`event: job\ndata: ${JSON.stringify(frame)}\n\n`));
  };
  return { route, send, end: () => feed!.close(), opened: () => opened };
}

// page mounts the screens of t in a fake page, as main.ts does in the browser.
function page(redraw: { draw: () => void }, t: ReturnType<typeof harness>): { doc: FakePage; root: FakeNode } {
  const doc = new FakePage();
  const root = doc.root();
  redraw.draw = mount(root, () => shellView(t.app.state, t.app.actions));
  return { doc, root: root as unknown as FakeNode };
}

// submitForm submits a form of the page with the values its fields hold now, as a browser does.
function submitForm(form: FakeNode): void {
  const fd = new FormData();
  for (const el of form.find((n) => n.attrs.name !== undefined)) {
    if (el.attrs.type === "file") el.files.forEach((f) => fd.append(el.attrs.name, f));
    else fd.append(el.attrs.name, el.value);
  }
  form.dispatch("submit", { currentTarget: fd });
}

test("adding a source on the screen saves the Application with If-Match and bindings unchanged", async () => {
  const t = harness();
  await t.app.start();
  await t.submit("sources", "add-source", BOARD);
  assert.equal(puts(t.f.seen).length, 1, "exactly one save");
  const [put] = puts(t.f.seen);
  assert.equal(put.headers.get("If-Match"), '"v1"');
  const sent = JSON.parse(String(put.body)).parameters as Params;
  assert.deepEqual(sent.bindings, params().bindings);
  assert.deepEqual(sent.assistants, params().assistants);
  assert.deepEqual(sent.sources?.[1], BOARD_DEF);
  assert.equal(t.app.state.app?.etag, '"v2"');
  assert.match(textOf(t.view("sources")), /Saved: source board/);
  assert.match(textOf(byTest(t.view("sources"), "sources")), /Board papersgroup:boarduser:sec@acme.examplenot reported yet/);
});

test("a save answered 409 reloads the Application, shows the difference and offers the draft again", async () => {
  // Between the read and the save another person added the source minutes: the stored
  // Application is at v2 when the save of board arrives, and the save is refused.
  const minutes: SourceDef = { id: "minutes", title: "Minutes", connector: { type: "upload" }, access: { readers: ["group:board"], managers: ["group:secretariat"] } };
  let refused = false;
  const t = harness({
    put: () => {
      if (refused) return undefined;
      refused = true;
      t.replace({ ...params(), sources: [...(params().sources ?? []), minutes] });
      return json(409, { error: "conflict" });
    },
  });
  await t.app.start();
  await t.submit("sources", "add-source", BOARD);
  const screen = t.view("sources");
  assert.match(textOf(byTest(screen, "conflict")), /The Application changed since this page loaded/);
  const rows = findAll(byTest(screen, "conflict-rows"), (n) => n.tag === "tr")
    .slice(1)
    .map((r) => r.children.map(textOf));
  assert.deepEqual(rows, [
    ["sources[board]", JSON.stringify(BOARD_DEF), "(absent)"],
    ["sources[minutes]", "(absent)", JSON.stringify(minutes)],
  ]);
  assert.equal(t.app.state.app?.etag, '"v2"', "the Application as stored now replaced the one read");
  assert.equal(findAll(screen, (n) => n.props.name === "id")[0].props.value, "board", "the draft is offered again");
  assert.equal(t.f.seen.filter((s) => s.method === "GET" && s.url === APP_URL).length, 2, "the Application was reloaded");
  await t.submit("sources", "add-source", BOARD);
  assert.equal(puts(t.f.seen).length, 2);
  assert.equal(puts(t.f.seen)[1].headers.get("If-Match"), '"v2"');
  assert.equal(t.app.state.conflict, null);
  assert.deepEqual(t.stored().sources?.map((s) => s.id), ["hr-policies", "minutes", "board"]);
});

test("a source with a mistake is named on the screen and not sent", async () => {
  const t = harness();
  await t.app.start();
  await t.submit("sources", "add-source", { ...BOARD, id: "Board", managers: "" });
  assert.equal(puts(t.f.seen).length, 0);
  assert.match(textOf(byTest(t.view("sources"), "message")), /id: a lower-case letter.*managers: at least one/);
});

test("removing a source asks first, and a source an assistant reads is not removed", async () => {
  const no = harness({ confirm: false });
  await no.app.start();
  await no.click("sources", "remove-source-hr-policies");
  assert.equal(puts(no.f.seen).length, 0);
  const yes = harness();
  await yes.app.start();
  await yes.click("sources", "remove-source-hr-policies");
  assert.equal(puts(yes.f.seen).length, 0);
  assert.match(textOf(byTest(yes.view("sources"), "message")), /read by assistant staff-helpdesk/);
  await yes.click("assistants", "remove-assistant-staff-helpdesk");
  await yes.click("sources", "remove-source-hr-policies");
  assert.equal(puts(yes.f.seen).length, 2);
  assert.deepEqual(yes.stored().sources, []);
  assert.deepEqual(yes.stored().bindings, params().bindings);
});

test("an upload with readers reaches cortex and the document is listed with its state", async () => {
  const doc = { id: "d1", sourceId: "hr-policies", title: "leave.txt", uri: "", state: "queued", updatedAt: "2026-11-20T10:00:00Z" };
  const t = harness({
    routes: [
      (r) => (r.method === "POST" && r.url.endsWith("/v1/sources/hr-policies/documents") ? json(202, { documentId: "d1", jobId: "j1" }) : undefined),
      (r) => (r.url.endsWith("/v1/documents/d1") ? json(200, doc) : undefined),
    ],
  });
  await t.app.start();
  await t.submit("upload", "upload", { source: "hr-policies", file: new File(["policy"], "leave.txt"), readers: "group:hr\nuser:a@acme.example" });
  const post = t.f.seen.find((s) => s.method === "POST")!;
  assert.deepEqual((post.body as FormData).getAll("reader"), ["group:hr", "user:a@acme.example"]);
  assert.match(textOf(byTest(t.view("documents"), "documents")), /leave\.txthr-policiesqueued/);
  assert.match(textOf(byTest(t.view("documents"), "message")), /document d1, job j1/);
});

test("a job frame lists the job, refetches the sources and the stream is reopened", async () => {
  const frame = { jobId: "j1", sourceId: "hr-policies", documentId: "", kind: "index", state: "done", at: "2026-11-20T10:00:00Z" };
  let sourceCalls = 0;
  const t = harness({
    routes: [
      (r) => (r.url.endsWith("/v1/events") ? new Response(`event: job\ndata: ${JSON.stringify(frame)}\n\n`, { status: 200 }) : undefined),
      (r) => (r.url.endsWith("/v1/sources") ? json(200, { items: [{ id: "hr-policies", title: "HR", state: "idle", documents: ++sourceCalls, chunks: 4, lastSyncAt: null, lastError: null }] }) : undefined),
    ],
  });
  await t.app.start();
  for (let i = 0; i < 100 && t.later.length === 0; i++) await new Promise((r) => setImmediate(r));
  assert.match(textOf(byTest(t.view("jobs"), "jobs")), /indexdonehr-policies/);
  assert.equal(sourceCalls, 2, "the job frame made the screen refetch the sources");
  assert.match(textOf(byTest(t.view("jobs"), "live")), /Not connected/);
  t.later[0]();
  await new Promise((r) => setImmediate(r));
  assert.equal(t.f.seen.filter((s) => s.url.endsWith("/v1/events")).length, 2);
});

test("adding an assistant saves its sources with their tiers, and an answer shows its citations", async () => {
  const t = harness({
    routes: [
      (r) =>
        r.url.endsWith("/v1/chat/completions")
          ? json(200, { choices: [{ message: { content: "Thirty days [1]." } }], citations: [{ index: 1, documentId: "d1", title: "Leave", uri: "", page: 3 }] })
          : undefined,
      (r) => (r.url.endsWith("/v1/documents/d1") ? json(200, { id: "d1", sourceId: "hr-policies", title: "Leave", uri: "", state: "indexed", updatedAt: "2026-11-20T10:00:00Z" }) : undefined),
    ],
  });
  await t.app.start();
  await t.submit("assistants", "add-assistant", { id: "hr-qa", title: "HR questions", instructions: "Cite.", "tier:hr-policies": "secondary", users: "group:hr", topK: "5", rerank: "on" });
  const sent = JSON.parse(String(puts(t.f.seen)[0].body)).parameters as Params;
  assert.deepEqual(sent.assistants?.[1], {
    id: "hr-qa",
    title: "HR questions",
    instructions: "Cite.",
    sources: [{ id: "hr-policies", tier: "secondary" }],
    retrieval: { topK: 5, rerank: true },
    users: ["group:hr"],
  });
  assert.deepEqual(sent.bindings, params().bindings);
  await t.submit("assistants", "ask", { assistant: "staff-helpdesk", question: "How much leave?" });
  const answer = byTest(t.view("assistants"), "answer");
  assert.match(textOf(answer), /Thirty days \[1\]\.\[1\] Leave, page 3/);
  (findAll(byTest(answer, "citations"), (n) => n.tag === "button")[0].props.onclick as () => void)();
  await t.settle();
  assert.equal(t.app.state.tab, "documents");
});

test("a refused token starts the sign-on again", async () => {
  // Every route refuses the token: the three reads and the stream. The sign-on starts once.
  const t = harness({ routes: [() => json(401, { error: "unauthenticated" })] });
  await t.app.start();
  await until("the stream was tried", () => t.f.seen.some((s) => s.url.endsWith("/v1/events")));
  await tick();
  assert.equal(t.signIns(), 1);
  assert.equal(t.later.length, 0, "the stream is not reopened");
  assert.doesNotMatch(t.app.state.message, /unauthenticated/);
});

test("with an Application read without a version, Sources, Upload and Assistants say so by its code, offer nothing that needs it, and nothing is saved", async () => {
  const t = harness({
    routes: [
      (r) => (r.url === APP_URL && r.method === "GET" ? json(200, { parameters: params() }) : undefined),
      (r) => (r.url.endsWith("/v1/sources") ? json(200, { items: [{ id: "hr-policies", title: "HR policies", state: "idle", documents: 3, chunks: 9, lastSyncAt: null, lastError: null }] }) : undefined),
    ],
  });
  assert.equal(textOf(byTest(t.view("sources"), "app-not-loaded")), "The Application is being read. Its sources are not shown and cannot be changed until it is read.");
  await t.app.start();
  const said: [Tab, string][] = [
    ["sources", "Its sources are not shown and cannot be changed until it is read."],
    ["upload", "The upload sources are defined there, so nothing can be uploaded until it is read."],
    ["assistants", "Its assistants are not shown and cannot be changed until it is read."],
  ];
  for (const [tab, consequence] of said) {
    const screen = t.view(tab);
    assert.equal(textOf(byTest(screen, "app-not-loaded")), `The Application could not be read: no-version-from-the-application-route. ${consequence}`);
    assert.doesNotMatch(textOf(screen), /removed|No upload source is defined|not reported yet/, tab);
    const offered = findAll(screen, (n) => /^(sources|assistants|add-source|add-assistant|upload|conflict|remove-.*)$/.test(String(n.props["data-test"] ?? "")));
    assert.deepEqual(offered.map((n) => n.props["data-test"]), [], `${tab}: nothing that needs the Application`);
  }
  assert.equal(byTest(t.view("assistants"), "ask").tag, "form", "asking needs no Application");
  await t.app.actions.addSource(BOARD_DEF);
  assert.equal(puts(t.f.seen).length, 0);
  assert.match(t.app.state.message, /the Application is not loaded/);
});

test("a job frame, its refetches and the stream's reopening keep what is typed in each form, the chosen file, the selection and the focus", async () => {
  const jobs = jobStream();
  let sourceCalls = 0;
  let docCalls = 0;
  let draws = 0;
  const screen = { draw: (): void => {} };
  const t = harness({
    redraw: () => (draws++, screen.draw()),
    routes: [
      jobs.route,
      (r) => (r.url.endsWith("/v1/sources") ? json(200, { items: [{ id: "hr-policies", title: "HR policies", state: "idle", documents: ++sourceCalls, chunks: 0, lastSyncAt: null, lastError: null }] }) : undefined),
      (r) => (r.url.endsWith("/v1/documents/d1") ? json(200, { ...DOC, state: `state-${++docCalls}` }) : undefined),
    ],
  });
  const { doc, root } = page(screen, t);
  await t.app.start();
  await t.app.actions.open("d1"); // on the list, so a frame naming it refetches it
  const file = new File(["policy"], "leave.txt");
  const cases: { tab: Tab; form: string; typed: Record<string, string>; focus: string; around?: () => RegExp }[] = [
    { tab: "sources", form: "add-source", typed: { id: "board", title: "Board papers", managers: "user:sec@acme.example" }, focus: "title", around: () => new RegExp(`idle${sourceCalls}0never`) },
    { tab: "upload", form: "upload", typed: { readers: "group:hr\nuser:a@acme.example" }, focus: "readers" },
    { tab: "documents", form: "search", typed: { query: "parental leave" }, focus: "query", around: () => new RegExp(`state-${docCalls}`) },
    { tab: "assistants", form: "add-assistant", typed: { id: "hr-qa", instructions: "Answer from the HR policies. ".repeat(275) }, focus: "instructions" },
    { tab: "assistants", form: "ask", typed: { question: "How much leave?" }, focus: "question" },
  ];
  const kept = (c: (typeof cases)[number], form: FakeNode, focused: FakeNode, when: string): void => {
    assert.equal(root.one("data-test", c.form), form, `${c.form} ${when}: the same form, on the page`);
    for (const [name, text] of Object.entries(c.typed)) assert.equal(form.one("name", name).value, text, `${c.form} ${when}: ${name}`);
    assert.equal(doc.activeElement, focused, `${c.form} ${when}: the focus`);
    assert.deepEqual([focused.selectionStart, focused.selectionEnd], [3, 9], `${c.form} ${when}: the selection`);
    if (c.tab === "upload") assert.deepEqual(form.one("name", "file").files, [file], `${c.form} ${when}: the chosen file`);
  };
  let last: (() => void) | null = null;
  for (const c of cases) {
    t.app.actions.show(c.tab);
    const form = root.one("data-test", c.form);
    for (const [name, text] of Object.entries(c.typed)) form.one("name", name).value = text;
    if (c.tab === "upload") form.one("name", "file").files = [file];
    const focused = form.one("name", c.focus);
    focused.focus();
    [focused.selectionStart, focused.selectionEnd] = [3, 9];
    const before = draws;
    jobs.send({ jobId: `j-${c.form}` });
    await until(`${c.form}: the frame was listed and both refetches drawn`, () => draws >= before + 3);
    if (c.around) assert.match(root.textContent, c.around(), `${c.form}: the screen around it was redrawn`);
    kept(c, form, focused, "after a job frame");
    last = () => kept(c, form, focused, "after the stream was reopened");
  }
  jobs.end();
  await until("the stream ended", () => t.later.length === 1);
  t.later[0]();
  await until("the stream was reopened and ended", () => jobs.opened() === 2 && t.later.length === 2);
  last!();
});

test("a saved source, a saved assistant and a done upload draw their form afresh; a refused source keeps it", async () => {
  const screen = { draw: (): void => {} };
  const t = harness({
    redraw: () => screen.draw(),
    routes: [
      (r) => (r.method === "POST" && r.url.endsWith("/v1/sources/hr-policies/documents") ? json(202, { documentId: "d1", jobId: "j1" }) : undefined),
      (r) => (r.url.endsWith("/v1/documents/d1") ? json(200, DOC) : undefined),
    ],
  });
  const { root } = page(screen, t);
  await t.app.start();
  const fill = (form: FakeNode, values: Record<string, string>): void => {
    for (const [name, text] of Object.entries(values)) form.one("name", name).value = text;
  };
  t.app.actions.show("sources");
  const refused = root.one("data-test", "add-source");
  fill(refused, { id: "Board", title: "Board papers", managers: "user:sec@acme.example" });
  submitForm(refused);
  await until("the mistake was named", () => /id: a lower-case letter/.test(t.app.state.message));
  assert.equal(root.one("data-test", "add-source"), refused, "a refused source keeps its form");
  assert.equal(refused.one("name", "title").value, "Board papers");
  fill(refused, { id: "board" });
  submitForm(refused);
  await until("the source was saved", () => /Saved: source board/.test(t.app.state.message));
  const fresh = root.one("data-test", "add-source");
  assert.notEqual(fresh, refused);
  assert.deepEqual(["id", "title", "managers"].map((n) => fresh.one("name", n).value), ["", "", ""]);
  t.app.actions.show("assistants");
  const assistant = root.one("data-test", "add-assistant");
  fill(assistant, { id: "hr-qa", title: "HR questions", "tier:hr-policies": "primary", users: "group:hr" });
  submitForm(assistant);
  await until("the assistant was saved", () => /Saved: assistant hr-qa/.test(t.app.state.message));
  assert.notEqual(root.one("data-test", "add-assistant"), assistant);
  assert.equal(root.one("data-test", "add-assistant").one("name", "id").value, "");
  t.app.actions.show("upload");
  const upload = root.one("data-test", "upload");
  fill(upload, { source: "hr-policies" });
  upload.one("name", "file").files = [new File(["policy"], "leave.txt")];
  submitForm(upload);
  await until("the upload was done", () => /Uploaded leave\.txt/.test(t.app.state.message) && !t.app.state.busy);
  assert.notEqual(root.one("data-test", "upload"), upload);
  assert.deepEqual(root.one("data-test", "upload").one("name", "file").files, []);
  assert.equal(puts(t.f.seen).length, 2);
});

test("a job frame whose document refetch is refused 401 starts the sign-on again", async () => {
  const jobs = jobStream();
  let docCalls = 0;
  const t = harness({
    routes: [jobs.route, (r) => (r.url.endsWith("/v1/documents/d1") ? (++docCalls === 1 ? json(200, DOC) : json(401, { error: "unauthenticated" })) : undefined)],
  });
  await t.app.start();
  await t.app.actions.open("d1");
  jobs.send({ documentId: "d1" });
  await until("the sign-on started", () => t.signIns() > 0);
  assert.equal(docCalls, 2);
  assert.equal(t.signIns(), 1);
  assert.doesNotMatch(t.app.state.message, /unauthenticated/);
});

test("a job frame whose document refetch fails 503 keeps the document and says why; a 404 takes it off the list", async () => {
  const jobs = jobStream();
  const answers = [json(200, DOC), json(503, { error: "unavailable", detail: "try later" }), json(404, { error: "document-not-found" })];
  const t = harness({ routes: [jobs.route, (r) => (r.url.endsWith("/v1/documents/d1") ? answers.shift() : undefined)] });
  await t.app.start();
  await t.app.actions.open("d1");
  jobs.send({ documentId: "d1" });
  await until("the refusal was shown", () => /unavailable/.test(t.app.state.message));
  assert.equal(t.app.state.message, "Refused: unavailable: try later");
  assert.deepEqual(t.app.state.documents, [DOC], "a refusal that may pass keeps the document as it was");
  jobs.send({ documentId: "d1", kind: "delete", state: "done" });
  await until("the deleted document left the list", () => t.app.state.documents.length === 0);
  assert.equal(answers.length, 0);
  assert.equal(t.signIns(), 0);
});
