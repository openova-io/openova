// Documents: search, the documents this page has seen (uploaded, opened or named by a job), and
// for each its state, the original and deletion. cortex has no route that lists every document.

import { h, type VNode } from "../dom.js";
import type { Actions, State } from "../state.js";
import { field, onSubmit, table, text, when } from "./parts.js";

export function documentsView(s: State, act: Actions): VNode {
  const hits = s.hits.map((x) => [
    x.title,
    x.sourceId,
    x.chunk.page === null ? "" : `page ${x.chunk.page}`,
    x.chunk.score.toFixed(2),
    x.chunk.text,
    h("button", { type: "button", onclick: () => void act.open(x.documentId) }, "Open"),
  ]);
  const docs = s.documents.map((d) => [
    d.title,
    d.sourceId,
    d.state,
    when(d.updatedAt),
    h("button", { type: "button", "data-test": `download-${d.id}`, onclick: () => void act.download(d.id) }, "Original"),
    h("button", { type: "button", "data-test": `delete-${d.id}`, disabled: s.busy, onclick: () => void act.remove(d.id) }, "Delete"),
  ]);
  return h(
    "section",
    { "data-test": "screen-documents" },
    h("h2", null, "Documents"),
    h(
      "form",
      { "data-test": "search", onsubmit: onSubmit((f) => void act.search(text(f, "query"))) },
      field("Search", h("input", { name: "query", required: true, maxlength: 2000 })),
      h("button", { type: "submit", disabled: s.busy }, "Search"),
    ),
    hits.length > 0 ? table("hits", ["Document", "Source", "Page", "Score", "Passage", ""], hits) : null,
    h("h3", null, "Documents on this page"),
    docs.length > 0
      ? table("documents", ["Title", "Source", "State", "Updated", "", ""], docs)
      : h("p", { "data-test": "no-documents" }, "None yet: upload one, open one from a search, or open one by its id."),
    h(
      "form",
      { "data-test": "open-document", onsubmit: onSubmit((f) => void act.open(text(f, "id"))) },
      field("Document id", h("input", { name: "id", required: true })),
      h("button", { type: "submit" }, "Open"),
    ),
  );
}
