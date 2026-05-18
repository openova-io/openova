// sandbox_db.go — real handlers for sandbox.db.* (CNPG provisioning).
//
// Scope (architecture.md §3 + §5 + §7): apps the agent ships from inside
// a Sandbox occasionally need their own Postgres. Rather than per-Org
// shared databases (which would couple every app's downtime + tenancy
// surface), Sandbox provides ON-DEMAND CNPG Cluster CRs in the Sandbox's
// OWN namespace (`sandbox-<owner-uid>`). The upstream CNPG operator (the
// `bp-cnpg` Application Blueprint, `platform/cnpg/README.md`) reconciles
// those CRs into a real Postgres StatefulSet + Services + Secrets — the
// MCP server only mutates the CR; it never touches the underlying Pods.
//
// PR #1645 (Wave 8) wired gitea.* + k8s.read.* + session.* but left
// sandbox.db.* as not_implemented stubs. This file (Wave 11) ships the
// real handlers using the same dynamic-client pattern as k8s_read.go.
//
// Tools shipped here:
//
//   - sandbox.db.provision {name, plan?} → POST a Cluster CR
//   - sandbox.db.list                    → LIST Cluster CRs (sandbox ns)
//   - sandbox.db.get {name}              → GET one Cluster CR
//   - sandbox.db.drop {name}             → DELETE one Cluster CR
//   - sandbox.db.dump {name}             → POST a one-shot Backup CR
//
// All five are scoped to env.SandboxNamespace — the agent CANNOT direct
// the call at any other namespace inside the Org vcluster, even though
// the k8s SA the pod runs as may have broader read powers.
//
// Auth: same shape as PR #1645 (gitea.* + k8s.read.*) — the bearer's
// claims.OrgID must match env.OrgID, and every tool declares
// RequiredCapability="sandbox.db" so claims without that capability
// receive 403 from Registry.Call before reaching the handler.
package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// CNPG GVRs (group=postgresql.cnpg.io, version=v1). Centralised so a
// future operator version bump (v1 → v1beta1, etc.) is a one-line edit.
var (
	cnpgClusterGVR = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "clusters"}
	cnpgBackupGVR  = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "backups"}
)

// cnpgManagedByLabel marks every Cluster CR the MCP server creates so
// `sandbox.db.list` returns ONLY agent-provisioned databases (not
// e.g. a per-Sandbox metadata Cluster the controller might own later).
const (
	cnpgManagedByLabel = "openova.io/managed-by"
	cnpgManagedByValue = "openova-sandbox-mcp"
	cnpgSandboxIDLabel = "openova.io/sandbox-id"
)

