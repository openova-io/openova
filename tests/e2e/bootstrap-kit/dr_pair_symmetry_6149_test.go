// #6149 — THE DR PAIRING INVARIANT: a pair that can be PROMOTED must be able to
// FAIL BACK, and neither half may render without the other.
//
// THE DEFECT THIS EXISTS TO PREVENT
// ---------------------------------
// hw293 G12 region-kill, 2026-08-11, dep a0077ba47e3720e5. The deployment census
// taken after region A returned and cnpg-pair had fully converged:
//
//	dr-promoter deployments in region B : 4   <- all four pairs
//	dr-failback deployments in region A : 1   <- bp-cnpg-pair ONLY
//
// The single failback belonged to the single pair that converged. `shared-pg` and
// `shared-pg-b` were left permanently DUAL-WRITABLE on divergent timelines — both
// sides pg_is_in_recovery()=false, different row counts of the SAME table, neither
// following the other, and nothing anywhere scheduled to reconcile them.
// `shared-pg` carries keycloak, the authoritative identity store.
//
// #5623 added the promoter to bp-postgres and thereby converted "does not fail
// over" into "fails over and never comes back" — strictly worse, because a
// split-brain accepting writes on both sides silently diverges rather than simply
// being unavailable. A promoter without a failback is not a partial feature; it is
// a data-integrity hazard.
//
// WHY THE ASSERTIONS LOOK LIKE THIS
// ---------------------------------
//  1. The assertion is on the PAIR COUNT — promoters == failbacks — never on
//     either alone. An assertion that only checks "the failback renders" passes on
//     a tree where the promoter quietly stopped rendering, and an assertion that
//     only checks the promoter is exactly the guard that was green for every one
//     of the months bp-postgres had no failback. The ASYMMETRY is the defect, so
//     the asymmetry is what has to go red.
//  2. Both sides are rendered. The two halves render in SEPARATE helm invocations
//     (side=primary on cluster-A, side=replica on cluster-B), so a test that
//     renders one side cannot see the census that mattered live.
//  3. Deployments are identified by their rendered `catalyst.openova.io/role`
//     LABEL VALUE, parsed as YAML — not by grepping the name, which passes on a
//     comment (#5639: assert on the value, never on the key).
//  4. bp-cnpg-pair is the CONTROL: it shares the suspect property (it is a CNPG DR
//     pair rendering a promoter) and it already carried a failback before this
//     change. It must stay green, unchanged. If the whole suite could pass on an
//     empty render, the control would go red too.
//  5. Vacuity: TestDRPairSymmetry_ComparatorGoesRedOnAsymmetry feeds the SAME
//     comparator a deliberately asymmetric census and requires it to report a
//     violation. A comparator observed only passing is not yet known to be a
//     comparator.
//  6. The registry cannot rot: every chart template in the catalog that emits a
//     CNPG `bootstrap.pg_basebackup` stanza (the structural signature of a standby
//     half) must be covered by a case here or declared promoter-less.
//
// Run: go test ./tests/e2e/bootstrap-kit/ -run 'DRPair' -count=1 -v
// (-count=1 always: a cached green is indistinguishable from a real one.)
//
// Refs #6149 #5623 #5245 #6148
package bootstrapkit

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const (
	drRoleLabel    = "catalyst.openova.io/role"
	drRolePromoter = "dr-promoter"
	drRoleFailback = "dr-failback"

	drRegionA = "hz-fsn-rtz-prod"
	drRegionB = "hz-hel-rtz-prod"
)

// drCensus is the rendered-actor tally for ONE DR pair — the exact shape of the
// live census taken on hw293.
type drCensus struct {
	pair      string
	promoters []string // Deployment names carrying role=dr-promoter (region-B side)
	failbacks []string // Deployment names carrying role=dr-failback (region-A side)
}

func (c drCensus) symmetric() bool { return len(c.promoters) == len(c.failbacks) }

