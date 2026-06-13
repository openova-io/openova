# #3380 ROBUSTNESS — user acceptance walk (web UI)

**Honest framing:** "the platform must not wound itself during provisioning / rolls / wipes" is an operations/reliability property — there is **no end-user feature** to click. The only thing a user can accept in the browser is the **outcome**: a freshly created environment comes up cleanly and stays usable.

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| `console.<new-sov>/` | After a brand-new Sovereign is provisioned, the console loads and you are signed in (no manual fixing) | ☐ | |
| `/apps` | The full app catalog shows everything **Installed** — none stuck failing | ☐ | |
| `grafana.<sov>/`, `gitea.<sov>/`, `registry.<sov>/` | Each main app loads → the env is usable end-to-end | ☐ | |
| `/apps` | Install a new app → it comes up; the rest of the console keeps working during the install | ☐ | |
| `console.<sov>/` | Decommission/wipe a test environment → reported cleanly; the user's other environments are unaffected | ☐ | |
| (internal) | The internal protections (image policy, network rules, retries, wipe cleanup) have **no UI** — validated only by the outcomes above | ☐ | |

**Verdict:** **not an end-user-UI feature.** It passes only as the **outcome** of every other ticket's walk succeeding on a fresh, zero-touch environment — no standalone UI walk.

**Live note (hw133):** the fresh-environment walk already surfaced a wound a user *would* feel — the SME/billing stack crash-looping (breaks the funnel) — so "comes up clean" currently **fails** for the funnel path.
