// Package orgapi defines the Go types mirroring the
// orgs.openova.io/v1 Organization CRD that organization-controller
// reconciles. These mirror products/catalyst/chart/crds/organization.yaml
// 1:1; if either side changes, both sides must change in the same PR.
//
// Per ADR-0001 §2.7 the Organization is the K8s-native parent of the
// vCluster + Keycloak group + Gitea Org triplet — no Postgres
// `tenants` table, no parallel state store. Status fields surface
// reconciliation progress for the operator console.
package orgapi

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// GroupVersion is the API group/version for the Organization kind.
var GroupVersion = schema.GroupVersion{Group: "orgs.openova.io", Version: "v1"}

// SchemeBuilder is used to add the Organization types to a runtime.Scheme.
var SchemeBuilder = runtime.NewSchemeBuilder(addKnownTypes)

// AddToScheme registers Organization{,List} with the supplied Scheme so
// the controller-runtime manager and its caches can serialize the kind.
var AddToScheme = SchemeBuilder.AddToScheme

func addKnownTypes(scheme *runtime.Scheme) error {
	scheme.AddKnownTypes(GroupVersion, &Organization{}, &OrganizationList{})
	metav1.AddToGroupVersion(scheme, GroupVersion)
	return nil
}

// Organization is the cluster-scoped CR. See §3.2.1 of
// docs/EPICS-1-6-unified-design.md.
//
// +kubebuilder:resource:scope=Cluster
type Organization struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   OrganizationSpec   `json:"spec"`
	Status OrganizationStatus `json:"status,omitempty"`
}

// OrganizationList is the standard list wrapper.
type OrganizationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Organization `json:"items"`
}

// OrganizationSpec is the user-supplied desired state.
type OrganizationSpec struct {
	// Slug is the canonical lowercase identifier (3-32 chars). Used as
	// the vCluster name, Keycloak group leaf, and Gitea Org name.
	Slug string `json:"slug"`

	// DisplayName is the human-readable name (e.g. "ACME Corp").
	DisplayName string `json:"displayName"`

	// Kind is "customer" (external) or "internal" (department).
	Kind string `json:"kind"`

	// Tier is "sme" or "corporate". Drives DMZ-vCluster auto-provisioning
	// in EPIC-5 — out of scope here.
	Tier string `json:"tier"`

	// BillingMode is "real" | "chargeback" | "showback".
	BillingMode string `json:"billingMode"`

	// SovereignRef is the FQDN of the Sovereign hosting this Org.
	SovereignRef string `json:"sovereignRef"`

	// ParentOrg is the slug of a parent Organization (rare; corporate only).
	ParentOrg string `json:"parentOrg,omitempty"`

	// DefaultEnvironmentType is one of prod|stg|uat|dev|poc.
	DefaultEnvironmentType string `json:"defaultEnvironmentType,omitempty"`

	// Owners is the initial owner roster.
	Owners []OrganizationOwner `json:"owners"`

	// Identity holds optional federation config — empty means use the
	// Sovereign's own Keycloak realm.
	Identity OrganizationIdentity `json:"identity,omitempty"`

	// TenantPublic optionally exposes the Org's installed product on a
	// per-tenant public hostname. When set the organization-controller
	// renders a Gateway-API HTTPRoute in the Org's namespace pointing at
	// the supplied backend Service. When empty (the zero value) the
	// controller skips the HTTPRoute step — Orgs that don't yet have a
	// product installed (or that are accessed only via the per-Sovereign
	// console wildcard `*.<sovFQDN>`) keep working unchanged.
	//
	// The motivating use case (issue #1629 follow-up) is the
	// `<slug>.omani.homes` family of tenant hostnames: PowerDNS now
	// resolves them via the org-pool parent zone reconciler, but no
	// HTTPRoute was attaching them to the tenant's WordPress install.
	// Without this struct that traffic 404s at the Cilium Gateway.
	TenantPublic OrganizationTenantPublic `json:"tenantPublic,omitempty"`
}

