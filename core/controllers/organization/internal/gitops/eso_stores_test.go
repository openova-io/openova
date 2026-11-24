package gitops

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// SEC-205 — the External Secrets reader of an Organization namespace
// (esoStoresTemplate): ServiceAccount eso-reader and SecretStore openbao in
// the host-applied tree. Each object is decoded strictly, so a field the
// objects must not carry fails the decode, and compared whole with the value
// written out below.

type esoMeta struct {
	Name      string            `json:"name"`
	Namespace string            `json:"namespace"`
	Labels    map[string]string `json:"labels"`
}

type esoServiceAccount struct {
	APIVersion                   string  `json:"apiVersion"`
	Kind                         string  `json:"kind"`
	Metadata                     esoMeta `json:"metadata"`
	AutomountServiceAccountToken *bool   `json:"automountServiceAccountToken"`
}

type esoServiceAccountRef struct {
	Name string `json:"name"`
}

type esoKubernetesAuth struct {
	MountPath         string               `json:"mountPath"`
	Role              string               `json:"role"`
	ServiceAccountRef esoServiceAccountRef `json:"serviceAccountRef"`
}

type esoAuth struct {
	Kubernetes esoKubernetesAuth `json:"kubernetes"`
}

type esoVault struct {
	Server  string  `json:"server"`
	Path    string  `json:"path"`
	Version string  `json:"version"`
	Auth    esoAuth `json:"auth"`
}

type esoProvider struct {
	Vault esoVault `json:"vault"`
}

type esoStoreSpec struct {
	Provider esoProvider `json:"provider"`
}

type esoSecretStore struct {
	APIVersion string       `json:"apiVersion"`
	Kind       string       `json:"kind"`
	Metadata   esoMeta      `json:"metadata"`
	Spec       esoStoreSpec `json:"spec"`
}

func esoRender(t *testing.T, slug, plan string) map[string][]byte {
	t.Helper()
	out, err := Render(Inputs{Slug: slug, DisplayName: "Org " + slug, Tier: "org", PlanSlug: plan,
		SovereignFQDN: "x.example", HostCluster: "hz", VClusterChartVersion: "0.33.4"})
	if err != nil {
		t.Fatalf("Render(%s, %s): %v", slug, plan, err)
	}
	return out
}

// esoDocs splits the rendered file into its YAML documents.
func esoDocs(t *testing.T, out map[string][]byte, path string) []string {
	t.Helper()
	raw, ok := out[path]
	if !ok {
		t.Fatalf("missing %s", path)
	}
	return strings.Split(string(raw), "\n---\n")
}

func wantESOReader(slug string) esoServiceAccount {
	no := false
	return esoServiceAccount{
		APIVersion: "v1",
		Kind:       "ServiceAccount",
		Metadata: esoMeta{Name: "eso-reader", Namespace: slug, Labels: map[string]string{
			"openova.io/organization":     slug,
			"openova.io/managed-by":       "catalyst",
			"app.kubernetes.io/component": "eso-reader",
		}},
		AutomountServiceAccountToken: &no,
	}
}

func wantOpenBaoStore(slug string) esoSecretStore {
	return esoSecretStore{
		APIVersion: "external-secrets.io/v1beta1",
		Kind:       "SecretStore",
		Metadata: esoMeta{Name: "openbao", Namespace: slug, Labels: map[string]string{
			"openova.io/organization": slug,
			"openova.io/managed-by":   "catalyst",
		}},
		Spec: esoStoreSpec{Provider: esoProvider{Vault: esoVault{
			Server:  "http://openbao.openbao.svc.cluster.local:8200",
			Path:    "secret",
			Version: "v2",
			Auth: esoAuth{Kubernetes: esoKubernetesAuth{
				MountPath:         "kubernetes",
				Role:              "eso-ns",
				ServiceAccountRef: esoServiceAccountRef{Name: "eso-reader"},
			}},
		}}},
	}
}

// The file holds exactly two documents, the ServiceAccount and the
// SecretStore, each equal to its expected value, in the namespace named by
// the slug. No store for mount mounted/ is rendered.
func TestESOStores_ExactObjects(t *testing.T) {
	t.Parallel()
	for _, slug := range []string{"acme", "globex"} {
		out := esoRender(t, slug, "m")
		docs := esoDocs(t, out, SharedHostAppsDir+"/"+esoStoresDoc)
		if len(docs) != 2 {
			t.Fatalf("slug %s: %s holds %d documents, want 2 (ServiceAccount eso-reader, SecretStore openbao)\n%s",
				slug, esoStoresDoc, len(docs), out[SharedHostAppsDir+"/"+esoStoresDoc])
		}
		var sa esoServiceAccount
		if err := yaml.UnmarshalStrict([]byte(docs[0]), &sa); err != nil {
			t.Fatalf("slug %s: first document does not decode strictly as a ServiceAccount: %v\n%s", slug, err, docs[0])
		}
		if want := wantESOReader(slug); !reflect.DeepEqual(sa, want) {
			t.Errorf("slug %s: ServiceAccount:\n got %+v\nwant %+v", slug, sa, want)
		}
		if sa.AutomountServiceAccountToken == nil || *sa.AutomountServiceAccountToken {
			t.Errorf("slug %s: eso-reader must set automountServiceAccountToken: false", slug)
		}
		var store esoSecretStore
		if err := yaml.UnmarshalStrict([]byte(docs[1]), &store); err != nil {
			t.Fatalf("slug %s: second document does not decode strictly as a SecretStore: %v\n%s", slug, err, docs[1])
		}
		if want := wantOpenBaoStore(slug); !reflect.DeepEqual(store, want) {
			t.Errorf("slug %s: SecretStore:\n got %+v\nwant %+v", slug, store, want)
		}
	}
}

