// organization_federation_test.go — slice F2 (#1098) coverage. The
// existing organization_controller_test.go already covers the
// no-federation steady-state path (TestReconcile_HappyPath asserts
// IdentityProviderConfigured=False/NoFederation conditions). The
// scenarios in this file exercise the federation-on path:
//
//   1. Org with spec.identity.federationProvider="azure-sso" → IdP +
//      N claim mappers reconciled; status surfaces True conditions.
//   2. Federation requested but clientSecretRef Secret missing →
//      requeue + SecretMissing condition.
//   3. Federation idempotency: re-reconcile a steady-state federated Org
//      should produce zero Delete calls and re-call Ensure (the fake
//      records calls but does no real network I/O — drift detection is
//      server-side, covered by F1 unit tests).
//   4. Delete-on-drop: Org with federationProvider previously set,
//      then cleared → reconciler issues Delete on the deterministic
//      alias (best-effort).

package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
)

// federatedSampleOrg returns a sample Org with Azure-SSO federation
// fully populated.
func federatedSampleOrg() *orgapi.Organization {
	o := sampleOrg()
	o.Spec.Identity = orgapi.OrganizationIdentity{
		FederationProvider: "azure-sso",
		FederationConfig: orgapi.OrganizationFederationConf{
			Issuer:           "https://login.microsoftonline.com/T/v2.0",
			ClientID:         "00000000-0000-0000-0000-aaaaaaaaaaaa",
			TenantID:         "00000000-0000-0000-0000-bbbbbbbbbbbb",
			AuthorizationURL: "https://login.microsoftonline.com/T/oauth2/v2.0/authorize",
			TokenURL:         "https://login.microsoftonline.com/T/oauth2/v2.0/token",
			JwksURL:          "https://login.microsoftonline.com/T/discovery/v2.0/keys",
			ClientSecretRef: orgapi.OrganizationClientSecretRefSpec{
				Name: "acme-azure-secret",
				Key:  "client-secret",
			},
			ClaimMappers: []orgapi.OrganizationClaimMapper{
				{Src: "oid", Dest: "openova.io/external-id"},
				{Src: "upn", Dest: "email"},
				{Src: "groups", Dest: "openova.io/groups"},
			},
		},
	}
	return o
}

// federationSecret returns the K8s Secret holding the OIDC client
// secret. The reconciler reads it via Get on namespace
// "catalyst-controllers" by default.
func federationSecret(name string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: "catalyst-controllers",
		},
		Data: map[string][]byte{
			"client-secret": []byte("test-client-secret-value"),
		},
	}
}

func TestReconcile_Federation_AzureSSO_HappyPath(t *testing.T) {
	t.Parallel()
	org := federatedSampleOrg()
	sec := federationSecret("acme-azure-secret")
	r, _, kc := makeReconciler(t, org, sec)
	r.FederationSecretNamespace = "catalyst-controllers"

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "acme"},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// IdP ensure called exactly once, mapper ensure called once per
	// ClaimMappers entry (3 in the fixture).
	if kc.idpEnsureCalls != 1 {
		t.Errorf("expected 1 idpEnsure call, got %d", kc.idpEnsureCalls)
	}
	if kc.mapperEnsureCalls != 3 {
		t.Errorf("expected 3 mapper ensure calls (1 per ClaimMappers entry), got %d", kc.mapperEnsureCalls)
	}
	// No DeleteIdentityProvider calls when federation is on.
	if kc.idpDeleteCalls != 0 {
		t.Errorf("federation-on path should not call Delete, got %d", kc.idpDeleteCalls)
	}

	// IdP stored under deterministic alias.
	idp, ok := kc.idps["azure-sso-acme"]
	if !ok {
		t.Fatalf("IdP not stored under expected alias, got %v", kc.idps)
	}
	if idp.Config["clientId"] != "00000000-0000-0000-0000-aaaaaaaaaaaa" {
		t.Errorf("clientId not propagated to Keycloak Config: %q", idp.Config["clientId"])
	}
	if idp.Config["clientSecret"] != "test-client-secret-value" {
		t.Errorf("clientSecret not resolved from K8s Secret: %q", idp.Config["clientSecret"])
	}
	if idp.Config["authorizationUrl"] != "https://login.microsoftonline.com/T/oauth2/v2.0/authorize" {
		t.Errorf("authorizationUrl not propagated: %q", idp.Config["authorizationUrl"])
	}
	if idp.Config["openova.tenantId"] != "00000000-0000-0000-0000-bbbbbbbbbbbb" {
		t.Errorf("tenantId tag not surfaced: %q", idp.Config["openova.tenantId"])
	}

	// Status conditions: Ready=True + IdentityProviderConfigured=True/AzureSSOConfigured
	// + IdentityProviderClaimMappersConfigured=True.
	var got orgapi.Organization
	if err := r.Get(context.Background(), client.ObjectKey{Name: "acme"}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	// G117.3 W2.C3 (#2742) added the IacRepoBootstrapped condition —
	// rendered as Status=False / Reason=BootstrapDisabled in unit tests
	// where the iac-bootstrap deps are not wired into the Reconciler.
	if len(got.Status.Conditions) != 4 {
		t.Fatalf("expected 4 conditions, got %d: %+v",
			len(got.Status.Conditions), got.Status.Conditions)
	}
	if got.Status.Conditions[1].Type != "IdentityProviderConfigured" ||
		got.Status.Conditions[1].Status != "True" ||
		got.Status.Conditions[1].Reason != "AzureSSOConfigured" {
		t.Errorf("expected IdentityProviderConfigured=True/AzureSSOConfigured, got %+v",
			got.Status.Conditions[1])
	}
	if got.Status.Conditions[2].Type != "IdentityProviderClaimMappersConfigured" ||
		got.Status.Conditions[2].Status != "True" {
		t.Errorf("expected mappers=True, got %+v", got.Status.Conditions[2])
	}
}

