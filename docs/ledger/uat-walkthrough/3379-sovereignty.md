# #3379 SOVEREIGNTY — user acceptance walk (web UI)

**Honest framing:** the cutover (cutting the cord to the mothership) is a behind-the-scenes process the user never drives, so there is **almost no end-user UI**. The only thing a user can accept in the browser is: **after** the Sovereign becomes independent, **nothing they touch breaks**.

| Tested page | Description | Status | Evidence |
|---|---|---|---|
| [console.hw133.omani.works](https://console.hw133.omani.works/) | After cutover, the console still loads and you are signed in, as before | ☐ | |
| [Apps](https://console.hw133.omani.works/apps) | Navigate Dashboard / Apps / Organizations → all render normally | ☐ | |
| [grafana.hw133.omani.works](https://grafana.hw133.omani.works/) | App still loads signed-in, unchanged | ☐ | |
| [gitea.hw133.omani.works](https://gitea.hw133.omani.works/) | Push a repo / use the app → it works (served from the Sovereign's own local services) | ☐ | |
| [console.hw133.omani.works](https://console.hw133.omani.works/) | ❌ **GAP** — there is **no** UI page or badge that shows "this Sovereign is now independent / cutover complete" | ☐ | |
| [Apps](https://console.hw133.omani.works/apps) | Install or update an app **after** cutover → it succeeds (pulls from the Sovereign's own local registry) | ☐ | |

**Verdict:** **not an end-user-UI feature** — an operations process. The only valid web-UI acceptance is the **negative** one (after going independent, the user sees no change). Today the cutover has **never been run** on any handed-over environment, so even this is unexecuted.
