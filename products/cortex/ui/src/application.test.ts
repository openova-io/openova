import assert from "node:assert/strict";
import { test } from "node:test";
import { Applications, Conflict } from "./application.js";
import { APP_URL, CFG, fakeFetch, json, params } from "./fakes.js";
import { ApiError } from "./http.js";
import { withSource, type Params, type SourceDef } from "./params.js";

const token = (): string => "user-token";
const NEW: SourceDef = { id: "board", title: "Board papers", connector: { type: "upload" }, access: { readers: ["group:board"], managers: ["user:sec@acme.example"] } };

test("load reads the parameters and the version of the Application route", async () => {
  const f = fakeFetch(() => json(200, { name: "cortex", parameters: params() }, { ETag: '"v1"' }));
  const loaded = await new Applications(CFG, token, f.fetch).load();
  assert.equal(f.seen[0].url, APP_URL);
  assert.equal(f.seen[0].headers.get("Authorization"), "Bearer user-token");
  assert.equal(loaded.etag, '"v1"');
  assert.deepEqual(loaded.parameters, params());
});

test("load refuses an Application route that returns no version, so no save can overwrite another", async () => {
  const f = fakeFetch(() => json(200, { parameters: params() }));
  await assert.rejects(new Applications(CFG, token, f.fetch).load(), (e) => e instanceof ApiError && e.code === "no-version-from-the-application-route");
});

test("load refuses an Application read without parameters, so no save can replace them", async () => {
  const f = fakeFetch(() => json(200, { name: "cortex" }, { ETag: '"v1"' }));
  await assert.rejects(new Applications(CFG, token, f.fetch).load(), (e) => e instanceof ApiError && e.code === "no-parameters-from-the-application-route");
  assert.equal(f.seen.length, 1);
});

test("save sends the whole parameters with If-Match to the Application update route", async () => {
  const f = fakeFetch((_, n) => (n === 0 ? json(200, {}) : json(200, { parameters: JSON.parse(String(f.seen[0].body)).parameters }, { ETag: '"v2"' })));
  const apps = new Applications(CFG, token, f.fetch);
  const saved = await apps.save({ parameters: params(), etag: '"v1"' }, (p) => withSource(p, NEW));
  const put = f.seen[0];
  assert.equal(put.method, "PUT");
  assert.equal(put.url, APP_URL);
  assert.equal(put.headers.get("If-Match"), '"v1"');
  assert.equal(put.headers.get("Authorization"), "Bearer user-token");
  const sent = JSON.parse(String(put.body)) as { parameters: Params };
  assert.deepEqual(Object.keys(sent), ["parameters"]);
  assert.deepEqual(sent.parameters, { ...params(), sources: [...(params().sources ?? []), NEW] });
  assert.equal(f.seen[1].method, "GET");
  assert.equal(saved.etag, '"v2"');
});

test("save sends bindings back unchanged, also when the edit touched them", async () => {
  const f = fakeFetch((_, n) => (n === 0 ? json(200, {}) : json(200, { parameters: params() }, { ETag: '"v2"' })));
  const edit = (p: Params): Params => ({ ...withSource(p, NEW), bindings: { db: { secret: "other" } } });
  await new Applications(CFG, token, f.fetch).save({ parameters: params(), etag: '"v1"' }, edit);
  assert.deepEqual(JSON.parse(String(f.seen[0].body)).parameters.bindings, params().bindings);
});

test("save adds no bindings to an Application that has none", async () => {
  const f = fakeFetch((_, n) => (n === 0 ? json(200, {}) : json(200, { parameters: {} }, { ETag: '"v2"' })));
  const read = { parameters: { models: params().models }, etag: '"v1"' };
  await new Applications(CFG, token, f.fetch).save(read, (p) => ({ ...p, bindings: { x: 1 } }));
  assert.equal("bindings" in JSON.parse(String(f.seen[0].body)).parameters, false);
});

for (const status of [409, 412]) {
  test(`save answered ${status} reloads the Application and names the difference`, async () => {
    const stored = params();
    stored.sources = [{ ...stored.sources![0], title: "HR rules" }];
    const f = fakeFetch((_, n) => (n === 0 ? json(status, { error: status === 409 ? "conflict" : "precondition-failed" }) : json(200, { parameters: stored }, { ETag: '"v3"' })));
    const err = await new Applications(CFG, token, f.fetch).save({ parameters: params(), etag: '"v1"' }, (p) => withSource(p, NEW)).catch((e: unknown) => e);
    assert.ok(err instanceof Conflict);
    assert.equal(f.seen[1].method, "GET");
    assert.equal(err.current.etag, '"v3"');
    assert.deepEqual(err.current.parameters, stored);
    assert.deepEqual(err.differences, [
      { path: "sources[hr-policies].title", yours: "HR policies", stored: "HR rules" },
      { path: "sources[board]", yours: NEW, stored: undefined },
    ]);
  });
}

test("save passes any other refusal on with its code and detail", async () => {
  const f = fakeFetch(() => json(422, { error: "schema-invalid", detail: "sources[1].id: pattern" }));
  const err = await new Applications(CFG, token, f.fetch).save({ parameters: params(), etag: '"v1"' }, (p) => p).catch((e: unknown) => e);
  assert.ok(err instanceof ApiError);
  assert.equal(err.code, "schema-invalid");
  assert.equal(err.detail, "sources[1].id: pattern");
  assert.equal(f.seen.length, 1);
});

test("a 409 that is not a version conflict is passed on, with no reload", async () => {
  const f = fakeFetch(() => json(409, { error: "managed-by-git", detail: "owned by HelmRelease cortex" }));
  const err = await new Applications(CFG, token, f.fetch).save({ parameters: params(), etag: '"v1"' }, (p) => p).catch((e: unknown) => e);
  assert.ok(err instanceof ApiError && !(err instanceof Conflict));
  assert.equal(err.code, "managed-by-git");
  assert.equal(f.seen.length, 1);
});
