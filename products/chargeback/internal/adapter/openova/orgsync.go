package openova

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/dynamic/dynamicinformer"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"

	"github.com/openova-io/openova/products/chargeback/internal/crypto"
	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// OrganizationGVR is the Organization CRD's group/version/resource
// (products/catalyst/chart/crds/organization.yaml).
var OrganizationGVR = schema.GroupVersionResource{Group: "orgs.openova.io", Version: "v1", Resource: "organizations"}

// SourceKindOrg is the cost-source kind of the auto-created per-Organization
// platform source the platform collector writes into.
const SourceKindOrg = "openova-org"

// OrgSync lists+watches Organization CRs and mirrors them into the
// chargeback application's customers (ADR-0014 D2): slug = Org slug,
// kind = organization, billing_mode from spec.billingMode, admin_email from
// the owner roster (blank-pending when absent). spec.costSources[] become
// cost_sources rows; a deleted Organization SUSPENDS its customer — history
// is billing data and is never deleted.
type OrgSync struct {
	Dyn      dynamic.Interface
	Core     kubernetes.Interface
	Repo     Repository
	Keys     *crypto.Keyring
	Verifier Verifier // optional; nil leaves declared sources pending
	Metrics  *metrics.Registry
	Resync   time.Duration // informer resync period; 0 = 1h

	loggedAbsent bool
}

func (s *OrgSync) metricsReg() *metrics.Registry {
	if s.Metrics != nil {
		return s.Metrics
	}
	return metrics.Default
}

func (s *OrgSync) resync() time.Duration {
	if s.Resync > 0 {
		return s.Resync
	}
	return time.Hour
}

// Run blocks until ctx is done. When the Organization CRD is not served
// (the standalone D5 placement, or a Sovereign mid-bootstrap) it logs once
// and idles, probing every five minutes until the CRD appears.
func (s *OrgSync) Run(ctx context.Context) {
	for ctx.Err() == nil {
		if err := s.probe(ctx); err != nil {
			if !s.loggedAbsent {
				slog.Info("openova adapter: Organization CRD is not served; the Organization sync idles until it appears", "gvr", OrganizationGVR.String(), "error", err)
				s.loggedAbsent = true
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Minute):
			}
			continue
		}
		s.loggedAbsent = false
		s.runInformer(ctx)
	}
}

// probe checks the Organization resource is served at all.
func (s *OrgSync) probe(ctx context.Context) error {
	_, err := s.Dyn.Resource(OrganizationGVR).List(ctx, metav1.ListOptions{Limit: 1})
	return err
}

// runInformer drives the list+watch until ctx is done.
func (s *OrgSync) runInformer(ctx context.Context) {
	factory := dynamicinformer.NewFilteredDynamicSharedInformerFactory(s.Dyn, s.resync(), metav1.NamespaceAll, nil)
	inf := factory.ForResource(OrganizationGVR).Informer()
	handle := func(obj any) {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return
		}
		if err := s.SyncOrganization(ctx, u); err != nil {
			slog.Warn("openova adapter: organization sync failed; the watch continues", "org", u.GetName(), "error", err)
			s.metricsReg().Inc("chargeback_adapter_org_sync_total", "Organization sync events by result", map[string]string{"result": "error"}, 1)
			return
		}
		s.metricsReg().Inc("chargeback_adapter_org_sync_total", "Organization sync events by result", map[string]string{"result": "ok"}, 1)
	}
	_, err := inf.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    handle,
		UpdateFunc: func(_, newObj any) { handle(newObj) },
		DeleteFunc: func(obj any) {
			if d, ok := obj.(cache.DeletedFinalStateUnknown); ok {
				obj = d.Obj
			}
			u, ok := obj.(*unstructured.Unstructured)
			if !ok {
				return
			}
			if err := s.SuspendOrganization(ctx, u); err != nil {
				slog.Warn("openova adapter: suspend on organization delete failed", "org", u.GetName(), "error", err)
				s.metricsReg().Inc("chargeback_adapter_org_sync_total", "Organization sync events by result", map[string]string{"result": "suspend-error"}, 1)
				return
			}
			s.metricsReg().Inc("chargeback_adapter_org_sync_total", "Organization sync events by result", map[string]string{"result": "suspended"}, 1)
		},
	})
	if err != nil {
		slog.Error("openova adapter: register organization handler", "error", err)
		return
	}
	slog.Info("openova adapter: organization sync started", "gvr", OrganizationGVR.String(), "resync", s.resync())
	inf.Run(ctx.Done())
}

