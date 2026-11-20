// Assistants: the definitions of the Application, the form that adds one (a save of the
// Application), and a question to an assistant this user may call, answered with citations.

import { h, type Child, type VNode } from "../dom.js";
import type { Loaded } from "../application.js";
import { principals, TIERS, type AssistantDef, type SourceDef } from "../params.js";
import type { Actions, State } from "../state.js";
import { conflictPanel, field, notLoaded, onSubmit, table, text } from "./parts.js";

export function assistantFromForm(form: FormData, sources: SourceDef[]): AssistantDef {
  const topK = text(form, "topK");
  return {
    id: text(form, "id"),
    title: text(form, "title"),
    instructions: text(form, "instructions"),
    sources: sources.flatMap((s) => {
      const tier = text(form, `tier:${s.id}`);
      return tier === "" ? [] : [{ id: s.id, tier }];
    }),
    retrieval: { ...(topK === "" ? {} : { topK: Number(topK) }), rerank: form.get("rerank") === "on" },
    users: principals(text(form, "users")),
  };
}

// definitions: the assistants of the Application read, and the form that adds one.
function definitions(s: State, app: Loaded, act: Actions): Child[] {
  // A key absent from the parameters read: none is defined.
  const sources = app.parameters.sources ?? [];
  const defs = app.parameters.assistants ?? [];
  const d = s.draftAssistant;
  const tierOf = (id: string): string => d?.sources.find((x) => x.id === id)?.tier ?? "";
  const rows = defs.map((a) => [
    a.id,
    a.title,
    a.sources.map((x) => `${x.id} (${x.tier ?? "primary"})`).join(", "),
    a.users.join(", "),
    h("button", { type: "button", "data-test": `remove-assistant-${a.id}`, disabled: s.busy, onclick: () => void act.removeAssistant(a.id) }, "Remove"),
  ]);
  return [
    conflictPanel(s.conflict),
    table("assistants", ["Assistant", "Title", "Sources (tier)", "Users", ""], rows),
    h(
      "form",
      { "data-test": "add-assistant", key: `add-assistant#${s.done["add-assistant"]}`, onsubmit: onSubmit((f) => void act.addAssistant(assistantFromForm(f, sources))) },
      h("h3", null, "Add an assistant"),
      field("Id", h("input", { name: "id", required: true, value: d?.id ?? "" })),
      field("Title", h("input", { name: "title", required: true, value: d?.title ?? "" })),
      field("Instructions", h("textarea", { name: "instructions", maxlength: 8000, value: d?.instructions ?? "" })),
      sources.map((src) =>
        field(
          `Source ${src.title}`,
          h("select", { name: `tier:${src.id}`, value: tierOf(src.id) }, h("option", { value: "" }, "not used"), TIERS.map((t) => h("option", { value: t }, t))),
        ),
      ),
      field("Users", h("textarea", { name: "users", value: d?.users.join("\n") ?? "org:members" }), "who may call it, one per line; it never widens what a user may read"),
      field("Passages", h("input", { type: "number", name: "topK", min: 1, max: 50, value: d?.retrieval?.topK ?? "" }), "empty: 8"),
      field("Rerank", h("input", { type: "checkbox", name: "rerank", checked: d?.retrieval?.rerank ?? true })),
      h("button", { type: "submit", disabled: s.busy }, "Save to the Application"),
    ),
  ];
}

// ask: the question form, from the assistants cortex lists for this user; it needs no Application.
function ask(s: State, act: Actions): VNode {
  if (s.models === null) {
    const why = s.unread.models === undefined ? "are being read." : `could not be read: ${s.unread.models}.`;
    return h("p", { "data-test": "models-not-read" }, `The assistants open to you ${why}`);
  }
  if (s.models.length === 0) return h("p", { "data-test": "no-models" }, "No assistant is open to you.");
  return h(
    "form",
    { "data-test": "ask", onsubmit: onSubmit((f) => void act.ask(text(f, "assistant"), text(f, "question"))) },
    field("Assistant", h("select", { name: "assistant" }, s.models.map((m) => h("option", { value: m }, m)))),
    field("Question", h("textarea", { name: "question", required: true })),
    h("button", { type: "submit", disabled: s.busy }, "Ask"),
  );
}

export function assistantsView(s: State, act: Actions): VNode {
  const answer = s.answer;
  return h(
    "section",
    { "data-test": "screen-assistants" },
    h("h2", null, "Assistants"),
    s.app ? definitions(s, s.app, act) : notLoaded(s, "Its assistants are not shown and cannot be changed until it is read."),
    h("h3", null, "Ask"),
    ask(s, act),
    answer
      ? h(
          "article",
          { "data-test": "answer" },
          h("p", null, answer.content),
          h(
            "ol",
            { "data-test": "citations" },
            answer.citations.map((c) =>
              h(
                "li",
                null,
                `[${c.index}] ${c.title}${c.page === null ? "" : `, page ${c.page}`} `,
                h("button", { type: "button", onclick: () => void act.open(c.documentId) }, "Open"),
              ),
            ),
          ),
        )
      : null,
  );
}