// OrganizationTenantPublic is the per-tenant public-hostname binding
// the organization-controller renders into an HTTPRoute on Ready Orgs.
//
// All fields are optional at the CRD level — the controller treats an
// empty ParentDomain as "do not render". Defaulting rules:
//
//   - Subdomain defaults to spec.slug.
//   - BackendPort defaults to 80 (the conventional HTTP port WordPress,
//     Nextcloud, GitLab, BookStack, and Ghost all listen on inside the
//     vCluster).
//
// Per docs/INVIOLABLE-PRINCIPLES.md #4 no value is hardcoded inside the
// renderer — every knob flows through the CR.
type OrganizationTenantPublic struct {
	// ParentDomain is the apex zone the per-tenant hostname lives
	// under (e.g. "omani.homes"). Sovereign-wide parentZones lists the
	// pool of valid candidates; this field picks one specific apex per
	// Organization. Required to render the HTTPRoute — empty disables
	// the whole TenantPublic feature for this Org.
	ParentDomain string `json:"parentDomain,omitempty"`

	// Subdomain is the leftmost label of the per-tenant hostname.
	// Defaults to spec.slug when empty so the canonical
	// `<slug>.<parentDomain>` hostname renders without extra config.
	Subdomain string `json:"subdomain,omitempty"`

	// BackendService is the Service name the HTTPRoute routes "/" to —
	// e.g. `wordpress` for an in-cluster WordPress install, or the
	// vCluster-synced `wordpress-x-<slug>-x-vcluster` name when the
	// product lives inside a vCluster. The Service MUST resolve in the
	// Org's host namespace (= spec.slug) so the HTTPRoute backendRefs
	// don't need cross-namespace ReferenceGrants.
	BackendService string `json:"backendService,omitempty"`

	// BackendPort is the Service port number to route to. Defaults to
	// 80 when zero.
	BackendPort int32 `json:"backendPort,omitempty"`

	// Product is an operator-meaningful tag carried on the rendered
	// HTTPRoute's labels (e.g. "wordpress", "nextcloud", "gitlab").
	// Surfaced on the access-matrix UI so operators can filter routes
	// by installed product. Optional — empty just omits the label.
	Product string `json:"product,omitempty"`
}

// OrganizationOwner is an entry in spec.owners.
type OrganizationOwner struct {
	Email string `json:"email"`
	Role  string `json:"role"` // owner|admin|developer|viewer
}

// OrganizationIdentity holds federation config; preserves unknown
// fields per the CRD's x-kubernetes-preserve-unknown-fields.
type OrganizationIdentity struct {
	FederationProvider string                     `json:"federationProvider,omitempty"`
	FederationConfig   OrganizationFederationConf `json:"federationConfig,omitempty"`
}

// OrganizationFederationConf is the federation config struct mirroring
// the CRD shape. Plaintext secrets NEVER come through this struct —
// only a SealedSecret reference.
//
// Slice F2 (#1098) extends the field set with the OIDC endpoint URLs
// and tenant identifier needed by the Keycloak IdP CRUD path. The
// existing Issuer + ClientID + ClientSecretRef trio remains the
// minimum-viable shape for generic OIDC federation; the four new
// fields (TenantID, AuthorizationURL, TokenURL, JwksURL) are required
// for Azure-AD federation specifically since Microsoft's OIDC
// discovery endpoint scopes per-tenant. ClaimMappers carries the
// upstream-claim → Catalyst-attribute table that Keycloak materializes
// as IdentityProviderMappers (one per row).
type OrganizationFederationConf struct {
	Issuer          string                          `json:"issuer,omitempty"`
	ClientID        string                          `json:"clientId,omitempty"`
	ClientSecretRef OrganizationClientSecretRefSpec `json:"clientSecretRef,omitempty"`

	// TenantID is the Azure AD tenant UUID (or equivalent for other
	// IdPs). Empty for generic OIDC IdPs. Used solely as a tag on
	// the rendered Keycloak Config map for operator-debug; Keycloak
	// itself derives the tenant from the discovery URLs.
	TenantID string `json:"tenantId,omitempty"`

	// AuthorizationURL is the OIDC `authorization_endpoint`. For
	// Azure AD: https://login.microsoftonline.com/{tenantId}/oauth2/v2.0/authorize
	AuthorizationURL string `json:"authorizationUrl,omitempty"`

	// TokenURL is the OIDC `token_endpoint`.
	TokenURL string `json:"tokenUrl,omitempty"`

	// JwksURL is the OIDC `jwks_uri`. Keycloak validates ID-token
	// signatures against the JWKs published here.
	JwksURL string `json:"jwksUrl,omitempty"`

	// ClaimMappers maps upstream IdP claims (oid/upn/groups for Azure AD;
	// sub/email/groups for generic OIDC) to Catalyst's internal claim
	// shape. Each entry materializes as one Keycloak
	// IdentityProviderMapper of type `oidc-user-attribute-idp-mapper`.
	ClaimMappers []OrganizationClaimMapper `json:"claimMappers,omitempty"`
}