// orgFields is the subset of an Organization CR the sync reads.
type orgFields struct {
	Slug        string
	DisplayName string
	BillingMode string
	AdminEmail  string
	CostSources []costSourceSpec
}

type costSourceSpec struct {
	Kind          string
	Region        string
	ProjectID     string
	CredentialRef *credentialRef
}

type credentialRef struct {
	Name string
	Key  string
}

func readOrg(u *unstructured.Unstructured) (orgFields, error) {
	var f orgFields
	f.Slug, _, _ = unstructured.NestedString(u.Object, "spec", "slug")
	if f.Slug == "" {
		f.Slug = u.GetName()
	}
	if f.Slug == "" {
		return f, errors.New("organization has neither spec.slug nor a name")
	}
	f.DisplayName, _, _ = unstructured.NestedString(u.Object, "spec", "displayName")
	if f.DisplayName == "" {
		f.DisplayName = f.Slug
	}
	f.BillingMode, _, _ = unstructured.NestedString(u.Object, "spec", "billingMode")
	switch f.BillingMode {
	case "real", "chargeback", "showback":
	default:
		// Unknown or absent → showback: visibility without invoicing is
		// the safe floor; the CRD enum makes this unreachable via the API.
		f.BillingMode = "showback"
	}
	owners, _, _ := unstructured.NestedSlice(u.Object, "spec", "owners")
	first := ""
	for _, o := range owners {
		m, ok := o.(map[string]any)
		if !ok {
			continue
		}
		email, _ := m["email"].(string)
		role, _ := m["role"].(string)
		if email == "" {
			continue
		}
		if first == "" {
			first = email
		}
		if role == "owner" {
			f.AdminEmail = email
			break
		}
	}
	if f.AdminEmail == "" {
		f.AdminEmail = first // blank-pending when the roster is empty
	}
	srcs, _, _ := unstructured.NestedSlice(u.Object, "spec", "costSources")
	for _, raw := range srcs {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		cs := costSourceSpec{}
		cs.Kind, _ = m["kind"].(string)
		cs.Region, _ = m["region"].(string)
		cs.ProjectID, _ = m["projectId"].(string)
		if ref, ok := m["credentialRef"].(map[string]any); ok {
			name, _ := ref["name"].(string)
			key, _ := ref["key"].(string)
			if name != "" || key != "" {
				cs.CredentialRef = &credentialRef{Name: name, Key: key}
			}
		}
		f.CostSources = append(f.CostSources, cs)
	}
	return f, nil
}

