package handler

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	kfake "k8s.io/client-go/kubernetes/fake"
)

// #6072 — the shared-pg consumer-hub readiness gate tested a string, not a
// readiness fact, and on hw293 (dep a0077ba47e3720e5, measured 2026-08-11) that
// string could never turn true.
//
// MEASURED SHAPE THIS FILE ENCODES:
//
//   - All three region-A CNPG clusters healthy, 1 instance each; all nine hub
//     Secrets carrying `shared-pg{,-b,-c}-rw.shared-data.svc.cluster.local`,
//     because role-secrets.yaml renders `-mesh-rw` only under
//     `bp-postgres.activeHotStandby` (= `topology.crossRegion`, patched only by
//     enableCNPGPairAfterFullMesh).
//   - Region-B holding 0 of the 13 hub Secrets, while holding all 12
//     CNPG-operator-minted `-ca`/`-replication`/`-server`/`-superuser` ones —
//     the control proving the namespace, its Flux ownership and the operator
//     all work there, and only cross-region delivery was absent.
//   - All six `-mesh-rw` Services present in BOTH regions the entire time.
//     Region-B's `shared-pg-mesh-rw` is the correct zero-backend stub annotated
//     `service.cilium.io/global: "true"`; region-A's carries the only endpoint.
//     So the alias the gate was waiting to see named in a string was live and
//     routable on the replica the whole time.
//   - Downstream: `keycloak-0` in region-B stuck `Init:0/1` on
//     `MountVolume.SetUp failed … secret "keycloak-database-secret" not found`,
//     232 occurrences over 7h38m.
//
// The deferral is PERMANENT, not a timing window: the `host` string only turns
// `-mesh-rw` at the cnpg-pair flip, which is the very flip #5230 hoisted this
// sync OUT of — so the hoist was inert, and where the flip never lands the
// Secrets never land either.
//
// These tests are written so the CONTROL cases fail loudly if a future change
// "fixes" this by weakening the gate instead of re-basing it on evidence.

// seedMeshAliasService creates the ClusterMesh-global write alias Service the
// replica publishes (`<instance>-mesh-rw` in shared-data). global=false seeds a
// same-named Service WITHOUT the cilium global annotation — a region-local
// name, which must NOT satisfy the readiness gate.
func seedMeshAliasService(t *testing.T, cs kubernetes.Interface, name string, global bool) {
	t.Helper()
	ctx := context.Background()
	if _, err := cs.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: sharedPGNamespace},
	}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create shared-data namespace: %v", err)
	}
	annotations := map[string]string{"service.cilium.io/affinity": "local"}
	if global {
		annotations[ciliumGlobalServiceAnnotation] = "true"
	}
	if _, err := cs.CoreV1().Services(sharedPGNamespace).Create(ctx, &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   sharedPGNamespace,
			Annotations: annotations,
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Ports:    []corev1.ServicePort{{Name: "postgres", Port: 5432}},
			Selector: map[string]string{"cnpg.io/cluster": "shared-pg", "cnpg.io/instanceRole": "primary"},
		},
	}, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatalf("create mesh alias Service %s: %v", name, err)
	}
}