func (c drCensus) String() string {
	return fmt.Sprintf("%s: promoters=%d %v failbacks=%d %v",
		c.pair, len(c.promoters), c.promoters, len(c.failbacks), c.failbacks)
}

// violations returns a human-readable reason per asymmetric pair, plus the
// aggregate census line. THIS is the comparator under test — the vacuity case
// feeds it a hand-built asymmetric census and requires a non-empty result.
func drViolations(census []drCensus) []string {
	var out []string
	totalP, totalF := 0, 0
	for _, c := range census {
		totalP += len(c.promoters)
		totalF += len(c.failbacks)
		if !c.symmetric() {
			out = append(out, fmt.Sprintf(
				"PAIR ASYMMETRY — %s. A pair that can be PROMOTED must be able to FAIL BACK; "+
					"this is the #6149 geometry, in which region B promotes on a kill and region A "+
					"returns as a second writable primary on a divergent timeline with nothing "+
					"scheduled to reconcile them.", c))
		}
	}
	if totalP != totalF {
		out = append(out, fmt.Sprintf(
			"CATALOG CENSUS ASYMMETRY — dr-promoter deployments=%d, dr-failback deployments=%d. "+
				"hw293 measured 4 and 1; the whole explanation of that walk's permanently "+
				"dual-writable shared-pg/shared-pg-b was that promote was implemented for four "+
				"pairs and failback for one.", totalP, totalF))
	}
	return out
}

// drHelm renders a chart and returns the parsed docs. A render ERROR is returned,
// not fataled, so the negative cases can assert on it.
func drHelm(t *testing.T, chartDir string, args ...string) ([]map[string]any, string, error) {
	t.Helper()

	helmBin := os.Getenv("HELM_BIN")
	if helmBin == "" {
		helmBin = "helm"
	}
	if _, err := exec.LookPath(helmBin); err != nil {
		t.Skipf("helm not on PATH (%v)", err)
	}

	root := repoRoot(t)
	full := append([]string{"template", "dr", filepath.Join(root, chartDir)}, args...)
	full = append(full, "--api-versions", "postgresql.cnpg.io/v1")

	cmd := exec.Command(helmBin, full...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, string(out), err
	}

	var docs []map[string]any
	dec := yaml.NewDecoder(strings.NewReader(string(out)))
	for {
		var doc map[string]any
		if derr := dec.Decode(&doc); derr != nil {
			break
		}
		if doc != nil {
			docs = append(docs, doc)
		}
	}
	return docs, string(out), nil
}

// drActors returns the names of Deployments whose rendered
// `catalyst.openova.io/role` label VALUE equals want. Parsed as YAML off the
// object, so a comment or a name substring can never satisfy it.
func drActors(docs []map[string]any, want string) []string {
	var names []string
	for _, d := range docs {
		if k, _ := d["kind"].(string); k != "Deployment" {
			continue
		}
		md, _ := d["metadata"].(map[string]any)
		if md == nil {
			continue
		}
		labels, _ := md["labels"].(map[string]any)
		if labels == nil {
			continue
		}
		if v, _ := labels[drRoleLabel].(string); v == want {
			n, _ := md["name"].(string)
			names = append(names, n)
		}
	}
	sort.Strings(names)
	return names
}

// drPairCase is one DR pair and the two per-side renders that make up its census.
type drPairCase struct {
	pair string
	// chart dir, relative to the repo root
	chart string
	// helm args for the REGION-A (primary) half — where a failback belongs
	primaryArgs []string
	// helm args for the REGION-B (replica) half — where a promoter belongs
	replicaArgs []string
	// control marks the pair that already carried a failback before #6149. It
	// must stay green and unchanged; if it goes red the suite is broken, not the
	// tree under test.
	control bool
}