// SyncOrganization upserts the customer + sources for one Organization CR.
func (s *OrgSync) SyncOrganization(ctx context.Context, u *unstructured.Unstructured) error {
	f, err := readOrg(u)
	if err != nil {
		return err
	}
	c, err := s.Repo.GetCustomerBySlug(ctx, f.Slug)
	switch {
	case errors.Is(err, store.ErrNotFound):
		c, err = s.Repo.CreateCustomer(ctx, store.CustomerInput{
			Slug:        f.Slug,
			Name:        f.DisplayName,
			AdminEmail:  f.AdminEmail,
			Kind:        "organization",
			OrgSlug:     f.Slug,
			BillingMode: f.BillingMode,
		})
		if err != nil {
			return fmt.Errorf("create customer: %w", err)
		}
		if err := s.Repo.SetCustomerStatus(ctx, c.ID, "active"); err != nil {
			return fmt.Errorf("activate customer: %w", err)
		}
		slog.Info("openova adapter: organization synced as new customer", "org", f.Slug, "customer", c.ID, "billing_mode", f.BillingMode)
	case err != nil:
		return fmt.Errorf("get customer: %w", err)
	default:
		p := store.CustomerPatch{}
		changed := false
		if c.Name != f.DisplayName {
			p.Name, changed = &f.DisplayName, true
		}
		if f.AdminEmail != "" && !strings.EqualFold(c.AdminEmail, f.AdminEmail) {
			p.AdminEmail, changed = &f.AdminEmail, true
		}
		if c.BillingMode != f.BillingMode {
			p.BillingMode, changed = &f.BillingMode, true
		}
		if c.OrgSlug == nil || *c.OrgSlug != f.Slug {
			p.OrgSlug, changed = &f.Slug, true
		}
		if changed {
			if c, err = s.Repo.UpdateCustomer(ctx, c.ID, p); err != nil {
				return fmt.Errorf("update customer: %w", err)
			}
		}
		if c.Status != "active" {
			// The CR exists (again) — a suspended or pending customer
			// resumes; its history was kept across the suspension.
			if err := s.Repo.SetCustomerStatus(ctx, c.ID, "active"); err != nil {
				return fmt.Errorf("reactivate customer: %w", err)
			}
		}
	}

	// The per-Organization platform source (one auto-created; the platform
	// collector writes into it). Nothing external to verify — it is marked
	// verified so `collecting` reads true.
	src, _, err := s.Repo.UpsertSource(ctx, c.ID, SourceKindOrg, "", f.Slug)
	if err != nil {
		return fmt.Errorf("upsert platform source: %w", err)
	}
	if src.Status != "verified" {
		if err := s.Repo.SetSourceVerified(ctx, src.ID, ""); err != nil {
			return fmt.Errorf("verify platform source: %w", err)
		}
	}

	for _, cs := range f.CostSources {
		if err := s.syncCostSource(ctx, c, f.Slug, cs); err != nil {
			slog.Warn("openova adapter: cost source sync failed; continuing with the next one", "org", f.Slug, "project", cs.ProjectID, "error", err)
		}
	}
	return nil
}

// syncCostSource mirrors one spec.costSources[] entry into a cost_sources
// row, resolving credentialRef from the named Secret in the Organization's
// host namespace (= slug), read-only.
func (s *OrgSync) syncCostSource(ctx context.Context, c store.Customer, slug string, cs costSourceSpec) error {
	if cs.Kind != "huawei-project" {
		return fmt.Errorf("unsupported cost source kind %q", cs.Kind)
	}
	region := strings.TrimSpace(cs.Region)
	projectID := strings.TrimSpace(cs.ProjectID)
	if region == "" || projectID == "" {
		return errors.New("cost source needs region and projectId")
	}
	src, created, err := s.Repo.UpsertSource(ctx, c.ID, cs.Kind, region, projectID)
	if err != nil {
		return fmt.Errorf("upsert source: %w", err)
	}
	if created {
		slog.Info("openova adapter: declared cost source registered", "org", slug, "region", region, "project", projectID)
	}
	if cs.CredentialRef == nil {
		return nil // credential arrives through the UI/API instead (ADR-0014 D8a)
	}
	if cs.CredentialRef.Name == "" || cs.CredentialRef.Key == "" {
		return errors.New("credentialRef needs both name and key")
	}
	if s.Core == nil {
		return errors.New("no core clientset to read the credential Secret")
	}
	sec, err := s.Core.CoreV1().Secrets(slug).Get(ctx, cs.CredentialRef.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("read credential secret %s/%s: %w", slug, cs.CredentialRef.Name, err)
	}
	raw, ok := sec.Data[cs.CredentialRef.Key]
	if !ok {
		return fmt.Errorf("secret %s/%s has no key %q", slug, cs.CredentialRef.Name, cs.CredentialRef.Key)
	}
	ak, sk, err := parseAKSK(raw)
	if err != nil {
		return fmt.Errorf("secret %s/%s key %q: %w", slug, cs.CredentialRef.Name, cs.CredentialRef.Key, err)
	}
	defer zero(sk)
	if src.AccessKey == ak && src.Status == "verified" {
		return nil // already synced and healthy — a resync must not mint a new credential
	}
	enc, err := s.Keys.Seal(sk)
	if err != nil {
		return fmt.Errorf("seal credential: %w", err)
	}
	cred, err := s.Repo.CreateCredential(ctx, c.ID, ak, enc)
	if err != nil {
		return fmt.Errorf("store credential: %w", err)
	}
	if src.CredentialID != nil && *src.CredentialID != cred.ID {
		_ = s.Repo.MarkCredentialRotated(ctx, *src.CredentialID)
	}
	if err := s.Repo.SetSourceCredential(ctx, src.ID, cred.ID); err != nil {
		return fmt.Errorf("link credential: %w", err)
	}
	if s.Verifier == nil {
		return nil // stays pending until verified through the API
	}
	if verr := s.Verifier.VerifyProject(ctx, region, projectID, ak, string(sk)); verr != nil {
		msg := strings.ReplaceAll(verr.Error(), string(sk), "[redacted]")
		if serr := s.Repo.SetSourceFailed(ctx, src.ID, msg); serr != nil {
			return serr
		}
		return fmt.Errorf("verification failed: %s", msg)
	}
	if err := s.Repo.SetSourceVerified(ctx, src.ID, ""); err != nil {
		return err
	}
	slog.Info("openova adapter: declared cost source verified", "org", slug, "region", region, "project", projectID)
	return nil
}

