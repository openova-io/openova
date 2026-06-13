# #3379 SOVEREIGNTY — user acceptance walk (100% web UI)

**Honest framing:** the cutover (cutting the Sovereign's cord to the mothership) is a **behind-the-scenes process** that the end user never drives. So there is **almost no end-user UI** for it. The only thing a user can *accept* in the browser is: **after** the Sovereign becomes independent, **everything keeps working exactly as before** — nothing the user touches breaks.

_Each line = one browser step. `☐`/`✅`/`❌`._

### After cutover, the console still works
- [ ] Open `https://console.<sov>/` → it loads and you are signed in, exactly as before cutover.
- [ ] Navigate Dashboard / Apps / Organizations → all render normally.

### After cutover, the apps still work
- [ ] Open each main app's URL (`grafana.<sov>`, `gitea.<sov>`, `registry.<sov>`, …) → each loads signed-in, unchanged.
- [ ] Use one app for real (e.g. push to a Gitea repo, view a Grafana dashboard) → it works — the Sovereign is serving everything from its own local services now.

### Is there any user-visible "independent" indicator?
- [ ] ❌ **GAP** — there is **no** console page or badge that shows the user "this Sovereign is now independent / cutover complete". The sovereignty state is internal only; a user cannot see or confirm it in the UI.

### Installing/updating an app still works while disconnected
- [ ] Install or update any app from the console **after** cutover → it succeeds (pulling from the Sovereign's own local registry, not the mothership). ✅ if the install completes and the app comes up.

**Verdict:** #3379 is **not really an end-user-UI feature** — it is an operations process. The only valid web-UI acceptance is the **negative** one: after the Sovereign goes independent, the user notices **no change** (console + every app + installs keep working). There is no UI to "see cutover" — and today the cutover has **never been run** on any handed-over environment, so even this negative walk is unexecuted.