// OrganizationClaimMapper is one row of the upstream-claim → Catalyst-
// attribute mapping table.
type OrganizationClaimMapper struct {
	// Src is the upstream IdP claim name (e.g. "oid", "upn", "groups").
	Src string `json:"src"`
	// Dest is the Catalyst user-attribute name (e.g. "openova.io/external-id",
	// "email", "openova.io/groups"). Matches Keycloak's
	// `user.attribute` config key on `oidc-user-attribute-idp-mapper`.
	Dest string `json:"dest"`
}

// OrganizationClientSecretRefSpec is a SealedSecret pointer (name + key).
// The K8s Secret named `Name` MUST live in the catalyst-controllers
// namespace (where organization-controller runs) so the controller's
// ClusterRole `secrets:get` rule can read the value at reconcile time.
type OrganizationClientSecretRefSpec struct {
	Name string `json:"name,omitempty"`
	Key  string `json:"key,omitempty"`
}

// OrganizationStatus is the controller-managed reconciliation summary.
type OrganizationStatus struct {
	VCluster           VClusterStatus      `json:"vcluster,omitempty"`
	KeycloakGroup      KeycloakGroupStatus `json:"keycloakGroup,omitempty"`
	PerOrgRealm        PerOrgRealmStatus   `json:"perOrgRealm,omitempty"`
	GiteaOrg           GiteaOrgStatus      `json:"giteaOrg,omitempty"`
	IacBootstrap       IacBootstrapStatus  `json:"iacBootstrap,omitempty"`
	Conditions         []Condition         `json:"conditions,omitempty"`
	ObservedGeneration int64               `json:"observedGeneration,omitempty"`
}

// PerOrgRealmStatus surfaces the per-Org Keycloak realm provisioning
// state (#3084 Part 2). The organization-controller find-or-creates a
// realm named after the Org slug so Tier-3 per-Org SSO has an isolation
// boundary distinct from the Sovereign realm (where the Keycloak GROUP
// lives — see KeycloakGroupStatus). The controller transitions Ready on
// a successful EnsureRealm, Failed on a hard error (with `lastError`
// populated + a controller-runtime requeue), or Disabled when the
// per-Org-realm feature flag is off.
//
// The `realmName` equals the Org slug on success — surfaced so the
// operator console can deep-link to `auth.<sov>/admin/<realmName>/console`.
type PerOrgRealmStatus struct {
	// RealmName is the provisioned realm name (== spec.slug on success).
	RealmName string `json:"realmName,omitempty"`

	// State is one of Ready | Failed | Disabled.
	State string `json:"state,omitempty"`

	// LastError carries the most recent failure message when State=Failed.
	// Truncated to 1024 chars to keep the CR under the etcd cap.
	LastError string `json:"lastError,omitempty"`
}

// IacBootstrapStatus surfaces the per-Org IaC repo bootstrap state
// (ADR-0009 + G117.3 / W2.C3). The controller transitions through
// Pending → Provisioning → Ready on success, or → Failed on hard error
// (with `lastError` populated and a controller-runtime requeue).
//
// The `repoURL` is the canonical HTTPS URL surfaced on the operator
// console's "View IaC repo" link. `robotUsername` lets the operator
// grep audit logs by author (e.g. when a misconfigured PR lands).
type IacBootstrapStatus struct {
	// State is one of Pending | Provisioning | Ready | Failed.
	State string `json:"state,omitempty"`

	// RepoURL is the canonical HTTPS URL of the per-Org IaC repo —
	// e.g. "https://gitea.t01.omani.works/acme/iac".
	RepoURL string `json:"repoURL,omitempty"`

	// RobotUsername is the per-Org Gitea robot user's login name —
	// `<slug>-iac-bot`.
	RobotUsername string `json:"robotUsername,omitempty"`

	// LastError carries the most recent failure message when State=Failed.
	// Truncated to 1024 chars so the CR fits comfortably under the
	// 1MiB etcd cap even with thousands of consecutive failures.
	LastError string `json:"lastError,omitempty"`
}