// cnpgNameRE enforces a conservative naming surface. CNPG itself accepts
// any RFC-1123 label; we narrow to lowercase alnum + hyphen + 4..40 chars
// so collisions with the operator's own status-resources
// (`<cluster>-rw`, `<cluster>-r`, `<cluster>-1`) stay obvious.
var cnpgNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{2,39}$`)

// cnpgPlan binds the agent's `plan` argument to a CNPG resource shape.
// Wave 11 ships ONE plan (`default`); future waves can map plan IDs to
// instance count / storage / image without changing the tool surface.
type cnpgPlan struct {
	Instances    int    `json:"instances"`
	StorageSize  string `json:"storage_size"`
	StorageClass string `json:"storage_class,omitempty"`
	Image        string `json:"image"`
	Database     string `json:"database"`
}

// defaultCNPGPlan is the only plan Wave 11 honours: a single Pod, 5Gi
// PVC, postgres 16 from CNPG's published image, with a default app DB.
// Sized to be the cheapest useful sidecar — apps that need more should
// graduate to a per-Org bp-cnpg-pair via the marketplace.
func defaultCNPGPlan() cnpgPlan {
	return cnpgPlan{
		Instances:   1,
		StorageSize: "5Gi",
		Image:       "ghcr.io/cloudnative-pg/postgresql:16",
		Database:    "app",
	}
}

// --- argument types ----------------------------------------------------

type sandboxDBProvisionArgs struct {
	Name string `json:"name"`
	Plan string `json:"plan,omitempty"`
}

type sandboxDBNameOnlyArgs struct {
	Name string `json:"name"`
}

// --- helpers -----------------------------------------------------------

// requireSandboxNS is the canonical scope-gate: every sandbox.db.* tool
// MUST address the Sandbox's OWN namespace. The agent cannot pass a
// `namespace` argument; we read it from env.
func requireSandboxNS(ctx context.Context) (*Env, string, error) {
	env := EnvFrom(ctx)
	if env == nil {
		return nil, "", errors.New("sandbox.db: server env missing on context")
	}
	if env.SandboxNamespace == "" {
		return nil, "", errors.New("sandbox.db: server env has no SANDBOX_NAMESPACE")
	}
	return env, env.SandboxNamespace, nil
}

// validateCNPGName rejects names that would clash with operator-managed
// child resources (`<name>-rw`, `<name>-1`) or that aren't valid
// RFC-1123 labels.
func validateCNPGName(name string) error {
	if name == "" {
		return errors.New("`name` is required")
	}
	if !cnpgNameRE.MatchString(name) {
		return fmt.Errorf("`name` must match %s (lowercase alnum + hyphen, 4-40 chars, leading letter)", cnpgNameRE.String())
	}
	// CNPG sticks `-1`, `-2`, ... + `-rw`/`-ro`/`-r` on Service names; we
	// already cap at 40 so a 4-char suffix won't exceed the 63-char DNS
	// label limit.
	return nil
}

// connectionEnvelope is the structured response sandbox.db.provision and
// sandbox.db.get return so the agent can paste it directly into the
// app's env (`postgres://app:<pw>@<host>:<port>/<dbname>`). CNPG writes
// the password into Secret `<cluster>-app` (key=`password`) at install
// time — the MCP server NEVER returns the password itself; the agent
// mounts the Secret into its app pod.
type connectionEnvelope struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Database   string `json:"dbname"`
	User       string `json:"user"`
	SecretName string `json:"secretName"`
	SecretKey  string `json:"secretKey"`
	Cluster    string `json:"cluster"`
	Namespace  string `json:"namespace"`
}

// connectionFor renders the canonical envelope for a Cluster named `name`
// in `ns`. CNPG's defaults: write-Service = `<name>-rw`, port 5432, app
// Secret = `<name>-app` (keys: `username`, `password`, `pgpass`, `uri`).
func connectionFor(name, ns, database string) connectionEnvelope {
	return connectionEnvelope{
		Host:       fmt.Sprintf("%s-rw.%s.svc.cluster.local", name, ns),
		Port:       5432,
		Database:   database,
		User:       database, // CNPG default: owner == database name.
		SecretName: name + "-app",
		SecretKey:  "password",
		Cluster:    name,
		Namespace:  ns,
	}
}

// buildClusterCR constructs the Unstructured Cluster CR. Kept in its own
// function so the unit tests can assert the rendered shape without
// running against a live API server.
func buildClusterCR(env *Env, name string, plan cnpgPlan) *unstructured.Unstructured {
	labels := map[string]any{
		cnpgManagedByLabel: cnpgManagedByValue,
	}
	if env.SandboxID != "" {
		labels[cnpgSandboxIDLabel] = env.SandboxID
	}
	storage := map[string]any{
		"size": plan.StorageSize,
	}
	if plan.StorageClass != "" {
		storage["storageClass"] = plan.StorageClass
	}
	cr := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "postgresql.cnpg.io/v1",
			"kind":       "Cluster",
			"metadata": map[string]any{
				"name":      name,
				"namespace": env.SandboxNamespace,
				"labels":    labels,
				"annotations": map[string]any{
					"openova.io/created-by": "sandbox.db.provision",
				},
			},
			"spec": map[string]any{
				"instances": int64(plan.Instances),
				"imageName": plan.Image,
				"storage":   storage,
				"bootstrap": map[string]any{
					"initdb": map[string]any{
						"database": plan.Database,
						"owner":    plan.Database,
					},
				},
				"enableSuperuserAccess": false,
			},
		},
	}
	return cr
}

