# #3379 SOVEREIGNTY — user acceptance walk (web UI)

**Honest framing:** the cutover (cutting the cord to the mothership) is a behind-the-scenes process the user never drives, so there is **almost no end-user UI**. The only thing a user can accept in the browser is: **after** the Sovereign becomes independent, **nothing they touch breaks**.

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| `console.<sov>/` | After cutover, the console still loads and you are signed in, as before | ☐ | |
| `console.<sov>/` | Navigate Dashboard / Apps / Organizations → all render normally | ☐ | |
| `grafana.<sov>/`, `gitea.<sov>/`, `registry.<sov>/` | Each app still loads signed-in, unchanged | ☐ | |
| `https://<app>.<sov>/` | Use one app for real (push a repo, view a dashboard) → it works | ☐ | |
| (any console page) | ❌ **GAP** — there is **no** UI page or badge that shows "this Sovereign is now independent / cutover complete" | ☐ | |
| `/apps` | Install or update an app **after** cutover → it succeeds (pulls from the Sovereign's own local registry) | ☐ | |

**Verdict:** **not an end-user-UI feature** — an operations process. The only valid web-UI acceptance is the **negative** one (after going independent, the user sees no change). Today the cutover has **never been run** on any handed-over environment, so even this is unexecuted.