// TestMeshRWHostRewrite pins the pure host mapping, including the shapes that
// must NOT be rewritten.
func TestMeshRWHostRewrite(t *testing.T) {
	cases := []struct {
		name      string
		host      string
		wantHost  string
		wantAlias string
		wantOK    bool
	}{
		{
			name:      "region-local rw host maps onto the mesh alias",
			host:      "shared-pg-rw.shared-data.svc.cluster.local",
			wantHost:  "shared-pg-mesh-rw.shared-data.svc.cluster.local",
			wantAlias: "shared-pg-mesh-rw",
			wantOK:    true,
		},
		{
			name:      "instance-b (the hw293 grafana/pdns/pda hub host)",
			host:      "shared-pg-b-rw.shared-data.svc.cluster.local",
			wantHost:  "shared-pg-b-mesh-rw.shared-data.svc.cluster.local",
			wantAlias: "shared-pg-b-mesh-rw",
			wantOK:    true,
		},
		{
			name:      "instance-c (the hw293 org/newapi/openova-flow hub host)",
			host:      "shared-pg-c-rw.shared-data.svc.cluster.local",
			wantHost:  "shared-pg-c-mesh-rw.shared-data.svc.cluster.local",
			wantAlias: "shared-pg-c-mesh-rw",
			wantOK:    true,
		},
		// An ALREADY-mesh host must be rejected outright: rewriting it again
		// would fabricate `shared-pg-mesh-mesh-rw`, a name nothing publishes.
		{name: "already a mesh alias is not rewritable", host: "shared-pg-mesh-rw.shared-data.svc.cluster.local"},
		{name: "empty host", host: ""},
		{name: "no domain part", host: "shared-pg-rw"},
		{name: "first label does not end in -rw", host: "shared-pg-ro.shared-data.svc.cluster.local"},
		{name: "read alias is not a write host", host: "shared-pg-r.shared-data.svc.cluster.local"},
		{name: "bare -rw with no instance name", host: "-rw.shared-data.svc.cluster.local"},
		{name: "external managed database host", host: "db.example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotHost, gotAlias, ok := meshRWHostRewrite(tc.host)
			if ok != tc.wantOK {
				t.Fatalf("meshRWHostRewrite(%q) ok = %v, want %v", tc.host, ok, tc.wantOK)
			}
			if !tc.wantOK {
				return
			}
			if gotHost != tc.wantHost {
				t.Errorf("host = %q, want %q", gotHost, tc.wantHost)
			}
			if gotAlias != tc.wantAlias {
				t.Errorf("alias = %q, want %q", gotAlias, tc.wantAlias)
			}
		})
	}
}

// TestSyncSharedPGConsumerHubSecretsRewritesLocalRWHost is the hw293 regression.
//
// RED against the pre-#6072 gate: the source host is the region-local `-rw`
// form, so `strings.Contains(host, "-mesh-rw.")` is false and the Secret is
// deferred — the replica gains NOTHING and keycloak stays Init:0/1.
func TestSyncSharedPGConsumerHubSecretsRewritesLocalRWHost(t *testing.T) {
	h := &Handler{log: silentLogger()}
	dep := &Deployment{ID: "dep-6072"}

	// The hw293 shape: region-A hub Secrets on the region-local `-rw` host
	// (crossRegion never flipped), region-B publishing the mesh-global alias.
	primaryCS := kfake.NewSimpleClientset()
	replicaCS := kfake.NewSimpleClientset()
	seedHubSecret(t, primaryCS, "keycloak-database-secret",
		"shared-pg-rw.shared-data.svc.cluster.local", "region-A-authoritative-pw")
	seedMeshAliasService(t, replicaCS, "shared-pg-mesh-rw", true)

	slots := []regionSlot{
		{key: "", clientset: primaryCS},
		{key: "me-east-215-b-1", clientset: replicaCS},
	}
	h.syncSharedPGConsumerHubSecrets(context.Background(), dep, slots)

	got, ok := getSharedDataSecret(t, replicaCS, "keycloak-database-secret")
	if !ok {
		t.Fatalf("replica did not receive keycloak-database-secret — this is the hw293 defect: " +
			"the readiness gate defers every hub Secret while region-A's host is the region-local `-rw` " +
			"form, and that string only turns `-mesh-rw` at the cnpg-pair flip. keycloak-0 stays Init:0/1.")
	}

	// ASSERT ON THE VALUE, not on existence.
	//
	// The host must be the mesh alias — delivering region-A's literal `-rw`
	// host would point the replica at a Service that on hw293 has ZERO
	// endpoints in region-B.
	wantHost := "shared-pg-mesh-rw.shared-data.svc.cluster.local"
	if h := string(got.Data["host"]); h != wantHost {
		t.Errorf("replica host = %q, want %q (the region-local `-rw` host does not route from the replica)", h, wantHost)
	}
	// The credential must be region-A's, byte-for-byte. This is what #5224 /
	// #5228 made un-mintable on the replica side, so a wrong value here is a
	// regression of that fix.
	if p := string(got.Data["password"]); p != "region-A-authoritative-pw" {
		t.Errorf("replica password = %q, want region-A's authoritative value — the replica cannot mint its own since #5228", p)
	}
	// The `uri` embeds the host, so it must have been rewritten with it — and
	// must still carry region-A's password.
	uri := string(got.Data["uri"])
	if !strings.Contains(uri, wantHost) {
		t.Errorf("replica uri = %q, want it to embed the mesh alias host %q", uri, wantHost)
	}
	if strings.Contains(uri, "shared-pg-rw.shared-data") {
		t.Errorf("replica uri = %q still embeds the region-local `-rw` host — the host rewrite missed the uri", uri)
	}
	if !strings.Contains(uri, "region-A-authoritative-pw") {
		t.Errorf("replica uri = %q lost region-A's password", uri)
	}
	// The reflector annotations must survive so region-B's own reflector
	// re-pushes the Secret into the keycloak namespace (the object the pod
	// actually mounts).
	if got.Annotations["reflector.v1.k8s.emberstack.com/reflection-auto-enabled"] != "true" {
		t.Errorf("replica hub Secret lost the reflection-auto annotation — the consumer namespace would never get the copy")
	}

	// The SOURCE must be untouched: region-A stays on its own region-local
	// host, which is correct for region-A. The rewrite is a delivery-time
	// transform, not a mutation of the authoritative copy.
	srcAfter, _ := getSharedDataSecret(t, primaryCS, "keycloak-database-secret")
	if h := string(srcAfter.Data["host"]); h != "shared-pg-rw.shared-data.svc.cluster.local" {
		t.Errorf("primary host = %q, want it UNCHANGED — the rewrite must not mutate region-A's authoritative Secret", h)
	}
}

