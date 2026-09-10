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
const SourceKindOrg = store.SourceKindOrg

// SourceKindPlatform is the kind of the ONE internal source the Sovereign's
// own platform footprint is recorded on (no customer).
const SourceKindPlatform = store.SourceKindPlatform

// OrgSync lists+watches Organization CRs and mirrors them into the
// chargeback application's customers (ADR-0014 D2): slug = Org slug,
// kind = organization, billing_mode from spec.billingMode, admin_email from
// the owner roster (blank-pending when absent), plan_slug from spec.planSlug
// (DESIGN.md §2.8 "Plan revenue"). spec.costSources[] become cost_sources
// rows; a deleted Organization SUSPENDS its customer — history is billing
// data and is never deleted.
//
// The Sovereign's OWN Organization (spec.kind = internal) is NOT a customer
// (DESIGN.md §2): it gets no customer row; its platform footprint is
// recorded on the internal openova-platform source, which only Allocation
// reads. A customer an earlier version synced it as is retired to a plain
// external customer (its cloud sources stay).
//
// It also owns the TWO platform rate cards, one per billing shape (DESIGN.md
// §2.9a): "OpenOva plans" for a committed plan (s/m/l/xl) and "Organization
// PAYG" for the uncapped flexi plan, which has no bundle to sell and is
// billed off its k8s.* meters instead. Each is created once when absent, and
// an Organization's openova-org SOURCE is pointed at the one its plan calls
// for. Neither is ever re-created or re-priced, and a source an operator put
// on some other book is never re-assigned.
type OrgSync struct {
	Dyn      dynamic.Interface
	Core     kubernetes.Interface
	Repo     Repository
	Keys     *crypto.Keyring
	Verifier Verifier // optional; nil leaves declared sources pending
	Metrics  *metrics.Registry
	Resync   time.Duration // informer resync period; 0 = 1h

	// OverheadSink receives the slug of the Sovereign's OWN Organization
	// (spec.kind = internal) when one is observed. The platform collector
	// uses it to attribute unlabelled platform namespaces to the
	// platform-overhead line instead of dropping them (ADR-0014 D3 case 3,
	// #6850). Optional — nil disables overhead attribution entirely.
	OverheadSink interface{ SetOverheadOrg(string) }

	// Now is the clock (tests); nil = time.Now.
	Now func() time.Time

	loggedAbsent bool
}