// buildBackupCR constructs the on-demand Backup CR sandbox.db.dump
// submits. The CNPG operator picks it up, runs `barman-cloud-backup`
// against the Cluster's `spec.backup.barmanObjectStore`, and writes the
// resulting URL into `status.destinationPath`. With no barmanObjectStore
// configured on a default-plan Cluster, the operator falls back to a
// volumeSnapshot — either way the Backup CR's status carries the result
// so the agent gets the same wire shape.
func buildBackupCR(ns, clusterName string) *unstructured.Unstructured {
	// Backup names must be unique per ns; tag with a UTC timestamp.
	name := fmt.Sprintf("%s-dump-%s", clusterName, time.Now().UTC().Format("20060102-150405"))
	return &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "postgresql.cnpg.io/v1",
			"kind":       "Backup",
			"metadata": map[string]any{
				"name":      name,
				"namespace": ns,
				"labels": map[string]any{
					cnpgManagedByLabel: cnpgManagedByValue,
				},
				"annotations": map[string]any{
					"openova.io/created-by": "sandbox.db.dump",
				},
			},
			"spec": map[string]any{
				"cluster": map[string]any{
					"name": clusterName,
				},
			},
		},
	}
}

// readCNPGStatusSummary distils the bits of Cluster.status the agent
// usually wants: phase, readyInstances, conditions[type=Ready], the
// rw/r/ro service names, and the bottom-line `currentPrimary`.
// We deliberately do NOT echo the entire status sub-object — it's
// noisy + version-volatile.
func readCNPGStatusSummary(obj map[string]any) map[string]any {
	st, _, _ := unstructured.NestedMap(obj, "status")
	if st == nil {
		return map[string]any{"phase": "Pending"}
	}
	out := map[string]any{}
	for _, k := range []string{
		"phase", "phaseReason", "instances", "readyInstances",
		"currentPrimary", "targetPrimary", "writeService", "readService",
	} {
		if v, ok := st[k]; ok {
			out[k] = v
		}
	}
	// Conditions: surface only [Ready] for brevity.
	if conds, found, _ := unstructured.NestedSlice(st, "conditions"); found {
		for _, c := range conds {
			cm, ok := c.(map[string]any)
			if !ok {
				continue
			}
			if cm["type"] == "Ready" {
				out["ready"] = cm
				break
			}
		}
	}
	return out
}

// --- sandbox.db.provision ----------------------------------------------

func sandboxDBProvision(ctx context.Context, raw json.RawMessage) (any, error) {
	var args sandboxDBProvisionArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("sandbox.db.provision: invalid arguments: %w", err)
	}
	if err := validateCNPGName(args.Name); err != nil {
		return nil, fmt.Errorf("sandbox.db.provision: %w", err)
	}
	plan := defaultCNPGPlan()
	// Wave 11 honours `plan` only as a free-form tag (only "default"
	// implemented). Future waves can plug a plan registry here.
	if args.Plan != "" && strings.ToLower(args.Plan) != "default" {
		return nil, fmt.Errorf("sandbox.db.provision: unknown plan %q (only `default` available)", args.Plan)
	}
	env, ns, err := requireSandboxNS(ctx)
	if err != nil {
		return nil, err
	}
	client, err := dynamicClient(ctx)
	if err != nil {
		return nil, err
	}
	cr := buildClusterCR(env, args.Name, plan)
	created, err := client.Resource(cnpgClusterGVR).Namespace(ns).
		Create(ctx, cr, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("sandbox.db.provision: create Cluster %s/%s: %w", ns, args.Name, err)
	}
	conn := connectionFor(args.Name, ns, plan.Database)
	return map[string]any{
		"status":     "Provisioning",
		"connection": conn,
		"plan": map[string]any{
			"name":          "default",
			"instances":     plan.Instances,
			"storage_size":  plan.StorageSize,
			"image":         plan.Image,
			"database":      plan.Database,
			"storage_class": plan.StorageClass,
		},
		"resourceVersion": created.GetResourceVersion(),
		"uid":             string(created.GetUID()),
		"note":            "CNPG operator will reconcile this CR; poll `sandbox.db.get name=<n>` until status.phase == \"Cluster in healthy state\".",
	}, nil
}

func schemaSandboxDBProvision() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"name"},
		"properties": map[string]any{
			"name": map[string]any{
				"type":        "string",
				"description": "Cluster name (lowercase alnum + hyphen, 4-40 chars). Used as the Service prefix; `<name>-rw` is the writer endpoint.",
				"pattern":     cnpgNameRE.String(),
			},
			"plan": map[string]any{
				"type":        "string",
				"description": "Plan ID. Wave 11 only ships `default` (1 instance, 5Gi, postgres 16).",
				"enum":        []string{"default"},
			},
		},
		"additionalProperties": false,
	}
}

