// The entry point in the browser: settings, sign-on, then the screens.

import { createApp } from "./app.js";
import { Applications } from "./application.js";
import { currentSession, discover, finishSignIn, signInURL, signOut, type Session } from "./auth.js";
import { loadConfig } from "./config.js";
import { Cortex } from "./cortex.js";
import { mount } from "./dom.js";
import { shellView } from "./screens/shell.js";

async function main(root: HTMLElement): Promise<void> {
  const store = window.sessionStorage;
  const redirect = `${location.origin}${location.pathname}`;
  const cfg = await loadConfig(fetch.bind(window));
  const endpoints = await discover(cfg, fetch.bind(window));
  const params = new URLSearchParams(location.search);
  let session: Session | null = currentSession(store, Date.now());
  if (params.has("code")) {
    session = await finishSignIn(cfg, endpoints, params, redirect, store, fetch.bind(window), Date.now());
    history.replaceState(null, "", location.pathname);
  }
  const signIn = (): void => {
    signOut(store);
    void signInURL(cfg, endpoints, redirect, store).then((url) => location.assign(url));
  };
  if (!session) {
    signIn();
    return;
  }
  const token = session.token;
  let redraw = (): void => {};
  const app = createApp({
    cortex: new Cortex(cfg.cortexURL, () => token, fetch.bind(window)),
    applications: new Applications(cfg, () => token, fetch.bind(window)),
    redraw: () => redraw(),
    confirm: (q) => window.confirm(q),
    save: (content, name) => {
      const a = document.createElement("a");
      a.href = URL.createObjectURL(content);
      a.download = name;
      a.click();
      URL.revokeObjectURL(a.href);
    },
    later: (work, ms) => void window.setTimeout(work, ms),
    signIn,
  });
  redraw = mount(root, () => shellView(app.state, app.actions));
  await app.start();
}

const root = document.getElementById("app");
if (root) {
  main(root).catch((err: unknown) => {
    root.textContent = `The knowledge hub cannot start: ${err instanceof Error ? err.message : String(err)}`;
  });
}