// bpPostgresArgs builds the shared-pg value set for one instance name + side.
func bpPostgresArgs(instance, side string, extra ...string) []string {
	a := []string{
		"--namespace", "shared-data",
		"--set", "enabled=true",
		"--set", "instance.name=" + instance,
		"--set", "instance.namespace=shared-data",
		"--set", "topology.mode=active-hot-standby",
		"--set", "topology.side=" + side,
		"--set", "topology.primary.region=" + drRegionA,
		"--set", "topology.replica.region=" + drRegionB,
		"--set", "topology.networkPolicy.crossRegionPeerClusters={peer-mesh}",
		"--set", "databases[0].name=keycloak",
		"--set", "databases[0].owner=keycloak",
	}
	return append(a, extra...)
}

func cnpgPairArgs(side string, extra ...string) []string {
	a := []string{
		"--set", "cnpgPair.enabled=true",
		"--set", "cnpgPair.side=" + side,
		"--set", "cnpgPair.primary.region=" + drRegionA,
		"--set", "cnpgPair.replica.region=" + drRegionB,
		"--set", "cnpgPair.image.tag=16.3-23",
		"--set", "cnpgPair.networkPolicy.crossRegionPeerClusters={peer-mesh}",
	}
	return append(a, extra...)
}

// drPairs is the catalog of DR pairs whose census this guard takes. Kept honest
// by TestDRPairSymmetry_RegistryCoversEveryStandbyHalf below.
func drPairs() []drPairCase {
	return []drPairCase{
		{
			// THE CONTROL. Shares the suspect property (a CNPG DR pair that
			// renders a region-B promoter) and already shipped the failback
			// before #6149. Untouched by this change; it must stay green.
			pair:        "bp-cnpg-pair",
			chart:       "platform/cnpg-pair/chart",
			primaryArgs: cnpgPairArgs("primary"),
			replicaArgs: cnpgPairArgs("replica"),
			control:     true,
		},
		{
			// hw293: promoted, then returned WRITABLE on TL=3 alongside a
			// WRITABLE region B on TL=3 with different row counts. Carries
			// keycloak.
			pair:        "bp-postgres/shared-pg",
			chart:       "platform/postgres/chart",
			primaryArgs: bpPostgresArgs("shared-pg", "primary"),
			replicaArgs: bpPostgresArgs("shared-pg", "replica"),
		},
		{
			// hw293: promoted, then returned WRITABLE on TL=2 against a WRITABLE
			// region B on TL=3. The `secondary` side alias is exercised here
			// because that is the literal value cloud-init stamps.
			pair:  "bp-postgres/shared-pg-b",
			chart: "platform/postgres/chart",
			primaryArgs: bpPostgresArgs("shared-pg-b", "primary",
				"--set", "topology.failback.hr.name=bp-postgres-shared-b",
				"--set", "topology.failback.demotedSubstituteKey=SOVEREIGN_SHARED_PG_B_DEMOTED"),
			replicaArgs: bpPostgresArgs("shared-pg-b", "secondary",
				"--set", "topology.autoPromote.hr.name=bp-postgres-shared-b"),
		},
		{
			// hw293: did NOT promote (its promoter correctly refused — #5220,
			// unarmed at T0), so there was never a second timeline. Included so
			// the census covers the pair that looked healthy for the wrong
			// reason.
			pair:  "bp-postgres/shared-pg-c",
			chart: "platform/postgres/chart",
			primaryArgs: bpPostgresArgs("shared-pg-c", "primary",
				"--set", "topology.failback.hr.name=bp-postgres-shared-c",
				"--set", "topology.failback.demotedSubstituteKey=SOVEREIGN_SHARED_PG_C_DEMOTED"),
			replicaArgs: bpPostgresArgs("shared-pg-c", "secondary",
				"--set", "topology.autoPromote.hr.name=bp-postgres-shared-c"),
		},
	}
}

