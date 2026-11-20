// Sources: the definitions of the Application beside the state cortex reports, and the form that
// adds an upload source. Adding and removing are saves of the Application.

import { h, type VNode } from "../dom.js";
import { principals, type SourceDef } from "../params.js";
import type { Actions, State } from "../state.js";
import { conflictPanel, field, notLoaded, onSubmit, table, text, when } from "./parts.js";

export function sourceFromForm(form: FormData): SourceDef {
  return {
    id: text(form, "id"),
    title: text(form, "title"),
    connector: { type: "upload" },
    parse: { profile: text(form, "profile") },
    access: { readers: principals(text(form, "readers")), managers: principals(text(form, "managers")) },
  };
}

export function sourcesView(s: State, act: Actions): VNode {
  if (!s.app) {
    return h("section", { "data-test": "screen-sources" }, h("h2", null, "Sources"), notLoaded(s, "Its sources are not shown and cannot be changed until it is read."));
  }
  const defs = s.app.parameters.sources ?? []; // no sources key in the parameters read: none is defined
  const status = new Map(s.sources.map((x) => [x.id, x]));
  const ids = [...new Set([...defs.map((d) => d.id), ...s.sources.map((x) => x.id)])];
  const rows = ids.map((id) => {
    const def = defs.find((d) => d.id === id);
    const st = status.get(id);
    return [
      id,
      def?.title ?? st?.title ?? "",
      def ? def.access.readers.join(", ") : "",
      def ? def.access.managers.join(", ") : "",
      st ? st.state : "not reported yet",
      st ? st.documents : "",
      st ? st.chunks : "",
      st ? when(st.lastSyncAt) : "",
      st?.lastError ?? "",
      def ? h("button", { type: "button", "data-test": `remove-source-${id}`, disabled: s.busy, onclick: () => void act.removeSource(id) }, "Remove") : "removed",
    ];
  });
  const d = s.draftSource;
  return h(
    "section",
    { "data-test": "screen-sources" },
    h("h2", null, "Sources"),
    conflictPanel(s.conflict),
    table("sources", ["Source", "Title", "Readers", "Managers", "State", "Documents", "Chunks", "Last sync", "Last error", ""], rows),
    h(
      "form",
      { "data-test": "add-source", key: `add-source#${s.done["add-source"]}`, onsubmit: onSubmit((f) => void act.addSource(sourceFromForm(f))) },
      h("h3", null, "Add an upload source"),
      field("Id", h("input", { name: "id", required: true, value: d?.id ?? "" }), "a lower-case letter, then letters, digits or dashes; never reused"),
      field("Title", h("input", { name: "title", required: true, value: d?.title ?? "" })),
      field("Readers", h("textarea", { name: "readers", value: d?.access.readers.join("\n") ?? "org:members" }), "one per line: user:, group:, agent: or org:members"),
      field("Managers", h("textarea", { name: "managers", required: true, value: d?.access.managers.join("\n") ?? "" }), "one per line: user: or group:"),
      field(
        "Parser",
        h("select", { name: "profile", value: d?.parse?.profile ?? "documents" }, h("option", { value: "documents" }, "documents"), h("option", { value: "text" }, "text")),
      ),
      h("button", { type: "submit", disabled: s.busy }, "Save to the Application"),
    ),
  );
}
