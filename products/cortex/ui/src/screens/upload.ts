// Upload: one file into an upload source, with an optional reader list that narrows who may read
// the document. The source's managers always read it.

import { h, type VNode } from "../dom.js";
import { principals } from "../params.js";
import type { Actions, State } from "../state.js";
import { field, notLoaded, onSubmit, text } from "./parts.js";

export function uploadFromForm(form: FormData): { sourceId: string; file: File | null; readers: string[] } {
  const file = form.get("file");
  return {
    sourceId: text(form, "source"),
    file: file instanceof File && file.name !== "" ? file : null,
    readers: principals(text(form, "readers")),
  };
}

export function uploadView(s: State, act: Actions): VNode {
  if (!s.app) {
    return h("section", { "data-test": "screen-upload" }, h("h2", null, "Upload"), notLoaded(s, "The upload sources are defined there, so nothing can be uploaded until it is read."));
  }
  const uploads = (s.app.parameters.sources ?? []).filter((d) => d.connector.type === "upload");
  // configSchema: policy.maxUploadMb defaults to 50.
  const policy = s.app.parameters.policy as { maxUploadMb?: number } | undefined;
  const limit = policy?.maxUploadMb ?? 50;
  const submit = onSubmit((f) => {
    const u = uploadFromForm(f);
    if (u.file) void act.upload(u.sourceId, u.file, u.readers);
  });
  return h(
    "section",
    { "data-test": "screen-upload" },
    h("h2", null, "Upload"),
    uploads.length === 0
      ? h("p", { "data-test": "no-upload-source" }, "No upload source is defined. Add one under Sources.")
      : h(
          "form",
          // A redraw keeps this form and its chosen file (a script cannot choose a file again); a done upload draws it afresh.
          { "data-test": "upload", key: `upload#${s.done.upload}`, onsubmit: submit },
          field("Source", h("select", { name: "source", required: true }, uploads.map((d) => h("option", { value: d.id }, `${d.title} (${d.id})`)))),
          field("File", h("input", { type: "file", name: "file", required: true }), `at most ${limit} MB; the type is decided by the content`),
          field("Readers", h("textarea", { name: "readers" }), "optional, one per line; empty: the readers of the source"),
          h("button", { type: "submit", disabled: s.busy }, "Upload"),
        ),
  );
}