// TestDRPairSymmetry_EveryPromotablePairCanFailBack is the primary assertion:
// the rendered PAIR COUNT, per pair AND in aggregate.
//
// BEFORE this fix it reports exactly the hw293 census — 4 promoters, 1 failback —
// and fails on three pairs. AFTER, 4 and 4.
func TestDRPairSymmetry_EveryPromotablePairCanFailBack(t *testing.T) {
	var census []drCensus

	for _, c := range drPairs() {
		primaryDocs, primaryOut, err := drHelm(t, c.chart, c.primaryArgs...)
		if err != nil {
			t.Fatalf("%s: region-A (primary) render failed: %v\n%s", c.pair, err, tailLines(primaryOut, 6))
		}
		replicaDocs, replicaOut, err := drHelm(t, c.chart, c.replicaArgs...)
		if err != nil {
			t.Fatalf("%s: region-B (replica) render failed: %v\n%s", c.pair, err, tailLines(replicaOut, 6))
		}

		cc := drCensus{
			pair: c.pair,
			// The promoter is a region-B actor and the failback a region-A one,
			// so each is counted on the side that would actually run it. Counting
			// both sides for both roles would let a misplaced actor pass.
			promoters: drActors(replicaDocs, drRolePromoter),
			failbacks: drActors(primaryDocs, drRoleFailback),
		}
		census = append(census, cc)
		t.Logf("census %s", cc)

		if c.control {
			// The control must keep BOTH actors. It shares the suspect property,
			// so if this pair ever goes red the suite itself is broken (bad helm,
			// bad label, empty render) and no green elsewhere can be trusted.
			if len(cc.failbacks) == 0 {
				t.Errorf("CONTROL %s lost its dr-failback — this pair carried one before #6149 and "+
					"must be unchanged by it. A red control means the guard cannot distinguish a "+
					"real green from a vacuous one.", c.pair)
			}
			if len(cc.promoters) == 0 {
				t.Errorf("CONTROL %s lost its dr-promoter — see above.", c.pair)
			}
		}
	}

	if len(census) == 0 {
		t.Fatal("ZERO pairs rendered — the guard is vacuous. Fix the case table before trusting a green run.")
	}

	for _, v := range drViolations(census) {
		t.Error(v)
	}
}

// TestDRPairSymmetry_ComparatorGoesRedOnAsymmetry — THE VACUITY CHECK.
//
// Feed the comparator a deliberately asymmetric census (the literal hw293 shape:
// four promoters, one failback) and require it to report violations. Without this,
// a comparator that returned nil unconditionally would make every other assertion
// in this file pass forever.
func TestDRPairSymmetry_ComparatorGoesRedOnAsymmetry(t *testing.T) {
	hw293 := []drCensus{
		{pair: "bp-cnpg-pair", promoters: []string{"cnpg-pair-dr-promoter"}, failbacks: []string{"cnpg-pair-dr-failback"}},
		{pair: "shared-pg", promoters: []string{"shared-pg-dr-promoter"}},
		{pair: "shared-pg-b", promoters: []string{"shared-pg-b-dr-promoter"}},
		{pair: "shared-pg-c", promoters: []string{"shared-pg-c-dr-promoter"}},
	}
	v := drViolations(hw293)
	if len(v) == 0 {
		t.Fatal("VACUOUS COMPARATOR: the literal hw293 census (4 promoters, 1 failback — the census that " +
			"accompanied two permanently dual-writable databases) was reported as SYMMETRIC. Every other " +
			"assertion in this file is worthless until this is fixed.")
	}
	if len(v) != 4 { // three asymmetric pairs + the aggregate line
		t.Errorf("expected 3 per-pair violations + 1 aggregate, got %d:\n%s", len(v), strings.Join(v, "\n"))
	}

	// And the inverse: a symmetric census must produce NO violations, so the
	// comparator is not simply always-red.
	balanced := []drCensus{
		{pair: "a", promoters: []string{"p"}, failbacks: []string{"f"}},
		{pair: "b", promoters: []string{"p"}, failbacks: []string{"f"}},
	}
	if v := drViolations(balanced); len(v) != 0 {
		t.Errorf("comparator is always-red: a balanced census produced %v", v)
	}
}