// --- sandbox.db.list ---------------------------------------------------

func sandboxDBList(ctx context.Context, _ json.RawMessage) (any, error) {
	_, ns, err := requireSandboxNS(ctx)
	if err != nil {
		return nil, err
	}
	client, err := dynamicClient(ctx)
	if err != nil {
		return nil, err
	}
	list, err := client.Resource(cnpgClusterGVR).Namespace(ns).
		List(ctx, metav1.ListOptions{
			LabelSelector: cnpgManagedByLabel + "=" + cnpgManagedByValue,
		})
	if err != nil {
		return nil, fmt.Errorf("sandbox.db.list: %w", err)
	}
	items := make([]map[string]any, 0, len(list.Items))
	for i := range list.Items {
		obj := list.Items[i].Object
		name, _, _ := unstructured.NestedString(obj, "metadata", "name")
		database, _, _ := unstructured.NestedString(obj, "spec", "bootstrap", "initdb", "database")
		if database == "" {
			database = "app"
		}
		items = append(items, map[string]any{
			"name":              name,
			"namespace":         ns,
			"creationTimestamp": list.Items[i].GetCreationTimestamp().Time.UTC().Format(time.RFC3339),
			"status":            readCNPGStatusSummary(obj),
			"connection":        connectionFor(name, ns, database),
		})
	}
	return map[string]any{
		"namespace": ns,
		"count":     len(items),
		"items":     items,
	}, nil
}

// --- sandbox.db.get ----------------------------------------------------

func sandboxDBGet(ctx context.Context, raw json.RawMessage) (any, error) {
	var args sandboxDBNameOnlyArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("sandbox.db.get: invalid arguments: %w", err)
	}
	if err := validateCNPGName(args.Name); err != nil {
		return nil, fmt.Errorf("sandbox.db.get: %w", err)
	}
	_, ns, err := requireSandboxNS(ctx)
	if err != nil {
		return nil, err
	}
	client, err := dynamicClient(ctx)
	if err != nil {
		return nil, err
	}
	obj, err := client.Resource(cnpgClusterGVR).Namespace(ns).
		Get(ctx, args.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("sandbox.db.get %s/%s: %w", ns, args.Name, err)
	}
	// Defence in depth: refuse to surface a Cluster that wasn't created
	// by the MCP server (e.g. operator-internal pair clusters), even if
	// the SA's RBAC let us read it. This stops a malicious sandbox from
	// fishing for credentials of a per-Org shared DB by guessing names.
	if obj.GetLabels()[cnpgManagedByLabel] != cnpgManagedByValue {
		return nil, fmt.Errorf("sandbox.db.get: Cluster %s/%s is not Sandbox-managed (no %s=%s label)",
			ns, args.Name, cnpgManagedByLabel, cnpgManagedByValue)
	}
	database, _, _ := unstructured.NestedString(obj.Object, "spec", "bootstrap", "initdb", "database")
	if database == "" {
		database = "app"
	}
	return map[string]any{
		"name":              obj.GetName(),
		"namespace":         ns,
		"creationTimestamp": obj.GetCreationTimestamp().Time.UTC().Format(time.RFC3339),
		"resourceVersion":   obj.GetResourceVersion(),
		"uid":               string(obj.GetUID()),
		"status":            readCNPGStatusSummary(obj.Object),
		"connection":        connectionFor(args.Name, ns, database),
	}, nil
}

func schemaSandboxDBNameOnly() map[string]any {
	return map[string]any{
		"type":     "object",
		"required": []string{"name"},
		"properties": map[string]any{
			"name": map[string]any{
				"type":    "string",
				"pattern": cnpgNameRE.String(),
			},
		},
		"additionalProperties": false,
	}
}

// --- sandbox.db.drop ---------------------------------------------------

