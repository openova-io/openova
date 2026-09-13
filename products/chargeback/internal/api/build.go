package api

// BUILD IDENTITY — the one fact a browser tab cannot work out for itself.
//
// index.html is served uncached and /assets/index-<hash>.js is served
// `immutable`, which is the correct pairing: a reload fetches the new shell
// and therefore the new hashed bundle. What it does not cover is a tab that
// is NEVER reloaded. That tab keeps executing the bundle it loaded weeks ago
// and talks to whatever the server has become, and when a write then fails
// the console had no way to say why — the founder's 0.1.42 session, where a
// page that predated the capacity routes reported "edit pool is not working
// at all" and the save simply appeared to do nothing.
//
// So the build is carried in two places, both stamped from the SAME
// h.Version that /healthz and GET /api/v1/me already report — one value, no
// second source of truth:
//
//	the SHELL      <meta name="chargeback-build"> substituted into index.html
//	               as it goes out. Because the shell and the hashed bundle it
//	               names are the same build, stamping the shell stamps the
//	               code that is running.
//	every API      the X-Chargeback-Build response header. Every call the
//	RESPONSE       console already makes carries the server's current build,
//	               so detection costs no request of its own — no poller, no
//	               interval, and the answer is present on the very response
//	               that failed.
const (
	// BuildMetaName is the <meta name="..."> the console reads its own
	// build identity from.
	BuildMetaName = "chargeback-build"

	// BuildHeader names the build that answered an API call.
	BuildHeader = "X-Chargeback-Build"

	// buildPlaceholder is the token ui/index.html ships with. A shell that
	// still carries it was not stamped (a dev server, or a build older than
	// this one); the console treats that as "unknown" and stays quiet rather
	// than accusing the user of running a stale page it cannot identify.
	buildPlaceholder = "__CHARGEBACK_BUILD__"
)