// TestDRPairSymmetry_OperatorCannotDisableOneHalf — the producer-side invariant.
//
// drPairCapable makes the STRUCTURAL preconditions common to both actors, so the
// only remaining way to express the hw293 geometry is to switch exactly one of the
// two `enabled` gates off by hand. That must FAIL THE RENDER, naming both keys —
// in BOTH directions.
func TestDRPairSymmetry_OperatorCannotDisableOneHalf(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{
			// The #6149 hazard itself: promote on, fail back off.
			name: "failback disabled while the promoter still renders",
			args: bpPostgresArgs("shared-pg", "replica", "--set", "topology.failback.enabled=false"),
		},
		{
			// The inverse trap: an actor armed to demote and re-clone region A on
			// a peer-ahead proof that only a hand-promotion could ever produce.
			name: "promoter disabled while the failback still renders",
			args: bpPostgresArgs("shared-pg", "primary", "--set", "topology.autoPromote.enabled=false"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, out, err := drHelm(t, "platform/postgres/chart", tc.args...)
			if err == nil {
				t.Fatalf("render SUCCEEDED with one DR half disabled. The chart must fail closed: "+
					"this is the configuration that produced hw293's permanently dual-writable "+
					"shared-pg.\nrendered:\n%s", tailLines(out, 10))
			}
			if !strings.Contains(out, "DR PAIRING INVARIANT VIOLATED") {
				t.Fatalf("render failed, but not with the pairing-invariant error — the operator gets "+
					"no idea which two keys disagree.\ngot:\n%s", tailLines(out, 10))
			}
			for _, key := range []string{"topology.autoPromote.enabled", "topology.failback.enabled"} {
				if !strings.Contains(out, key) {
					t.Errorf("the failure message does not name %q; an invariant the operator cannot "+
						"locate is not enforceable", key)
				}
			}
		})
	}
}

// TestDRPairSymmetry_NoDRChainRendersNeitherHalf — the negative twin.
//
// Every configuration that is NOT DR-capable must render ZERO of BOTH actors. A
// blanket "everything renders" cannot satisfy the symmetry assertion above, and a
// promoter that renders where it can never promote is the silently-inert shape
// #6149 replaced with an honestly-absent one.
func TestDRPairSymmetry_NoDRChainRendersNeitherHalf(t *testing.T) {
	cases := []struct {
		name string
		why  string
		args []string
	}{
		{
			name: "async replication",
			why:  "async has no synchronous data fence — an automatic promote could fork committed data, and the failback's 'region-B's line contains every acked commit' premise does not hold",
			args: []string{"--set", "topology.replication.mode=async"},
		},
		{
			name: "no peer ClusterMesh names",
			why:  "with no peer identity there is no cross-region probe path: the promoter could never positively prove region A is gone (#5178) and the failback could not observe the peer at all",
			args: []string{"--set", "topology.networkPolicy.crossRegionPeerClusters=null"},
		},
		{
			name: "clusterMesh disabled",
			why:  "same as above — the mesh IS the probe path",
			args: []string{"--set", "topology.clusterMesh.enabled=false"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, side := range []string{"primary", "replica"} {
				docs, out, err := drHelm(t, "platform/postgres/chart", bpPostgresArgs("shared-pg", side, tc.args...)...)
				if err != nil {
					t.Fatalf("side=%s render errored (expected a clean render with no DR actors): %v\n%s",
						side, err, tailLines(out, 6))
				}
				if got := drActors(docs, drRolePromoter); len(got) != 0 {
					t.Errorf("side=%s rendered a dr-promoter under %q — %s; got %v", side, tc.name, tc.why, got)
				}
				if got := drActors(docs, drRoleFailback); len(got) != 0 {
					t.Errorf("side=%s rendered a dr-failback under %q — %s; got %v", side, tc.name, tc.why, got)
				}
			}
		})
	}
}