func sandboxDBDrop(ctx context.Context, raw json.RawMessage) (any, error) {
	var args sandboxDBNameOnlyArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("sandbox.db.drop: invalid arguments: %w", err)
	}
	if err := validateCNPGName(args.Name); err != nil {
		return nil, fmt.Errorf("sandbox.db.drop: %w", err)
	}
	_, ns, err := requireSandboxNS(ctx)
	if err != nil {
		return nil, err
	}
	client, err := dynamicClient(ctx)
	if err != nil {
		return nil, err
	}
	// Refuse to delete Clusters we didn't create (parity with get).
	obj, err := client.Resource(cnpgClusterGVR).Namespace(ns).
		Get(ctx, args.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("sandbox.db.drop: lookup Cluster %s/%s: %w", ns, args.Name, err)
	}
	if obj.GetLabels()[cnpgManagedByLabel] != cnpgManagedByValue {
		return nil, fmt.Errorf("sandbox.db.drop: refusing to delete non-Sandbox Cluster %s/%s", ns, args.Name)
	}
	// Foreground deletion so the operator finalises cleanup (PVCs,
	// Services, Secrets) before the API server returns. Otherwise the
	// agent could race a `provision` with the same name and hit a
	// "PVC still exists" reconcile loop.
	policy := metav1.DeletePropagationForeground
	err = client.Resource(cnpgClusterGVR).Namespace(ns).
		Delete(ctx, args.Name, metav1.DeleteOptions{
			PropagationPolicy: &policy,
		})
	if err != nil {
		return nil, fmt.Errorf("sandbox.db.drop: delete Cluster %s/%s: %w", ns, args.Name, err)
	}
	return map[string]any{
		"status":    "Deleting",
		"name":      args.Name,
		"namespace": ns,
		"note":      "CNPG operator is cascading the delete (Pods, PVCs, Services, app Secret). Use `sandbox.db.list` to confirm removal.",
	}, nil
}

// --- sandbox.db.dump ---------------------------------------------------

// sandboxDBDumpResponse is the structured envelope the dump tool returns.
// `backup_name` lets the agent poll Backup status separately;
// `destination_path` is the configured S3 prefix (when set on the
// Cluster) so the agent can find the resulting WAL+base files without
// the Backup status hop on a successful run.
type sandboxDBDumpResponse struct {
	Status          string `json:"status"`
	Cluster         string `json:"cluster"`
	BackupName      string `json:"backupName"`
	Namespace       string `json:"namespace"`
	DestinationPath string `json:"destinationPath,omitempty"`
	Note            string `json:"note"`
}

func sandboxDBDump(ctx context.Context, raw json.RawMessage) (any, error) {
	var args sandboxDBNameOnlyArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, fmt.Errorf("sandbox.db.dump: invalid arguments: %w", err)
	}
	if err := validateCNPGName(args.Name); err != nil {
		return nil, fmt.Errorf("sandbox.db.dump: %w", err)
	}
	_, ns, err := requireSandboxNS(ctx)
	if err != nil {
		return nil, err
	}
	client, err := dynamicClient(ctx)
	if err != nil {
		return nil, err
	}
	// Same Sandbox-managed guard as drop/get — the dump endpoint reads
	// from a real Cluster, and a malicious sandbox could otherwise force
	// a backup against a per-Org pair Cluster (cheap CPU/IO attack).
	cluster, err := client.Resource(cnpgClusterGVR).Namespace(ns).
		Get(ctx, args.Name, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("sandbox.db.dump: lookup Cluster %s/%s: %w", ns, args.Name, err)
	}
	if cluster.GetLabels()[cnpgManagedByLabel] != cnpgManagedByValue {
		return nil, fmt.Errorf("sandbox.db.dump: refusing to dump non-Sandbox Cluster %s/%s", ns, args.Name)
	}
	destPath, _, _ := unstructured.NestedString(cluster.Object,
		"spec", "backup", "barmanObjectStore", "destinationPath")

	backupCR := buildBackupCR(ns, args.Name)
	created, err := client.Resource(cnpgBackupGVR).Namespace(ns).
		Create(ctx, backupCR, metav1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("sandbox.db.dump: create Backup %s/%s: %w", ns, args.Name, err)
	}
	return sandboxDBDumpResponse{
		Status:          "Started",
		Cluster:         args.Name,
		BackupName:      created.GetName(),
		Namespace:       ns,
		DestinationPath: destPath,
		Note: "CNPG operator will execute the backup via barman-cloud (or volumeSnapshot if no barmanObjectStore is configured). " +
			"Poll the Backup CR's status.phase via k8s.read.get group=postgresql.cnpg.io version=v1 resource=backups.",
	}, nil
}

