// Package controller — useraccess-controller reconciler.
//
// Reconciles UserAccess.access.openova.io/v1alpha1 CRs into
// RoleBinding / ClusterRoleBinding objects on the same K8s cluster.
//
// Per docs/INVIOLABLE-PRINCIPLES.md #3 + ADR-0001 §2.3 amendment:
// K8s-to-K8s reconciliation is an in-cluster controller's job, not a
// Crossplane Composition. This controller REPLACES the broken
// `useraccess.compose.openova.io` Composition that uses
// provider-kubernetes (which is not installed on any production
// Sovereign — caught in the EPIC-0 audit, slice C5 #1095, P0).
//
// Wire shape mirrors the existing CRD at
//
//	platform/crossplane-claims/chart/templates/xrds/useraccess.yaml
//
// The controller treats UserAccess as cluster-scoped (it spans many
// namespaces by design) and writes per-(application × namespace × role)
// RoleBindings owned by the UserAccess CR via ownerReferences. A
// `namespaces: ["*"]` entry is materialized as a ClusterRoleBinding
// instead.
//
// Scope-matching uses internal/labels — Manara DNA: AND within a
// UserAccess.spec.scopes[], OR across UserAccess CRs. The full
// catalog-tier role inheritance lands in EPIC-3 (#1098); slice C5 ships
// the wire-level reconciler with the role enum {admin, editor, viewer}
// → ClusterRole `openova:application-<role>` mapping.
package controller

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// UserAccess GVK / GVR — pinned to the CRD shipped by
// platform/crossplane-claims chart. A drift here vs the chart YAML is
// a packaging bug and surfaces as a 404 on the dynamic client at
// startup. The shape (group/version/resource) is part of the CRD's
// public contract — see also catalyst-api's
// internal/handler/user_access.go which uses the same constants.
const (
	UserAccessGroup    = "access.openova.io"
	UserAccessVersion  = "v1alpha1"
	UserAccessKind     = "UserAccess"
	UserAccessResource = "useraccesses"
)

// UserAccessGVR returns the cluster-scoped GroupVersionResource the
// dynamic client uses to watch / get / patch UserAccess CRs.
func UserAccessGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{
		Group:    UserAccessGroup,
		Version:  UserAccessVersion,
		Resource: UserAccessResource,
	}
}

// UserAccessGVK returns the corresponding GroupVersionKind — used when
// constructing ownerReferences on materialized RoleBindings so K8s
// garbage-collects the bindings on UserAccess deletion.
func UserAccessGVK() schema.GroupVersionKind {
	return schema.GroupVersionKind{
		Group:   UserAccessGroup,
		Version: UserAccessVersion,
		Kind:    UserAccessKind,
	}
}

// Role enum constants — the three role short-forms the CRD's
// `spec.applications[].role` enum allows. The controller rewrites these
// to canonical ClusterRole names via clusterRoleForRole().
const (
	RoleAdmin  = "admin"
	RoleEditor = "editor"
	RoleViewer = "viewer"
)

// clusterRoleForRole returns the canonical openova:application-<role>
// ClusterRole name that a RoleBinding's roleRef points at. The set is
// declared in
//
//	platform/crossplane-claims/chart/templates/clusterroles.yaml
//
// — those three ClusterRoles are part of the bp-crossplane-claims
// blueprint and are present on every Sovereign that has the User
// Access primitive enabled.
func clusterRoleForRole(role string) string {
	return "openova:application-" + role
}

// vClusterNamespace returns the K8s namespace that hosts a vCluster on
// the parent cluster. Per docs/EPICS-1-6-unified-design.md §1.3 + the
// existing Crossplane Composition convention, vClusters live in
// `vcluster-<name>` namespaces.
func vClusterNamespace(name string) string {
	return "vcluster-" + name
}

// Wildcard namespace token — when a UserAccess application entry sets
// `namespaces: ["*"]`, the controller materializes a
// ClusterRoleBinding instead of a per-namespace RoleBinding. This
// matches the design doc §6.3 wildcard scope semantic.
const NamespaceWildcard = "*"

// Subject kinds.
const (
	SubjectKindUser  = "User"
	SubjectKindGroup = "Group"
)

// Labels written onto every materialized RoleBinding /
// ClusterRoleBinding. Per docs/EPICS-1-6-unified-design.md §1.1 the
// `openova.io/managed-by` value is what `mutate-add-openova-labels`
// (slice E1) and the compliance evaluator (EPIC-1) key off when
// auditing GitOps lineage.
const (
	LabelManagedBy      = "openova.io/managed-by"
	LabelManagedByValue = "useraccess-controller"
	LabelUserAccessName = "access.openova.io/useraccess"
	LabelUserAccessApp  = "access.openova.io/application"
	LabelUserAccessRole = "access.openova.io/role"
	LabelSovereignKey   = "catalyst.openova.io/sovereign"
)