// TestDRPairSymmetry_SingletonRendersNoDRActors — non-regression for the
// overwhelming majority of installs. A single-region prov must be untouched.
func TestDRPairSymmetry_SingletonRendersNoDRActors(t *testing.T) {
	docs, out, err := drHelm(t, "platform/postgres/chart",
		"--namespace", "shared-data",
		"--set", "enabled=true",
		"--set", "instance.name=shared-pg",
		"--set", "instance.namespace=shared-data",
		"--set", "databases[0].name=keycloak",
		"--set", "databases[0].owner=keycloak",
	)
	if err != nil {
		t.Fatalf("singleton render errored — the #6149 invariant must not affect non-DR installs: %v\n%s",
			err, tailLines(out, 6))
	}
	if got := drActors(docs, drRolePromoter); len(got) != 0 {
		t.Errorf("singleton rendered a dr-promoter: %v", got)
	}
	if got := drActors(docs, drRoleFailback); len(got) != 0 {
		t.Errorf("singleton rendered a dr-failback: %v", got)
	}
	if strings.Contains(out, "-replica-mesh") {
		t.Errorf("singleton leaked the reverse-direction `-replica-mesh` alias — a single-region " +
			"install has no peer region and must stay byte-identical")
	}
}

// TestDRPairSymmetry_FailbackDrivesTheDurableSeam — the failback is only worth
// counting if it can actually act. Assert on the rendered VALUES that make it
// able to demote, not on the Deployment's existence (#5639).
func TestDRPairSymmetry_FailbackDrivesTheDurableSeam(t *testing.T) {
	// Per-instance demotion substitute keys. Three bp-postgres installs share ONE
	// bootstrap-kit Kustomization, so a SHARED key would demote and re-clone all
	// three the moment any single one of them failed back.
	seen := map[string]string{}

	for _, c := range drPairs() {
		if !strings.HasPrefix(c.pair, "bp-postgres/") {
			continue
		}
		docs, out, err := drHelm(t, c.chart, c.primaryArgs...)
		if err != nil {
			t.Fatalf("%s: primary render failed: %v\n%s", c.pair, err, tailLines(out, 6))
		}
		env := drActorEnv(docs, drRoleFailback, "actor")
		if len(env) == 0 {
			t.Fatalf("%s: no dr-failback actor container env rendered", c.pair)
		}

		// #5125-D1 — the demote must ride a DURABLE source seam. A live Cluster-CR
		// patch is reverted by flux drift-correction mid-outage (hw256 G12).
		for _, k := range []string{"KS_NAME", "KS_NAMESPACE", "HR_NAME", "DEMOTED_SUB_KEY"} {
			if strings.TrimSpace(env[k]) == "" {
				t.Errorf("%s: dr-failback actor has no %s — it has no durable demote seam", c.pair, k)
			}
		}
		key := env["DEMOTED_SUB_KEY"]
		if prev, dup := seen[key]; dup {
			t.Errorf("%s and %s SHARE the demotion substitute key %q. They are installed against the "+
				"SAME bootstrap-kit Kustomization, so one pair failing back would demote and re-clone "+
				"the other(s) too.", prev, c.pair, key)
		}
		seen[key] = c.pair

		// #5245 — the peer-ahead hold must be a positive number of seconds; a zero
		// hold demotes on the first flap.
		if h := strings.TrimSpace(env["HOLD_SECONDS"]); h == "" || h == "0" {
			t.Errorf("%s: dr-failback HOLD_SECONDS=%q — a zero/absent peer-ahead hold demotes region A "+
				"on a transient mesh blip", c.pair, h)
		}
	}
	if len(seen) < 3 {
		t.Errorf("expected 3 distinct bp-postgres demotion substitute keys, got %d: %v", len(seen), seen)
	}
}