func (s *OrgSync) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
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
	// Internal marks the Sovereign's OWN Organization (spec.kind = internal)
	// as opposed to a tenant Organization. ADR-0014 D3 case 3 puts the
	// Sovereign's own platform consumption on a platform-overhead line rather
	// than a tenant Org row, and this is the discriminator (#6850).
	Internal bool
	// PlanSlug is spec.planSlug lower-cased; empty defaults to "s" exactly as
	// the org-controller's planQuota renderer does. The Sovereign's own
	// Organization buys no plan from itself, so Internal forces "" (no plan
	// line, and the pool it feeds is never inflated by a plan).
	PlanSlug string
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
	orgKind, _, _ := unstructured.NestedString(u.Object, "spec", "kind")
	f.Internal = orgKind == "internal"
	planSlug, _, _ := unstructured.NestedString(u.Object, "spec", "planSlug")
	f.PlanSlug = store.NormalizePlanSlug(planSlug)
	if f.PlanSlug == "" {
		f.PlanSlug = "s" // the org-controller's default for a CR without spec.planSlug
	}
	if f.Internal {
		f.PlanSlug = ""
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
	if f.Internal {
		return s.syncInternalOrganization(ctx, f)
	}
	// The two platform rate cards, one per billing shape: the plans book
	// prices the plan.<slug> line a sized Organization carries, the
	// pay-per-use book prices the k8s.* meters a flexi Organization carries
	// instead. Both are ensured on every sync (one indexed lookup by name
	// each) so a book the operator removed comes back on the next event; a
	// failure here is logged and leaves the source on whatever book it
	// already had rather than blocking the customer itself.
	books := s.ensurePlatformBooks(ctx, f.Slug)
	resumed := false
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
			PlanSlug:    f.PlanSlug,
		})
		if err != nil {
			return fmt.Errorf("create customer: %w", err)
		}
		if err := s.Repo.SetCustomerStatus(ctx, c.ID, "active"); err != nil {
			return fmt.Errorf("activate customer: %w", err)
		}
		slog.Info("openova adapter: organization synced as new customer", "org", f.Slug, "customer", c.ID, "billing_mode", f.BillingMode, "plan", f.PlanSlug)
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
		// DESIGN.md §8: billing_mode is now DERIVED from the commercial
		// fields, and CustomerInput/CustomerPatch translate this legacy
		// value through store.CommercialFromBillingMode — the same mapping
		// the migration used. Comparing the derived value keeps the CR
		// authoritative over the coarse mode while leaving a finer operator
		// choice inside it (stripe gateway vs bank transfer) alone.
		if c.BillingMode != f.BillingMode {
			p.BillingMode, changed = &f.BillingMode, true
		}
		if c.OrgSlug == nil || *c.OrgSlug != f.Slug {
			p.OrgSlug, changed = &f.Slug, true
		}
		if c.PlanSlug != f.PlanSlug {
			p.PlanSlug, changed = &f.PlanSlug, true
		}
		if changed {
			if c, err = s.Repo.UpdateCustomer(ctx, c.ID, p); err != nil {
				return fmt.Errorf("update customer: %w", err)
			}
		}
		if c.Status != "active" {
			// The CR exists (again) — a suspended or pending customer
			// resumes; its history was kept across the suspension.
			resumed = c.Status == "suspended"
			if err := s.Repo.SetCustomerStatus(ctx, c.ID, "active"); err != nil {
				return fmt.Errorf("reactivate customer: %w", err)
			}
		}
	}

	// The per-Organization platform source (one auto-created; the platform
	// collector writes into it). Nothing external to verify — it is marked
	// verified so `collecting` reads true. Bookless → the plan book; an
	// explicit assignment (any platform book, including a clone the operator
	// negotiated) is never touched.
	src, _, err := s.Repo.UpsertSource(ctx, c.ID, SourceKindOrg, "", f.Slug)
	if err != nil {
		return fmt.Errorf("upsert platform source: %w", err)
	}
	// A disabled source stays disabled: a resync must never undo the
	// operator's decommission.
	if src.Status != "verified" && src.Status != store.StatusDisabled {
		if err := s.Repo.SetSourceVerified(ctx, src.ID, ""); err != nil {
			return fmt.Errorf("verify platform source: %w", err)
		}
	}
	if err := s.assignPlatformBook(ctx, f, src, books); err != nil {
		return err
	}
	if resumed {
		// A suspended Organization had no pods and paid no plan; the
		// collector recomputes from the source's collection stamp, so
		// moving the stamp to the resume instant keeps the suspended gap
		// out of the ledger instead of billing the plan across it.
		if err := s.Repo.SetSourceCollected(ctx, src.ID, s.now()); err != nil {
			return fmt.Errorf("stamp platform source on resume: %w", err)
		}
		slog.Info("openova adapter: organization resumed; platform collection restarts now", "org", f.Slug, "customer", c.ID)
	}

	for _, cs := range f.CostSources {
		if err := s.syncCostSource(ctx, c, f.Slug, cs); err != nil {
			slog.Warn("openova adapter: cost source sync failed; continuing with the next one", "org", f.Slug, "project", cs.ProjectID, "error", err)
		}
	}
	return nil
}

// managedBook is one of the two platform rate cards the sync owns, in the
// order they are ensured.
type managedBook struct {
	Name   string
	Ensure func(context.Context) (store.PriceBook, bool, error)
}

func (s *OrgSync) managedBooks() []managedBook {
	return []managedBook{
		{Name: store.PlanBookName, Ensure: s.Repo.EnsurePlanBook},
		{Name: store.PAYGBookName, Ensure: s.Repo.EnsurePAYGBook},
	}
}

// platformBooks maps each managed book's name to its id. A name is absent
// when that book could not be ensured this pass.
type platformBooks map[string]string