// The file renders for every plan, in the shared host-apps tree and in its
// region-baseline copy, byte for byte the same; both host-apps indexes list
// exactly their five documents; the boundary index and the apps tree (which
// is applied inside the vCluster) never name it.
func TestESOStores_RenderedHostSideForEveryPlan(t *testing.T) {
	t.Parallel()
	wantIndex := []string{"ciliumnetworkpolicy.yaml", "eso-stores.yaml", "org-fence-api.yaml", "org-fence.yaml", "provisioning-rbac.yaml"}
	for _, plan := range []string{"s", "m", "l", "xl", "flexi"} {
		out := esoRender(t, "acme", plan)
		shared, ok := out[SharedHostAppsDir+"/"+esoStoresDoc]
		if !ok {
			t.Fatalf("plan %s: missing %s/%s", plan, SharedHostAppsDir, esoStoresDoc)
		}
		copied, ok := out[RegionBaselineHostAppsDir+"/"+esoStoresDoc]
		if !ok {
			t.Fatalf("plan %s: missing %s/%s", plan, RegionBaselineHostAppsDir, esoStoresDoc)
		}
		if string(copied) != string(shared) {
			t.Errorf("plan %s: %s/%s differs from the shared copy:\n%s", plan, RegionBaselineHostAppsDir, esoStoresDoc, copied)
		}
		for _, index := range []string{SharedHostAppsDir, RegionBaselineHostAppsDir} {
			raw, ok := out[index+"/kustomization.yaml"]
			if !ok {
				t.Fatalf("plan %s: missing %s/kustomization.yaml", plan, index)
			}
			var got []string
			for name := range parseKustomizationResources(string(raw)) {
				got = append(got, name)
			}
			sort.Strings(got)
			if !reflect.DeepEqual(got, wantIndex) {
				t.Errorf("plan %s: %s/kustomization.yaml lists %v, want %v", plan, index, got, wantIndex)
			}
		}
		for _, index := range []string{"vcluster/kustomization.yaml", SharedAppsDir + "/kustomization.yaml", RegionBaselineAppsDir + "/kustomization.yaml"} {
			raw, ok := out[index]
			if !ok {
				t.Fatalf("plan %s: missing %s", plan, index)
			}
			if strings.Contains(string(raw), esoStoresDoc) {
				t.Errorf("plan %s: %s lists %s", plan, index, esoStoresDoc)
			}
		}
		for path := range out {
			if strings.HasSuffix(path, "/"+esoStoresDoc) && path != SharedHostAppsDir+"/"+esoStoresDoc && path != RegionBaselineHostAppsDir+"/"+esoStoresDoc {
				t.Errorf("plan %s: %s renders outside the host-apps trees", plan, path)
			}
		}
	}
}

// No rendered object of an Organization carries the label
// secrets.openova.io/legacy-store, names the cluster-wide store, or is a
// store of any kind other than the one SecretStore openbao: an Organization
// namespace never gets the cluster-wide store (ADR-0018 S10).
func TestESOStores_NoLegacyOrClusterStore(t *testing.T) {
	t.Parallel()
	for _, plan := range []string{"s", "m", "l", "xl", "flexi"} {
		out := esoRender(t, "acme", plan)
		stores := 0
		for path, raw := range out {
			s := string(raw)
			for _, banned := range []string{"secrets.openova.io/legacy-store", "ClusterSecretStore", "vault-region1", "openbao-mounted", "path: mounted"} {
				if strings.Contains(s, banned) {
					t.Errorf("plan %s: %s carries %q", plan, path, banned)
				}
			}
			stores += strings.Count(s, "\nkind: SecretStore\n")
		}
		// One SecretStore in the shared host-apps tree and one in its
		// region-baseline copy: the scan above read the file it judges.
		if stores != 2 {
			t.Errorf("plan %s: the rendered tree holds %d SecretStore documents, want 2 (host-apps and its region-baseline copy)", plan, stores)
		}
	}
}

// An Organization whose host-apps index was written before this file existed
// (and that the funnel extended with a route) gains the entry on the next
// reconcile, and keeps the funnel's entry.
func TestESOStores_MergedIntoAnExistingIndex(t *testing.T) {
	t.Parallel()
	existing := "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\nresources:\n" +
		"  - app-wordpress-hostroute.yaml\n  - ciliumnetworkpolicy.yaml\n  - org-fence-api.yaml\n  - org-fence.yaml\n  - provisioning-rbac.yaml\n"
	out := esoRender(t, "acme", "m")
	merged, changed := MergeHostAppsKustomizationIndex(existing, string(out[SharedHostAppsDir+"/kustomization.yaml"]))
	if !changed {
		t.Fatalf("merge into an index without %s reported no change", esoStoresDoc)
	}
	var got []string
	for name := range parseKustomizationResources(merged) {
		got = append(got, name)
	}
	sort.Strings(got)
	want := []string{"app-wordpress-hostroute.yaml", "ciliumnetworkpolicy.yaml", "eso-stores.yaml", "org-fence-api.yaml", "org-fence.yaml", "provisioning-rbac.yaml"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("merged index lists %v, want %v\n%s", got, want, merged)
	}
}
