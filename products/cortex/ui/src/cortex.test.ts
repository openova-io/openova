import assert from "node:assert/strict";
import { test } from "node:test";
import { Cortex } from "./cortex.js";
import { parseFrames, readJobs, type JobEvent } from "./events.js";
import { fakeFetch, json } from "./fakes.js";
import { ApiError } from "./http.js";

const BASE = "https://cortex.acme.example";
const token = (): string => "user-token";

test("upload posts one file part and one reader part per principal, with the user's token", async () => {
  const f = fakeFetch(() => json(202, { documentId: "d1", jobId: "j1" }));
  const res = await new Cortex(BASE, token, f.fetch).upload("hr-policies", new Blob(["text"]), "leave.txt", ["group:hr", "user:a@acme.example"]);
  assert.deepEqual(res, { documentId: "d1", jobId: "j1" });
  const req = f.seen[0];
  assert.equal(req.method, "POST");
  assert.equal(req.url, `${BASE}/v1/sources/hr-policies/documents`);
  assert.equal(req.headers.get("Authorization"), "Bearer user-token");
  const form = req.body as FormData;
  assert.deepEqual(form.getAll("reader"), ["group:hr", "user:a@acme.example"]);
  assert.equal((form.get("file") as File).name, "leave.txt");
  assert.deepEqual([...form.keys()].sort(), ["file", "reader", "reader"]);
});

test("upload without readers sends no reader part, so the document has the readers of its source", async () => {
  const f = fakeFetch(() => json(202, { documentId: "d1", jobId: "j1" }));
  await new Cortex(BASE, token, f.fetch).upload("hr-policies", new Blob(["x"]), "a.txt", []);
  assert.deepEqual([...(f.seen[0].body as FormData).keys()], ["file"]);
});

test("a refusal of cortex carries its error code", async () => {
  const f = fakeFetch(() => json(403, { error: "not-manager" }));
  await assert.rejects(new Cortex(BASE, token, f.fetch).upload("hr-policies", new Blob(["x"]), "a.txt", []), (e) => e instanceof ApiError && e.code === "not-manager" && e.status === 403);
});

test("the read routes: sources, one document, its original, deletion and search", async () => {
  const f = fakeFetch((req) => {
    if (req.url.endsWith("/v1/sources")) return json(200, { items: [{ id: "hr-policies", title: "HR", state: "idle", documents: 2, chunks: 9, lastSyncAt: null, lastError: null }] });
    if (req.url.endsWith("/content")) return new Response("bytes", { status: 200 });
    if (req.method === "DELETE") return json(202, { jobId: "j9" });
    if (req.url.endsWith("/v1/search")) return json(200, { results: [{ documentId: "d1", sourceId: "hr-policies", title: "Leave", uri: "", chunk: { id: 1, text: "t", page: 2, score: 0.9 } }] });
    return json(200, { id: "d 1", sourceId: "hr-policies", title: "Leave", uri: "", state: "indexed", updatedAt: "2026-11-20T10:00:00Z" });
  });
  const c = new Cortex(BASE, token, f.fetch);
  assert.equal((await c.sources())[0].chunks, 9);
  assert.equal((await c.document("d 1")).state, "indexed");
  assert.equal(f.seen[1].url, `${BASE}/v1/documents/d%201`);
  assert.equal(await (await c.content("d1")).text(), "bytes");
  assert.deepEqual(await c.remove("d1"), { jobId: "j9" });
  assert.equal(f.seen[3].method, "DELETE");
  assert.equal((await c.search("leave"))[0].chunk.page, 2);
  assert.deepEqual(JSON.parse(String(f.seen[4].body)), { query: "leave" });
});

test("models lists the assistants the user may call, ask returns the answer with its citations", async () => {
  const f = fakeFetch((req) =>
    req.url.endsWith("/v1/models")
      ? json(200, { object: "list", data: [{ id: "staff-helpdesk", object: "model", owned_by: "cortex" }] })
      : json(200, {
          choices: [{ index: 0, message: { role: "assistant", content: "Thirty days [1]." } }],
          citations: [{ index: 1, documentId: "d1", title: "Leave", uri: "", page: 3 }],
        }),
  );
  const c = new Cortex(BASE, token, f.fetch);
  assert.deepEqual(await c.models(), ["staff-helpdesk"]);
  const a = await c.ask("staff-helpdesk", "How much leave?");
  assert.deepEqual(a, { content: "Thirty days [1].", citations: [{ index: 1, documentId: "d1", title: "Leave", uri: "", page: 3 }] });
  assert.deepEqual(JSON.parse(String(f.seen[1].body)), { model: "staff-helpdesk", messages: [{ role: "user", content: "How much leave?" }] });
});

test("an answer refused for residency names external-models-not-allowed", async () => {
  const f = fakeFetch(() => json(409, { error: "external-models-not-allowed" }));
  await assert.rejects(new Cortex(BASE, token, f.fetch).ask("staff-helpdesk", "q"), (e) => e instanceof ApiError && e.code === "external-models-not-allowed");
});

test("parseFrames takes complete frames, skips comments and keeps the rest", () => {
  const { frames, rest } = parseFrames(': ping\n\nevent: job\ndata: {"jobId":"j1"}\n\nevent: job\r\ndata: {"jo');
  assert.deepEqual(frames, [{ event: "job", data: '{"jobId":"j1"}' }]);
  assert.equal(rest, 'event: job\ndata: {"jo');
});

test("the job stream is read across chunks that split a frame", async () => {
  const job: JobEvent = { jobId: "j1", sourceId: "hr-policies", documentId: "d1", kind: "index", state: "done", at: "2026-11-20T10:00:00Z" };
  const text = `: ping\n\nevent: job\ndata: ${JSON.stringify(job)}\n\nevent: other\ndata: {}\n\n`;
  const enc = new TextEncoder();
  const body = new ReadableStream<Uint8Array>({
    start(c) {
      c.enqueue(enc.encode(text.slice(0, 30)));
      c.enqueue(enc.encode(text.slice(30)));
      c.close();
    },
  });
  const seen: JobEvent[] = [];
  await readJobs(body, (e) => seen.push(e));
  assert.deepEqual(seen, [job]);
});

test("the job stream keeps a CRLF line end that a chunk splits between CR and LF", async () => {
  const job: JobEvent = { jobId: "j1", sourceId: "hr-policies", documentId: "d1", kind: "index", state: "done", at: "2026-11-20T10:00:00Z" };
  const text = `event: job\r\ndata: ${JSON.stringify(job)}\r\n\r\n`;
  const enc = new TextEncoder();
  const cuts = [text.indexOf("\r") + 1, text.lastIndexOf("\r")]; // after the first CR, and between the last CR and its LF
  const body = new ReadableStream<Uint8Array>({
    start(c) {
      c.enqueue(enc.encode(text.slice(0, cuts[0])));
      c.enqueue(enc.encode(text.slice(cuts[0], cuts[1])));
      c.enqueue(enc.encode(text.slice(cuts[1])));
      c.close();
    },
  });
  const seen: JobEvent[] = [];
  await readJobs(body, (e) => seen.push(e));
  assert.deepEqual(seen, [job]);
});