// TestSyncSharedPGConsumerHubSecretsDeferralControls is THE decisive control
// set. A fix that merely removes the gate delivers Secrets to a replica that
// cannot route them, and would sail through the happy-path test above. Each
// case here must still DEFER after the fix.
func TestSyncSharedPGConsumerHubSecretsDeferralControls(t *testing.T) {
	h := &Handler{log: silentLogger()}
	dep := &Deployment{ID: "dep-6072-controls"}

	// CONTROL 1 — the replica does not publish the alias at all. A `-rw` host
	// pushed here would be a black hole, so nothing may be delivered. This is
	// the pre-flip state of a replica whose bp-postgres has not rendered yet.
	t.Run("no-mesh-alias-on-replica-still-defers", func(t *testing.T) {
		primaryCS := kfake.NewSimpleClientset()
		replicaCS := kfake.NewSimpleClientset()
		seedHubSecret(t, primaryCS, "keycloak-database-secret",
			"shared-pg-rw.shared-data.svc.cluster.local", "region-A-pw")
		// Deliberately NO mesh alias Service on the replica.
		slots := []regionSlot{{key: "", clientset: primaryCS}, {key: "secondary", clientset: replicaCS}}
		h.syncSharedPGConsumerHubSecrets(context.Background(), dep, slots)

		if _, ok := getSharedDataSecret(t, replicaCS, "keycloak-database-secret"); ok {
			t.Errorf("replica received a hub Secret although it publishes no `shared-pg-mesh-rw` alias — " +
				"the delivered host would resolve nowhere. The readiness gate must survive the fix, not be deleted.")
		}
	})

	// CONTROL 2 — the alias NAME exists on the replica but is not
	// ClusterMesh-global. It is then a region-local name: on hw293 region-B's
	// local `shared-pg-rw` had zero endpoints, which is exactly this hazard.
	// Name-presence alone must not satisfy the gate.
	t.Run("non-global-alias-on-replica-still-defers", func(t *testing.T) {
		primaryCS := kfake.NewSimpleClientset()
		replicaCS := kfake.NewSimpleClientset()
		seedHubSecret(t, primaryCS, "keycloak-database-secret",
			"shared-pg-rw.shared-data.svc.cluster.local", "region-A-pw")
		seedMeshAliasService(t, replicaCS, "shared-pg-mesh-rw", false) // no service.cilium.io/global
		slots := []regionSlot{{key: "", clientset: primaryCS}, {key: "secondary", clientset: replicaCS}}
		h.syncSharedPGConsumerHubSecrets(context.Background(), dep, slots)

		if _, ok := getSharedDataSecret(t, replicaCS, "keycloak-database-secret"); ok {
			t.Errorf("replica received a hub Secret although its `shared-pg-mesh-rw` Service is NOT annotated "+
				"%q=true — a non-global Service is region-local and would not route to the primary", ciliumGlobalServiceAnnotation)
		}
	})

	// CONTROL 3 — an unrecognised write host is not rewritable, so it defers
	// regardless of what the replica publishes. Guards against the rewrite
	// inventing an alias for a host shape it does not understand.
	t.Run("unrecognised-host-still-defers", func(t *testing.T) {
		primaryCS := kfake.NewSimpleClientset()
		replicaCS := kfake.NewSimpleClientset()
		seedHubSecret(t, primaryCS, "keycloak-database-secret", "db.example.com", "region-A-pw")
		seedMeshAliasService(t, replicaCS, "shared-pg-mesh-rw", true)
		slots := []regionSlot{{key: "", clientset: primaryCS}, {key: "secondary", clientset: replicaCS}}
		h.syncSharedPGConsumerHubSecrets(context.Background(), dep, slots)

		if _, ok := getSharedDataSecret(t, replicaCS, "keycloak-database-secret"); ok {
			t.Errorf("replica received a hub Secret whose host is neither the `-mesh-rw` alias nor the " +
				"region-local `<instance>-rw` form — an unrecognised host must defer")
		}
	})

	// CONTROL 4 — MIXED replicas. One publishes the alias, one does not. The
	// ready replica must be served; the unready one must not; and the Secret
	// must NOT be counted as synced-everywhere. Pins that the gate is decided
	// per-replica rather than once for the whole fan-out.
	t.Run("mixed-replicas-serve-only-the-ready-one", func(t *testing.T) {
		primaryCS := kfake.NewSimpleClientset()
		readyCS := kfake.NewSimpleClientset()
		unreadyCS := kfake.NewSimpleClientset()
		seedHubSecret(t, primaryCS, "keycloak-database-secret",
			"shared-pg-rw.shared-data.svc.cluster.local", "region-A-pw")
		seedMeshAliasService(t, readyCS, "shared-pg-mesh-rw", true)
		slots := []regionSlot{
			{key: "", clientset: primaryCS},
			{key: "ready", clientset: readyCS},
			{key: "unready", clientset: unreadyCS},
		}
		h.syncSharedPGConsumerHubSecrets(context.Background(), dep, slots)

		got, ok := getSharedDataSecret(t, readyCS, "keycloak-database-secret")
		if !ok {
			t.Fatalf("the READY replica did not receive the hub Secret")
		}
		if hst := string(got.Data["host"]); hst != "shared-pg-mesh-rw.shared-data.svc.cluster.local" {
			t.Errorf("ready replica host = %q, want the mesh alias", hst)
		}
		if _, ok := getSharedDataSecret(t, unreadyCS, "keycloak-database-secret"); ok {
			t.Errorf("the UNREADY replica (no mesh alias) received the hub Secret — the gate must be per-replica")
		}
	})
}