// VClusterStatus surfaces vCluster reconciliation state.
type VClusterStatus struct {
	Name        string `json:"name,omitempty"`
	HostCluster string `json:"hostCluster,omitempty"`
	Phase       string `json:"phase,omitempty"` // Pending|Provisioning|Ready|Failed
}

// KeycloakGroupStatus surfaces the Keycloak group ID + path + realm.
type KeycloakGroupStatus struct {
	ID    string `json:"id,omitempty"`
	Path  string `json:"path,omitempty"`
	Realm string `json:"realm,omitempty"`
}

// GiteaOrgStatus lists the Gitea Org name and its reconciled repos.
type GiteaOrgStatus struct {
	Name  string   `json:"name,omitempty"`
	Repos []string `json:"repos,omitempty"`
}

// Condition is the standard K8s-style condition.
type Condition struct {
	Type               string      `json:"type"`
	Status             string      `json:"status"` // True|False|Unknown
	Reason             string      `json:"reason,omitempty"`
	Message            string      `json:"message,omitempty"`
	LastTransitionTime metav1.Time `json:"lastTransitionTime,omitempty"`
}

// DeepCopyObject implements runtime.Object — required so the
// controller-runtime client can manipulate Organizations through the
// generic interface. Generated DeepCopy code is intentionally inlined
// here (a 4-field struct doesn't justify pulling in deepcopy-gen).
func (o *Organization) DeepCopyObject() runtime.Object {
	if o == nil {
		return nil
	}
	out := &Organization{}
	o.DeepCopyInto(out)
	return out
}

// DeepCopyInto copies the receiver into out.
func (o *Organization) DeepCopyInto(out *Organization) {
	*out = *o
	out.TypeMeta = o.TypeMeta
	o.ObjectMeta.DeepCopyInto(&out.ObjectMeta)
	o.Spec.DeepCopyInto(&out.Spec)
	o.Status.DeepCopyInto(&out.Status)
}

// DeepCopyObject for OrganizationList.
func (l *OrganizationList) DeepCopyObject() runtime.Object {
	if l == nil {
		return nil
	}
	out := &OrganizationList{}
	out.TypeMeta = l.TypeMeta
	l.ListMeta.DeepCopyInto(&out.ListMeta)
	if l.Items != nil {
		out.Items = make([]Organization, len(l.Items))
		for i := range l.Items {
			l.Items[i].DeepCopyInto(&out.Items[i])
		}
	}
	return out
}

// DeepCopyInto for OrganizationSpec.
func (s *OrganizationSpec) DeepCopyInto(out *OrganizationSpec) {
	*out = *s
	if s.Owners != nil {
		out.Owners = make([]OrganizationOwner, len(s.Owners))
		copy(out.Owners, s.Owners)
	}
	s.Identity.DeepCopyInto(&out.Identity)
}

// DeepCopyInto for OrganizationIdentity. Slice F2 added the
// ClaimMappers slice on OrganizationFederationConf so a flat shallow
// copy no longer suffices.
func (i *OrganizationIdentity) DeepCopyInto(out *OrganizationIdentity) {
	*out = *i
	if i.FederationConfig.ClaimMappers != nil {
		out.FederationConfig.ClaimMappers = make([]OrganizationClaimMapper, len(i.FederationConfig.ClaimMappers))
		copy(out.FederationConfig.ClaimMappers, i.FederationConfig.ClaimMappers)
	}
}

// DeepCopyInto for OrganizationStatus.
func (s *OrganizationStatus) DeepCopyInto(out *OrganizationStatus) {
	*out = *s
	if s.Conditions != nil {
		out.Conditions = make([]Condition, len(s.Conditions))
		for i := range s.Conditions {
			out.Conditions[i] = s.Conditions[i]
			out.Conditions[i].LastTransitionTime = *s.Conditions[i].LastTransitionTime.DeepCopy()
		}
	}
	if s.GiteaOrg.Repos != nil {
		out.GiteaOrg.Repos = make([]string, len(s.GiteaOrg.Repos))
		copy(out.GiteaOrg.Repos, s.GiteaOrg.Repos)
	}
}
