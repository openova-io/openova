// Pieces the screens share.

import { h, type Child, type Handler, type VNode } from "../dom.js";
import type { Difference } from "../params.js";
import type { State } from "../state.js";

export function table(test: string, head: string[], rows: Child[][]): VNode {
  return h(
    "table",
    { "data-test": test },
    h("thead", null, h("tr", null, head.map((c) => h("th", null, c)))),
    h("tbody", null, rows.map((r) => h("tr", null, r.map((c) => h("td", null, c))))),
  );
}

// field is keyed by its input's name, so a redraw matches it to its own label even when a field
// before it comes or goes.
export function field(label: string, input: VNode, hint = ""): VNode {
  const key = typeof input.props.name === "string" ? input.props.name : null;
  return h("label", { key }, h("span", null, label), input, hint ? h("small", null, hint) : null);
}

// formData reads the submitted form. A test hands the FormData itself as the event's target.
function formData(target: EventTarget | null): FormData {
  return target instanceof FormData ? target : new FormData(target as HTMLFormElement);
}

export function onSubmit(handle: (form: FormData) => void): Handler {
  return (e: Event) => {
    e.preventDefault();
    handle(formData(e.currentTarget));
  };
}

export function text(form: FormData, name: string): string {
  const v = form.get(name);
  return typeof v === "string" ? v.trim() : "";
}

export function when(at: string | null): string {
  return at ? new Date(at).toISOString().replace("T", " ").slice(0, 19) : "never";
}

function shown(v: unknown): string {
  return v === undefined ? "(absent)" : JSON.stringify(v);
}

// conflictPanel shows where the refused save and the stored Application differ.
export function conflictPanel(differences: Difference[] | null): VNode | null {
  if (!differences) return null;
  return h(
    "section",
    { class: "conflict", "data-test": "conflict" },
    h("h3", null, "The Application changed since this page loaded"),
    h("p", null, "It was reloaded. Where your save and the stored Application differ is listed below; check it and save again."),
    table(
      "conflict-rows",
      ["Path", "Your save", "Now stored"],
      differences.map((d) => [d.path, shown(d.yours), shown(d.stored)]),
    ),
  );
}

// notLoaded is what a screen that shows the Application's definitions says while it has none:
// that the Application was not read, by the code its route gave, or is still being read.
export function notLoaded(s: State, consequence: string): VNode {
  const why = s.unread.app === undefined ? "The Application is being read." : `The Application could not be read: ${s.unread.app}.`;
  return h("p", { "data-test": "app-not-loaded" }, `${why} ${consequence}`);
}