func TestReconcile_Federation_SecretMissing_Requeues(t *testing.T) {
	t.Parallel()
	org := federatedSampleOrg()
	// No federationSecret seeded — reconciler must surface SecretMissing.
	r, _, kc := makeReconciler(t, org)
	r.FederationSecretNamespace = "catalyst-controllers"

	res, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "acme"},
	})
	if err != nil {
		t.Fatalf("reconcile (secret-missing should requeue, not error): %v", err)
	}
	if res.RequeueAfter == 0 {
		t.Errorf("secret-missing path should requeue, got %v", res)
	}
	if kc.idpEnsureCalls != 0 {
		t.Errorf("secret-missing path should not call EnsureIdentityProvider, got %d", kc.idpEnsureCalls)
	}

	// Status surfaces SecretMissing as the failure reason.
	var got orgapi.Organization
	if err := r.Get(context.Background(), client.ObjectKey{Name: "acme"}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(got.Status.Conditions) == 0 {
		t.Fatal("no conditions written on failure path")
	}
	// fail() collapses to a single Ready=False with the reason.
	if got.Status.Conditions[0].Reason != "SecretMissing" {
		t.Errorf("expected SecretMissing reason, got %q", got.Status.Conditions[0].Reason)
	}
}

func TestReconcile_Federation_Idempotent(t *testing.T) {
	t.Parallel()
	org := federatedSampleOrg()
	sec := federationSecret("acme-azure-secret")
	r, _, kc := makeReconciler(t, org, sec)
	r.FederationSecretNamespace = "catalyst-controllers"

	// First reconcile populates everything.
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "acme"},
	}); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	firstIdPCalls := kc.idpEnsureCalls
	firstMapperCalls := kc.mapperEnsureCalls

	// Second reconcile: Ensure methods should still be CALLED (idempotency
	// is enforced server-side via byte-equal short-circuit covered by F1
	// unit tests). The fake counts every call. What we verify here is
	// that NO Delete fires and NO error surfaces.
	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "acme"},
	}); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if kc.idpDeleteCalls != 0 {
		t.Errorf("steady-state federated reconcile should not Delete, got %d", kc.idpDeleteCalls)
	}
	if kc.idpEnsureCalls != firstIdPCalls+1 {
		t.Errorf("expected one more idpEnsure on second reconcile, got %d (was %d)",
			kc.idpEnsureCalls, firstIdPCalls)
	}
	if kc.mapperEnsureCalls != firstMapperCalls+3 {
		t.Errorf("expected 3 more mapper ensures on second reconcile, got %d (was %d)",
			kc.mapperEnsureCalls, firstMapperCalls)
	}
}

func TestReconcile_Federation_Cleanup_OnDrop(t *testing.T) {
	t.Parallel()
	// Org with NO federationProvider — reconciler issues best-effort
	// DeleteIdentityProvider on each supported provider's deterministic
	// alias to handle the "operator dropped federation" path.
	org := sampleOrg()
	r, _, kc := makeReconciler(t, org)

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "acme"},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// Three Delete calls — one per supported provider's alias
	// (azure-sso-acme / okta-acme / generic-oidc-acme).
	if kc.idpDeleteCalls != 3 {
		t.Errorf("expected 3 best-effort delete calls (one per provider alias), got %d",
			kc.idpDeleteCalls)
	}
	if kc.idpEnsureCalls != 0 {
		t.Errorf("no-federation path must not Ensure, got %d", kc.idpEnsureCalls)
	}
}

func TestReconcile_Federation_OktaProvider(t *testing.T) {
	t.Parallel()
	org := federatedSampleOrg()
	org.Spec.Identity.FederationProvider = "okta"
	sec := federationSecret("acme-azure-secret")
	r, _, kc := makeReconciler(t, org, sec)
	r.FederationSecretNamespace = "catalyst-controllers"

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "acme"},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// Alias derived from provider — okta-acme, not azure-sso-acme.
	if _, ok := kc.idps["okta-acme"]; !ok {
		t.Errorf("expected idp alias 'okta-acme', got %v", kc.idps)
	}
	if _, ok := kc.idps["azure-sso-acme"]; ok {
		t.Errorf("did NOT expect 'azure-sso-acme' alias under okta provider")
	}

	// Reason should be OktaConfigured.
	var got orgapi.Organization
	if err := r.Get(context.Background(), client.ObjectKey{Name: "acme"}, &got); err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Status.Conditions[1].Reason != "OktaConfigured" {
		t.Errorf("expected reason=OktaConfigured for okta provider, got %q",
			got.Status.Conditions[1].Reason)
	}
}