// SuspendOrganization marks the customer of a deleted Organization
// suspended. Deletion never deletes — statements and the usage ledger are
// billing history.
func (s *OrgSync) SuspendOrganization(ctx context.Context, u *unstructured.Unstructured) error {
	f, err := readOrg(u)
	if err != nil {
		return err
	}
	c, err := s.Repo.GetCustomerBySlug(ctx, f.Slug)
	if errors.Is(err, store.ErrNotFound) {
		return nil // never synced; nothing to suspend
	}
	if err != nil {
		return err
	}
	if c.Status == "suspended" {
		return nil
	}
	if err := s.Repo.SetCustomerStatus(ctx, c.ID, "suspended"); err != nil {
		return err
	}
	slog.Info("openova adapter: organization deleted; customer suspended (history kept)", "org", f.Slug, "customer", c.ID)
	return nil
}

// parseAKSK reads a credential Secret value: JSON
// {"accessKey":"...","secretKey":"..."} (snake_case accepted), or the colon
// form ACCESSKEY:SECRETKEY.
func parseAKSK(raw []byte) (string, []byte, error) {
	trimmed := strings.TrimSpace(string(raw))
	if strings.HasPrefix(trimmed, "{") {
		var v struct {
			AccessKey  string `json:"accessKey"`
			SecretKey  string `json:"secretKey"`
			AccessKey2 string `json:"access_key"`
			SecretKey2 string `json:"secret_key"`
		}
		if err := json.Unmarshal([]byte(trimmed), &v); err != nil {
			return "", nil, fmt.Errorf("credential value is not valid JSON: %w", err)
		}
		ak, sk := v.AccessKey, v.SecretKey
		if ak == "" {
			ak = v.AccessKey2
		}
		if sk == "" {
			sk = v.SecretKey2
		}
		if ak == "" || sk == "" {
			return "", nil, errors.New("credential JSON needs accessKey and secretKey")
		}
		return ak, []byte(sk), nil
	}
	ak, sk, found := strings.Cut(trimmed, ":")
	if !found || ak == "" || sk == "" {
		return "", nil, errors.New("credential value must be JSON {accessKey, secretKey} or ACCESSKEY:SECRETKEY")
	}
	return ak, []byte(sk), nil
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
