# #3380 ROBUSTNESS — user acceptance walk (100% web UI)

**Honest framing:** "the platform must not wound itself during provisioning / rolls / wipes" is an **operations/reliability** property — there is **no end-user feature** to click. The user never sees a kyverno policy or a network rule. The only thing a user can *accept* in the browser is the **outcome**: a freshly created environment **comes up cleanly and stays up**, and creating/deleting environments doesn't break anything they can see.

_Each line = one browser step. `☐`/`✅`/`❌`._

### A fresh environment comes up clean (the user-visible proof of robustness)
- [ ] After a brand-new Sovereign is provisioned, open `https://console.<new-sov>/` → it loads and you are signed in. ✅ if the console comes up with no manual fixing.
- [ ] Open **Apps** → the full app catalog shows everything **Installed** (none stuck failing). ✅ if all core apps are healthy.
- [ ] Open each main app URL (grafana / gitea / harbor / …) → each loads. ✅ if the env is genuinely usable end-to-end.

### Creating and removing things doesn't visibly break the platform
- [ ] Install a new app from the console → it comes up; the rest of the console keeps working during the install (no blank screens, no errors).
- [ ] (Operator) decommission / wipe a test environment → the console reports it cleanly and the user's other environments are unaffected.

### Where robustness is INVISIBLE to the user (noted, not walked)
- [ ] The internal protections (image-source policy, network rules, fetch retries, wipe cleanup, watch-resume after a restart) have **no UI** — a user cannot and should not see them. They are validated only by the above outcomes holding true.

**Verdict:** #3380 is **not an end-user-UI feature**. Its only valid web-UI acceptance is the **outcome** — a fresh environment provisions cleanly and stays usable, and lifecycle actions don't visibly break the platform. In effect, **#3380 passes when every *other* ticket's walk succeeds on a fresh, zero-touch environment**; it has no standalone UI walk of its own.

**Live note (hw133):** the fresh-environment walk already surfaced real wounds that a user *would* feel — the SME/billing stack crash-looping (breaks the funnel) — so the "comes up clean" check currently **fails** for the funnel path.