// TestSyncSharedPGConsumerHubSecretsMeshRWSourceNeedsNoAlias pins that the
// POST-FLIP path is byte-identical to pre-#6072: a source already carrying the
// `-mesh-rw` host propagates without the replica needing to publish anything.
// Without this, the fix could silently add a new precondition to the one path
// that already worked (the failure mode the #5230 hoist itself hit).
func TestSyncSharedPGConsumerHubSecretsMeshRWSourceNeedsNoAlias(t *testing.T) {
	h := &Handler{log: silentLogger()}
	dep := &Deployment{ID: "dep-6072-postflip"}

	primaryCS := kfake.NewSimpleClientset()
	replicaCS := kfake.NewSimpleClientset()
	seedHubSecret(t, primaryCS, "keycloak-database-secret",
		"shared-pg-mesh-rw.shared-data.svc.cluster.local", "region-A-pw")
	// NO alias Service seeded on the replica on purpose.

	slots := []regionSlot{{key: "", clientset: primaryCS}, {key: "secondary", clientset: replicaCS}}
	h.syncSharedPGConsumerHubSecrets(context.Background(), dep, slots)

	got, ok := getSharedDataSecret(t, replicaCS, "keycloak-database-secret")
	if !ok {
		t.Fatalf("an already `-mesh-rw` source was not propagated — #6072 must not add a precondition to the post-flip path")
	}
	if hst := string(got.Data["host"]); hst != "shared-pg-mesh-rw.shared-data.svc.cluster.local" {
		t.Errorf("replica host = %q, want the source's `-mesh-rw` host passed through untouched", hst)
	}
	if p := string(got.Data["password"]); p != "region-A-pw" {
		t.Errorf("replica password = %q, want region-A's", p)
	}
}

