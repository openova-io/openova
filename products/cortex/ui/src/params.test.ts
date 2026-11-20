import assert from "node:assert/strict";
import { test } from "node:test";
import { params } from "./fakes.js";
import {
  assistantProblems,
  assistantsUsing,
  diff,
  principals,
  sourceProblems,
  withAssistant,
  withoutAssistant,
  withoutSource,
  withSource,
  type AssistantDef,
  type SourceDef,
} from "./params.js";

const source = (o: Partial<SourceDef> = {}): SourceDef => ({
  id: "board",
  title: "Board papers",
  connector: { type: "upload" },
  parse: { profile: "documents" },
  access: { readers: ["group:board"], managers: ["user:sec@acme.example"] },
  ...o,
});
const assistant = (o: Partial<AssistantDef> = {}): AssistantDef => ({
  id: "board-qa",
  title: "Board questions",
  instructions: "Cite.",
  sources: [{ id: "hr-policies", tier: "primary" }],
  users: ["group:board"],
  ...o,
});

test("principals takes one principal per line or comma", () => {
  assert.deepEqual(principals(" user:a@x.example,\ngroup:b \n\n org:members "), ["user:a@x.example", "group:b", "org:members"]);
});

test("a source is checked against the configSchema before the save", () => {
  assert.deepEqual(sourceProblems(source(), params()), []);
  assert.deepEqual(sourceProblems(source({ id: "Board" }), params()), ["id: a lower-case letter, then 1 to 40 letters, digits or dashes"]);
  assert.deepEqual(sourceProblems(source({ id: "hr-policies" }), params()), ["id: a source hr-policies exists"]);
  assert.deepEqual(sourceProblems(source({ title: " " }), params()), ["title: 1 to 120 characters"]);
  assert.deepEqual(sourceProblems(source({ parse: { profile: "ocr" } }), params()), ["parser: one of documents, text"]);
  assert.deepEqual(sourceProblems(source({ access: { readers: ["everyone"], managers: [] } }), params()), [
    "managers: at least one",
    'reader "everyone" is not a principal of that form',
  ]);
  assert.deepEqual(sourceProblems(source({ access: { readers: [], managers: ["agent:acme/bot"] } }), params()), [
    'manager "agent:acme/bot" is not a principal of that form',
  ]);
});

test("an assistant is checked against the configSchema and the defined sources", () => {
  assert.deepEqual(assistantProblems(assistant(), params()), []);
  assert.deepEqual(assistantProblems(assistant({ id: "staff-helpdesk" }), params()), ["id: an assistant staff-helpdesk exists"]);
  assert.deepEqual(assistantProblems(assistant({ sources: [] }), params()), ["sources: at least one"]);
  assert.deepEqual(assistantProblems(assistant({ sources: [{ id: "nope", tier: "best" }] }), params()), [
    "source nope is not defined",
    "tier best is not one of primary, secondary, fallback",
  ]);
  assert.deepEqual(assistantProblems(assistant({ retrieval: { topK: 51 } }), params()), ["topK: a whole number from 1 to 50"]);
  assert.deepEqual(assistantProblems(assistant({ instructions: "x".repeat(8001), users: ["all"] }), params()), [
    "instructions: at most 8000 characters",
    'user "all" is not a principal of that form',
  ]);
});

test("the edits add and remove one entry and leave the rest of the parameters as read", () => {
  const p = params();
  const added = withSource(p, source());
  assert.deepEqual(added.sources?.map((s) => s.id), ["hr-policies", "board"]);
  assert.deepEqual(added.bindings, p.bindings);
  assert.deepEqual(p.sources?.length, 1, "the parameters read are not changed");
  assert.deepEqual(withoutSource(added, "board").sources?.map((s) => s.id), ["hr-policies"]);
  const two = withAssistant(p, assistant());
  assert.deepEqual(two.assistants?.map((a) => a.id), ["staff-helpdesk", "board-qa"]);
  assert.deepEqual(withoutAssistant(two, "staff-helpdesk").assistants?.map((a) => a.id), ["board-qa"]);
});

test("a source an assistant reads is not removed", () => {
  assert.deepEqual(assistantsUsing(params(), "hr-policies"), ["staff-helpdesk"]);
  assert.throws(() => withoutSource(params(), "hr-policies"), /read by assistant staff-helpdesk/);
});

test("diff names each leaf that differs, lists of definitions by id", () => {
  const a = params();
  const b = params();
  b.sources = [{ ...b.sources![0], access: { readers: ["group:hr"], managers: ["group:hr-admins"] } }];
  b.policy = { allowExternalModels: true };
  b.assistants = [];
  assert.deepEqual(diff(a, b), [
    { path: "assistants[staff-helpdesk]", yours: a.assistants![0], stored: undefined },
    { path: "policy", yours: undefined, stored: { allowExternalModels: true } },
    { path: "sources[hr-policies].access.readers", yours: ["org:members"], stored: ["group:hr"] },
  ]);
  assert.deepEqual(diff(params(), params()), []);
  assert.deepEqual(diff({ x: { a: 1, b: 2 } }, { x: { b: 2, a: 1 } }), [], "the order of keys is no difference");
});
