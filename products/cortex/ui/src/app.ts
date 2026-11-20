// The state of the screens and the actions they call. Every change of a definition is a save of
// the Application (application.ts); documents and questions go to cortex; job changes arrive on
// cortex's event stream and make the screens refetch what they show.

import { Conflict, type Applications } from "./application.js";
import type { Cortex } from "./cortex.js";
import type { JobEvent } from "./events.js";
import { ApiError, describe } from "./http.js";
import {
  assistantProblems,
  READER,
  sourceProblems,
  withAssistant,
  withoutAssistant,
  withoutSource,
  withSource,
  type Params,
} from "./params.js";
import { emptyState, type Actions, type State } from "./state.js";

export interface Deps {
  cortex: Cortex;
  applications: Applications;
  redraw: () => void;
  confirm: (question: string) => boolean;
  save: (content: Blob, name: string) => void; // hands a file to the browser
  later: (work: () => void, ms: number) => void;
  signIn: () => void; // the token was refused: sign on again
}

const REOPEN_MS = 5000; // the server closes a stream after some minutes; it is reopened after this
const JOBS_KEPT = 200;

export function createApp(d: Deps): { state: State; actions: Actions; start(): Promise<void> } {
  const state = emptyState();

  // failure returns the line to show, or null when the token was refused and sign-on restarts.
  // Sign-on starts once, however many calls the refused token fails: each start writes a new state.
  let signingIn = false;
  const failure = (err: unknown): string | null => {
    if (err instanceof ApiError && err.status === 401) {
      if (!signingIn) d.signIn();
      signingIn = true;
      return null;
    }
    return describe(err);
  };

  const run = async (work: () => Promise<void>): Promise<void> => {
    state.busy = true;
    state.message = "";
    d.redraw();
    try {
      await work();
    } catch (err) {
      state.message = failure(err) ?? "";
    } finally {
      state.busy = false;
      d.redraw();
    }
  };

  // change saves edit to the Application. On a conflict the Application is replaced by the one
  // stored now and the differences are shown; the draft stays so the form offers it again.
  const change = async (edit: (p: Params) => Params, done: string): Promise<boolean> => {
    if (!state.app) throw new Error("the Application is not loaded, so nothing can be saved");
    try {
      state.app = await d.applications.save(state.app, edit);
    } catch (err) {
      if (!(err instanceof Conflict)) throw err;
      state.app = err.current;
      state.conflict = err.differences;
      state.message = "Not saved: the Application changed since it was read.";
      return false;
    }
    state.conflict = null;
    state.message = done;
    return true;
  };

  let refreshing = false;
  let again = false;
  const refreshSources = async (): Promise<void> => {
    if (refreshing) {
      again = true;
      return;
    }
    refreshing = true;
    try {
      do {
        again = false;
        state.sources = await d.cortex.sources();
      } while (again);
    } finally {
      refreshing = false;
      d.redraw();
    }
  };

  const remember = async (id: string): Promise<void> => {
    const doc = await d.cortex.document(id);
    state.documents = [doc, ...state.documents.filter((x) => x.id !== id)];
  };

  const onJob = (e: JobEvent): void => {
    state.jobs = [e, ...state.jobs].slice(0, JOBS_KEPT);
    d.redraw();
    void refreshSources().catch((err) => (state.message = failure(err) ?? ""));
    const id = e.documentId;
    if (id && state.documents.some((x) => x.id === id)) {
      void remember(id)
        .catch((err) => {
          // 404: deleted, or no longer readable by this user, so it leaves the list. Any other
          // refusal leaves the document as it was and is shown; a 401 starts the sign-on again.
          if (err instanceof ApiError && err.status === 404) state.documents = state.documents.filter((x) => x.id !== id);
          else state.message = failure(err) ?? "";
        })
        .finally(d.redraw);
    }
  };

  const stream = async (): Promise<void> => {
    state.live = true;
    d.redraw();
    try {
      await d.cortex.events(new AbortController().signal, onJob);
    } catch (err) {
      if (failure(err) === null) {
        state.live = false;
        return;
      }
    }
    state.live = false;
    d.redraw();
    d.later(() => void stream(), REOPEN_MS);
  };

  const actions: Actions = {
    show(tab) {
      state.tab = tab;
      d.redraw();
    },
    addSource: (s) =>
      run(async () => {
        const problems = state.app ? sourceProblems(s, state.app.parameters) : [];
        state.draftSource = s;
        if (problems.length > 0) {
          state.message = problems.join("; ");
          return;
        }
        if (await change((p) => withSource(p, s), `Saved: source ${s.id}. It appears when the release is applied.`)) {
          state.draftSource = null;
          state.done["add-source"]++;
        }
      }),
    removeSource: (id) =>
      run(async () => {
        if (!d.confirm(`Remove source ${id}? Its documents, passages and originals are deleted.`)) return;
        await change((p) => withoutSource(p, id), `Saved: source ${id} removed.`);
      }),
    addAssistant: (a) =>
      run(async () => {
        const problems = state.app ? assistantProblems(a, state.app.parameters) : [];
        state.draftAssistant = a;
        if (problems.length > 0) {
          state.message = problems.join("; ");
          return;
        }
        if (await change((p) => withAssistant(p, a), `Saved: assistant ${a.id}.`)) {
          state.draftAssistant = null;
          state.done["add-assistant"]++;
        }
      }),
    removeAssistant: (id) =>
      run(async () => {
        if (!d.confirm(`Remove assistant ${id}?`)) return;
        await change((p) => withoutAssistant(p, id), `Saved: assistant ${id} removed.`);
      }),
    upload: (sourceId, file, readers) =>
      run(async () => {
        const bad = readers.filter((r) => !READER.test(r));
        if (bad.length > 0) {
          state.message = `Not a reader: ${bad.join(", ")}`;
          return;
        }
        const res = await d.cortex.upload(sourceId, file, file.name, readers);
        state.done.upload++;
        state.message = `Uploaded ${file.name}: document ${res.documentId}, job ${res.jobId}.`;
        await remember(res.documentId);
      }),
    search: (query) =>
      run(async () => {
        state.hits = await d.cortex.search(query);
        if (state.hits.length === 0) state.message = "Nothing found that you may read.";
      }),
    open: (id) =>
      run(async () => {
        await remember(id);
        state.tab = "documents";
      }),
    download: (id) =>
      run(async () => {
        const doc = state.documents.find((x) => x.id === id);
        d.save(await d.cortex.content(id), doc?.title || id);
      }),
    remove: (id) =>
      run(async () => {
        if (!d.confirm(`Delete document ${id}?`)) return;
        const res = await d.cortex.remove(id);
        state.message = `Deletion queued as job ${res.jobId}.`;
      }),
    ask: (assistant, question) =>
      run(async () => {
        state.answer = null;
        state.answer = await d.cortex.ask(assistant, question);
      }),
  };

  const start = async (): Promise<void> => {
    const failed: string[] = [];
    // unread names where a screen says why it shows nothing: the code the route gave.
    const take = async <T>(what: string, p: Promise<T>, set: (v: T) => void, unread?: keyof State["unread"]): Promise<void> => {
      try {
        set(await p);
      } catch (err) {
        if (unread) state.unread[unread] = err instanceof ApiError ? err.code : describe(err);
        const line = failure(err);
        if (line !== null) failed.push(`${what}: ${line}`);
      }
    };
    await Promise.all([
      take("Application", d.applications.load(), (v) => (state.app = v), "app"),
      take("sources", d.cortex.sources(), (v) => (state.sources = v)),
      take("assistants", d.cortex.models(), (v) => (state.models = v), "models"),
    ]);
    state.message = failed.join(" | ");
    d.redraw();
    void stream();
  };

  return { state, actions, start };
}