// TestRewriteHubSecretHostLeavesCredentialsAlone pins that the rewrite is a
// host transform and nothing else — it must never touch a credential, and must
// cover every host-bearing key the role-secrets.yaml connection contract can
// emit (`host`, the `uri` embedding it, `reflect.hostKeys`, and
// `hostPortKeys` in `<host>:5432` form) without knowing their names.
func TestRewriteHubSecretHostLeavesCredentialsAlone(t *testing.T) {
	const oldHost = "shared-pg-b-rw.shared-data.svc.cluster.local"
	const newHost = "shared-pg-b-mesh-rw.shared-data.svc.cluster.local"

	src := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "grafana-database-env", Namespace: sharedPGNamespace},
		Type:       corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"host":                     []byte(oldHost),
			"uri":                      []byte("postgresql://grafana:pw@" + oldHost + ":5432/grafana"),
			"GF_DATABASE_HOST":         []byte(oldHost + ":5432"), // hostPortKeys shape
			"GF_DATABASE_HOST_NO_PORT": []byte(oldHost),           // hostKeys shape
			"password":                 []byte("pw"),
			"username":                 []byte("grafana"),
			"dbname":                   []byte("grafana"),
			"port":                     []byte("5432"),
		},
	}
	out := rewriteHubSecretHost(src, oldHost, newHost)

	for _, k := range []string{"host", "GF_DATABASE_HOST_NO_PORT"} {
		if v := string(out.Data[k]); v != newHost {
			t.Errorf("%s = %q, want %q", k, v, newHost)
		}
	}
	if v := string(out.Data["GF_DATABASE_HOST"]); v != newHost+":5432" {
		t.Errorf("GF_DATABASE_HOST = %q, want the host:port form rewritten", v)
	}
	if v := string(out.Data["uri"]); v != "postgresql://grafana:pw@"+newHost+":5432/grafana" {
		t.Errorf("uri = %q, want the embedded host rewritten", v)
	}
	// Credentials and non-host values pass through untouched.
	for k, want := range map[string]string{"password": "pw", "username": "grafana", "dbname": "grafana", "port": "5432"} {
		if v := string(out.Data[k]); v != want {
			t.Errorf("%s = %q, want %q untouched — the rewrite must only re-point hosts", k, v, want)
		}
	}
	// The source must not be mutated (it is region-A's authoritative object).
	if v := string(src.Data["host"]); v != oldHost {
		t.Errorf("source host mutated to %q — rewriteHubSecretHost must deep-copy", v)
	}
}