// TestDRPairSymmetry_RegistryCoversEveryStandbyHalf keeps drPairs() honest: any
// chart template in the catalog that emits a CNPG `bootstrap.pg_basebackup`
// stanza renders one half of a pair, and the other half is somewhere it can be
// lost. A new one must be either covered here or explicitly declared
// promoter-less.
func TestDRPairSymmetry_RegistryCoversEveryStandbyHalf(t *testing.T) {
	root := repoRoot(t)

	// Charts that render a CNPG standby half but ship NO promoter at all, and
	// therefore have nothing to be symmetric WITH. bp-wordpress-tenant declares
	// `manual` promotion and fails closed without that declaration
	// (scripts/check-dr-pairs-declare-promotion.sh owns that contract).
	promoterless := map[string]string{
		"platform/wordpress-tenant/chart": "declares promotion.mechanism=manual and renders no DR actor",
	}

	covered := map[string]bool{}
	for _, c := range drPairs() {
		covered[c.chart] = true
	}

	var found []string
	for _, dir := range []string{"platform", "products"} {
		walkErr := filepath.Walk(filepath.Join(root, dir), func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return nil
			}
			if ext := filepath.Ext(path); ext != ".yaml" && ext != ".tpl" {
				return nil
			}
			b, rerr := os.ReadFile(path)
			if rerr != nil {
				return nil
			}
			for _, line := range strings.Split(string(b), "\n") {
				if strings.HasPrefix(strings.TrimSpace(line), "pg_basebackup:") {
					rel, _ := filepath.Rel(root, path)
					found = append(found, rel)
					return nil
				}
			}
			return nil
		})
		if walkErr != nil {
			t.Fatalf("walk %s: %v", dir, walkErr)
		}
	}

	if len(found) == 0 {
		t.Fatal("detector matched ZERO templates — the pg_basebackup signature changed and this " +
			"registry check is now vacuous. Fix the detector before trusting a green run.")
	}
	sort.Strings(found)
	t.Logf("standby-half renderers: %v", found)

	for _, f := range found {
		chart := f
		if i := strings.Index(f, "/templates/"); i > 0 {
			chart = f[:i]
		}
		if covered[chart] || promoterless[chart] != "" {
			continue
		}
		t.Errorf("UNCOVERED DR pair: %s (chart %s). It renders a CNPG standby half, so something "+
			"promotes it on a region kill — and whatever promotes it must also be able to bring the "+
			"other side back (#6149). Add a drPairCase, or declare it promoter-less.", f, chart)
	}
}

// drActorEnv returns the rendered env map of the named container inside the
// Deployment carrying role=want. Values only — a `valueFrom` entry is skipped so
// a present-but-empty key can never read as satisfied (#5639).
func drActorEnv(docs []map[string]any, wantRole, container string) map[string]string {
	out := map[string]string{}
	for _, d := range docs {
		if k, _ := d["kind"].(string); k != "Deployment" {
			continue
		}
		md, _ := d["metadata"].(map[string]any)
		labels, _ := md["labels"].(map[string]any)
		if labels == nil {
			continue
		}
		if v, _ := labels[drRoleLabel].(string); v != wantRole {
			continue
		}
		spec, _ := d["spec"].(map[string]any)
		tmpl, _ := spec["template"].(map[string]any)
		pspec, _ := tmpl["spec"].(map[string]any)
		ctrs, _ := pspec["containers"].([]any)
		for _, ci := range ctrs {
			c, _ := ci.(map[string]any)
			if n, _ := c["name"].(string); n != container {
				continue
			}
			envs, _ := c["env"].([]any)
			for _, ei := range envs {
				e, _ := ei.(map[string]any)
				n, _ := e["name"].(string)
				v, ok := e["value"].(string)
				if n != "" && ok {
					out[n] = v
				}
			}
		}
	}
	return out
}

func tailLines(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}
