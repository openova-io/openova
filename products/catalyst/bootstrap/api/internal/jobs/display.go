// display.go — granular, human-readable Job display-name derivation
// (issue #3646: "why I see non granular naming in
// https://console.<fqdn>/jobs").
//
// # The problem this fixes
//
// Every leaf Job carries a URL-safe, prefix-jargon JobName ("install-
// keycloak", "mutation-remove-region", "cron-openbao-snapshot-save",
// "me-east-215-b-1:install-cilium"). The /jobs table renders
// `displayName ?? jobName`, so any leaf whose producing bridge left
// DisplayName empty surfaced the raw slug — exactly the "non granular
// naming" the founder called out. The install + mutation bridges (the
// dominant row volume) never set a DisplayName, and the reconciler
// bridge set only the bare lowercase-hyphenated object name.
//
// # The fix — one chokepoint, structured fields
//
// DeriveLeafDisplayName turns a leaf Job's structured fields (Kind +
// JobName + AppID + Region) into a human-readable label that names the
// COMPONENT and the action's PURPOSE, not the opaque id:
//
//   - install-keycloak                     → "Install Keycloak"
//   - me-east-215-b-1:install-cilium       → "Install Cilium (me-east-215-b-1)"
//   - reconcile-flux-system                → "Reconcile Flux System"
//   - cron-openbao-snapshot-save           → "Snapshot Save (cron)"… see note
//   - mutation-remove-region               → "Remove Region"
//   - reconciler-sso-bridge                → "SSO Bridge (reconciler)"
//   - tofu-init (lifecycle, no DisplayName)→ "Tofu Init"
//
// It is invoked from Store.deriveTreeView's read-time back-fill — the
// SAME single place that already back-fills the typed Kind — so every
// wire row gets a granular name regardless of which bridge minted it
// (and legacy persisted indexes are upgraded on read too). A bridge that
// DOES set a friendly DisplayName (the cutover steps via
// cutoverStepDisplay, the Phase-0 lifecycle via provisionerLifecycleDisplay,
// every group) keeps its own label — the back-fill only ever fills an
// EMPTY DisplayName, never overrides.
package jobs

import "strings"

// acronyms maps a lower-cased slug token onto its canonical display
// casing. Naive title-case would render "api"→"Api", "sso"→"Sso",
// "vcluster"→"Vcluster" — wrong for the OpenOva vocabulary. This table
// fixes the load-bearing acronyms + product names the /jobs surface
// actually shows so a row reads the way the operator says it out loud.
var acronyms = map[string]string{
	"api":      "API",
	"sso":      "SSO",
	"oidc":     "OIDC",
	"idp":      "IdP",
	"dns":      "DNS",
	"db":       "DB",
	"pg":       "PG",
	"cnpg":     "CNPG",
	"cpu":      "CPU",
	"tls":      "TLS",
	"ssl":      "SSL",
	"jwt":      "JWT",
	"url":      "URL",
	"vcluster": "vCluster",
	"openbao":  "OpenBao",
	"ferretdb": "FerretDB",
	"powerdns": "PowerDNS",
	"netbird":  "NetBird",
	"crd":      "CRD",
	"crds":     "CRDs",
	"xrc":      "XRC",
	"tofu":     "Tofu",
	"wal":      "WAL",
	"s3":       "S3",
	"ip":       "IP",
	"eip":      "EIP",
	"ca":       "CA",
	"ha":       "HA",
	"bcp":      "BCP",
	"dr":       "DR",
	"sbom":     "SBOM",
	"trivy":    "Trivy",
	"syft":     "Syft",
}

// humanizeToken renders one hyphen-separated slug token in its canonical
// display casing: an acronym from the table verbatim, otherwise the token
// with its first letter upper-cased ("keycloak" → "Keycloak"). A token
// that already contains an upper-case letter (a region key segment, a
// proper noun already cased upstream) is left untouched so we never
// mangle e.g. "me-east-215-b-1".
func humanizeToken(tok string) string {
	if tok == "" {
		return ""
	}
	if disp, ok := acronyms[strings.ToLower(tok)]; ok {
		return disp
	}
	// Leave a token that already carries internal casing alone.
	for _, r := range tok {
		if r >= 'A' && r <= 'Z' {
			return tok
		}
	}
	return strings.ToUpper(tok[:1]) + tok[1:]
}

// HumanizeSlug turns a hyphen-separated slug into a space-separated,
// acronym-aware Title Case label: "cert-manager" → "Cert Manager",
// "catalyst-api" → "Catalyst API", "openbao-snapshot-save" → "OpenBao
// Snapshot Save". A slug carrying a non-hyphen separator the bridges use
// for sub-segments (a ":" region prefix, a "/" multi-region tail) is
// caller-stripped before this is reached.
func HumanizeSlug(slug string) string {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return ""
	}
	parts := strings.Split(slug, "-")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" {
			continue
		}
		out = append(out, humanizeToken(p))
	}
	return strings.Join(out, " ")
}