// ensurePlatformBooks creates whichever of the two managed rate cards is
// missing and returns their ids. A book that cannot be ensured is logged and
// left out: the next sync tries again, and in the meantime a source keeps the
// book it already has rather than losing its rates.
func (s *OrgSync) ensurePlatformBooks(ctx context.Context, org string) platformBooks {
	out := platformBooks{}
	for _, b := range s.managedBooks() {
		pb, created, err := b.Ensure(ctx)
		if err != nil {
			slog.Warn("openova adapter: platform price book unavailable; the source keeps the book it has", "org", org, "book", b.Name, "error", err)
			continue
		}
		if created {
			slog.Info("openova adapter: platform price book created", "book", pb.Name, "id", pb.ID, "items", len(pb.Items))
		}
		if pb.Scope != store.LayerPlatform {
			// A book of the wrong scope can never be assigned to a platform
			// source (SetSourcePriceBook refuses it), and returning that
			// error here would wedge the whole Organization sync on every
			// event. Skip the book instead and name the fix: the source
			// keeps whatever it has, and everything else about the
			// Organization still syncs.
			slog.Warn("openova adapter: platform price book has the wrong scope and cannot be assigned; set its scope to platform", "book", pb.Name, "id", pb.ID, "scope", pb.Scope)
			continue
		}
		out[b.Name] = pb.ID
	}
	return out
}

// holds reports whether id is one of the managed books.
func (p platformBooks) holds(id string) bool {
	for _, v := range p {
		if v == id {
			return true
		}
	}
	return false
}

// assignPlatformBook points an Organization's platform source at the rate
// card its plan calls for: the plans book for a sized plan (and for an
// unknown or absent one, the fallback that was always there), the
// pay-per-use book for flexi. This is the whole of "never double charge" on
// the assignment side — a source can only ever be on ONE book, so a sized
// Organization is on the book where its meters carry no rate, and a flexi
// Organization is on the book where nothing prices a plan line it never
// emits.
//
// Three cases, in order:
//
//   - No book yet: assign the one the plan calls for.
//   - On the OTHER managed book: the Organization changed plan between flexi
//     and a sized plan, so re-point it. Without this the bill would not
//     follow the plan change.
//   - On any other book: an operator put it there — a negotiated clone, say —
//     and it is never touched.
func (s *OrgSync) assignPlatformBook(ctx context.Context, f orgFields, src store.CostSource, books platformBooks) error {
	name := store.BookForPlan(f.PlanSlug)
	want := books[name]
	if want == "" {
		return nil // not ensured this pass; the next sync assigns it
	}
	if src.PriceBookID != nil {
		if *src.PriceBookID == want {
			return nil
		}
		if !books.holds(*src.PriceBookID) {
			return nil // the operator chose this book; a resync does not overrule it
		}
	}
	if err := s.Repo.SetSourcePriceBook(ctx, src.ID, want); err != nil {
		return fmt.Errorf("assign the %q book to the platform source: %w", name, err)
	}
	slog.Info("openova adapter: platform source assigned its plan's price book", "org", f.Slug, "source", src.ID, "plan", f.PlanSlug, "book", name, "id", want)
	return nil
}

// syncInternalOrganization handles the Sovereign's OWN Organization
// (spec.kind = internal), which is not a customer (DESIGN.md §2): it tells
// the platform collector which slug carries the overhead line (#6850),
// retires the customer an earlier version synced it as — the customer stays
// as a plain external one with its cloud sources, its openova-org source
// becomes the internal source — and ensures the internal openova-platform
// source exists (customer NULL, internal, verified) for the collector to
// write the Sovereign's footprint to.
func (s *OrgSync) syncInternalOrganization(ctx context.Context, f orgFields) error {
	if s.OverheadSink != nil {
		s.OverheadSink.SetOverheadOrg(f.Slug)
	}
	c, err := s.Repo.GetCustomerBySlug(ctx, f.Slug)
	switch {
	case err == nil && c.Kind == "organization":
		if err := s.Repo.RetireOrganizationCustomer(ctx, c.ID); err != nil {
			return fmt.Errorf("retire the Sovereign's own Organization customer %s: %w", f.Slug, err)
		}
		slog.Info("openova adapter: the Sovereign's own Organization is not a customer; its customer row is now a plain external customer and its platform source the internal source", "org", f.Slug, "customer", c.ID)
	case err != nil && !errors.Is(err, store.ErrNotFound):
		return fmt.Errorf("get customer: %w", err)
	}
	src, created, err := s.Repo.EnsureInternalSource(ctx, f.Slug)
	if err != nil {
		return fmt.Errorf("ensure internal platform source: %w", err)
	}
	if created {
		slog.Info("openova adapter: internal platform source created for the Sovereign's own footprint", "org", f.Slug, "source", src.ID)
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
// billing history. The Sovereign's own Organization has no customer.
func (s *OrgSync) SuspendOrganization(ctx context.Context, u *unstructured.Unstructured) error {
	f, err := readOrg(u)
	if err != nil {
		return err
	}
	if f.Internal {
		return nil
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