// kindActionVerb maps a leaf Kind onto the action verb that prefixes its
// display label, so a row reads as an action on a component ("Install
// Cilium", "Reconcile Flux System") rather than a bare noun. install +
// reconcile + step verbs read naturally as a prefix; cron / task /
// reconciler / lifecycle carry their kind as a trailing qualifier
// instead (handled in DeriveLeafDisplayName) because "Cron Openbao
// Snapshot Save" reads worse than "OpenBao Snapshot Save (cron)".
var kindActionVerb = map[string]string{
	KindInstall:   "Install",
	KindReconcile: "Reconcile",
}

// stripRegionPrefix splits a multi-region component / jobName of the form
// "<region>:<rest>" into its region key and the bare remainder. A value
// with no ":" is a primary-region row → ("", value). Mirrors
// RegionFromComponent's "everything before the first colon" contract.
func stripRegionPrefix(s string) (region, rest string) {
	if i := strings.IndexByte(s, ':'); i > 0 {
		return s[:i], s[i+1:]
	}
	return "", s
}

// DeriveLeafDisplayName produces a granular, human-readable label for a
// leaf Job that has no bridge-supplied DisplayName. It NEVER returns a
// raw slug: the worst case (an unrecognised lifecycle slug) is still
// humanized to Title Case. Group Jobs are handled by the caller (their
// DisplayName is always bridge-supplied); passing one here falls through
// to a humanized JobName, which is harmless.
//
// The region (for a multi-region secondary install row) is surfaced as a
// trailing "(<region>)" qualifier so two same-chart rows in different
// regions are distinguishable at a glance — the dedicated Region column
// shows it too, but the name must stand alone in a search result / a
// narrow viewport where the column is scrolled off.
func DeriveLeafDisplayName(j Job) string {
	kind := j.Kind
	if kind == "" {
		kind = kindForLeaf(j.JobName)
	}

	// Region qualifier: prefer the first-class Region field; fall back to
	// the AppID / JobName "<region>:" prefix the multi-region fan-out
	// encodes.
	region := j.Region
	if region == "" {
		if r, _ := stripRegionPrefix(j.AppID); r != "" {
			region = r
		} else if r, _ := stripRegionPrefix(strings.TrimPrefix(j.JobName, JobNamePrefix)); r != "" {
			region = r
		}
	}
	suffix := ""
	if region != "" {
		suffix = " (" + region + ")"
	}

	switch kind {
	case KindInstall:
		// Component = AppID without any region prefix; fall back to the
		// JobName with "install-" + region prefix stripped.
		_, comp := stripRegionPrefix(j.AppID)
		if comp == "" {
			_, comp = stripRegionPrefix(strings.TrimPrefix(j.JobName, JobNamePrefix))
		}
		return joinVerb(kindActionVerb[KindInstall], HumanizeSlug(comp)) + suffix

	case KindReconcile:
		name := strings.TrimPrefix(j.JobName, ReconcileJobPrefix)
		if j.AppID != "" {
			name = j.AppID
		}
		return joinVerb(kindActionVerb[KindReconcile], HumanizeSlug(name)) + suffix

	case KindStep:
		// "<group>-step-<slug>" → "<Group> · <Step>". A bridge-supplied
		// DisplayName normally pre-empts this; here for completeness +
		// legacy rows.
		group, rest := splitStep(j.JobName)
		if rest == "" {
			return HumanizeSlug(j.JobName)
		}
		if group == "" {
			return HumanizeSlug(rest)
		}
		return HumanizeSlug(group) + " · " + HumanizeSlug(rest)

	case KindMutation:
		// "mutation-<verb>-<kind>[-<slug>]" → "<Verb> <Kind> [<slug>]".
		// The verb + kind read as the operator's action; a trailing slug
		// (the target id) is appended verbatim-humanized to disambiguate.
		rest := strings.TrimPrefix(j.JobName, MutationJobNamePrefix)
		return HumanizeSlug(rest)

	case KindCron:
		return HumanizeSlug(strings.TrimPrefix(j.JobName, CronJobPrefix)) + " (cron)"

	case KindTask:
		return HumanizeSlug(strings.TrimPrefix(j.JobName, TaskJobPrefix)) + " (task)"

	case KindReconciler:
		return HumanizeSlug(strings.TrimPrefix(j.JobName, ReconcilerJobPrefix)) + " (reconciler)"

	default:
		// Phase-0 lifecycle slugs (tofu-init, …) + any other leaf. A
		// bridge-supplied lifecycle DisplayName ("Terraform Init") normally
		// pre-empts this; the bare humanized slug is the floor.
		return HumanizeSlug(j.JobName) + suffix
	}
}

// joinVerb prefixes a verb onto a humanized component name, tolerating an
// empty component (returns just the verb) so a malformed row never yields
// a dangling "Install ".
func joinVerb(verb, name string) string {
	verb = strings.TrimSpace(verb)
	name = strings.TrimSpace(name)
	switch {
	case verb == "":
		return name
	case name == "":
		return verb
	default:
		return verb + " " + name
	}
}

// splitStep splits an activity step JobName "<group>-step-<slug>" into its
// group slug and step slug. A name without the "-step-" infix returns
// ("", name) so the caller humanizes it whole.
func splitStep(jobName string) (group, step string) {
	i := strings.Index(jobName, ActivityStepInfix)
	if i < 0 {
		return "", jobName
	}
	return jobName[:i], jobName[i+len(ActivityStepInfix):]
}
