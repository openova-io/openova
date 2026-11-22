// organization_end.go — an Organization minted for a marketplace tenant ENDS
// with that tenant.
//
// The marketplace door creates the Organization CR through the provisioning
// service on tenant.created (core/services/provisioning/handlers/
// organization_create.go, openova.io/source=tenant-created-event). Everything
// the platform then builds for it is removed by this controller's deletion
// branch in every region, and the chargeback product closes the customer, ONLY
// when that CR is deleted. Before this file nothing deleted it: on a
// cancellation the provisioning service pruned its Git tree and stopped, and
// the Organization stayed whole in both regions and stayed metered and billed
// (measured on the two-region environment, 22 November 2026: two cancelled
// Organizations still Ready 17 hours after their teardown commit). The
// sovereign-admin door deletes its own CR (catalyst-api
// deleteOrgOrganizationCR); this is the marketplace door's equivalent.
//
// Trigger: provision.tenant_removed, which the provisioning service publishes
// AFTER its Git prune (the #4459/R17 order: nothing left in Git can re-create
// what the cascade removes) with {id, slug, ended_at}. The CR is deleted here,
// by the controller that reconciles it, not by the service: a service writes
// no new object to the API (scripts/check-no-direct-cluster-write.py), and the
// CR cannot go under Flux because three components write it after it is
// created (this controller, the provisioning tenantPublic patch, the
// catalyst-api suspend route).
//
// SELECTOR — it can never end another Organization:
//   - the CR is addressed by its exact cluster-scoped name (= the slug);
//   - it must carry openova.io/source=tenant-created-event AND
//     openova.io/tenant-id=<the removed tenant's id>: a CR of the
//     sovereign-admin door, or one minted later for another tenant under the
//     same slug, is left alone (and so is the org-controller's own
//     tenant.deleted echo, whose id is the slug);
//   - the delete carries a UID precondition, so a CR re-created between the
//     read and the delete is refused by the apiserver and left alone.
//
// SAFE TO RUN TWICE: an absent CR, or one already marked for deletion, is a
// no-op; the ended-at annotation is written only when absent, so a redelivery
// never moves the instant the Organization ended. A refused delete is written
// as Removed=False/RemovalIncomplete on the CR and the message is redelivered.

package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	orgapi "github.com/openova-io/openova/core/controllers/organization/internal/orgapi"
	"github.com/openova-io/openova/core/controllers/pkg/natsbus"
)

const (
	// marketplaceSourceLabel / marketplaceSource mark a CR the provisioning
	// service minted on tenant.created.
	marketplaceSourceLabel = "openova.io/source"
	marketplaceSource      = "tenant-created-event"
	// tenantIDLabel carries the tenant-service id the CR was minted for.
	tenantIDLabel = "openova.io/tenant-id"
	// EndedAtAnnotation is the instant the Organization ended — the tenant's
	// cancellation. The chargeback product closes the customer at it
	// (products/chargeback/internal/adapter/openova/orgsync.go), falling back
	// to metadata.deletionTimestamp when it is absent.
	EndedAtAnnotation = "orgs.openova.io/ended-at"
)

// Outcomes of endMarketplaceOrganization.
const (
	orgEndDeleted = "deleted"        // this call marked the CR for deletion
	orgEndEnding  = "already-ending" // the CR already carried a deletionTimestamp
	orgEndAbsent  = "absent"         // no CR of that name
	orgEndNotOurs = "not-ours"       // a CR of that name was not minted for this tenant
)

// endMarketplaceOrganization deletes the Organization CR minted for tenantID
// under slug, after stamping the instant it ended. A non-nil error means the
// Organization may still be live and the caller must retry.
func endMarketplaceOrganization(ctx context.Context, c client.Client, slug, tenantID string, endedAt time.Time) (string, error) {
	slug = strings.ToLower(strings.TrimSpace(slug))
	tenantID = strings.TrimSpace(tenantID)
	if slug == "" || tenantID == "" {
		return orgEndNotOurs, nil
	}
	var org orgapi.Organization
	if err := c.Get(ctx, types.NamespacedName{Name: slug}, &org); err != nil {
		if apierrors.IsNotFound(err) {
			return orgEndAbsent, nil
		}
		return "", fmt.Errorf("read Organization %s: %w", slug, err)
	}
	if org.Labels[marketplaceSourceLabel] != marketplaceSource || org.Labels[tenantIDLabel] != tenantID {
		return orgEndNotOurs, nil
	}
	if !org.DeletionTimestamp.IsZero() {
		return orgEndEnding, nil
	}
	if org.Annotations[EndedAtAnnotation] == "" {
		base := org.DeepCopyObject().(*orgapi.Organization)
		if org.Annotations == nil {
			org.Annotations = map[string]string{}
		}
		org.Annotations[EndedAtAnnotation] = endedAt.UTC().Format(time.RFC3339)
		if err := c.Patch(ctx, &org, client.MergeFrom(base)); err != nil {
			if apierrors.IsNotFound(err) {
				return orgEndAbsent, nil
			}
			return "", fmt.Errorf("stamp %s on Organization %s: %w", EndedAtAnnotation, slug, err)
		}
	}
	uid := org.UID
	if err := c.Delete(ctx, &org, client.Preconditions{UID: &uid}); err != nil {
		switch {
		case apierrors.IsNotFound(err):
			return orgEndAbsent, nil
		case apierrors.IsConflict(err):
			// UID precondition failed: the name now belongs to a CR this read
			// did not see. Not ours to delete.
			return orgEndNotOurs, nil
		}
		if merr := markRemovalIncomplete(ctx, c, &org, "the Organization (its delete was refused)", err); merr != nil && !apierrors.IsNotFound(merr) {
			return "", fmt.Errorf("delete Organization %s: %w (and its Removed condition: %v)", slug, err, merr)
		}
		return "", fmt.Errorf("delete Organization %s: %w", slug, err)
	}
	return orgEndDeleted, nil
}

// HandleTenantRemoved reacts to provision.tenant_removed: it ends the
// Organization the provisioning service minted for the removed tenant.
// Returning an error Naks the message (redelivered after the natsbus backoff).
func (b *NATSBridge) HandleTenantRemoved(ctx context.Context, ev *natsbus.Event) error {
	if ev == nil {
		return nil
	}
	var payload struct {
		ID      string `json:"id"`
		Slug    string `json:"slug"`
		EndedAt string `json:"ended_at"`
	}
	if err := json.Unmarshal(ev.Data, &payload); err != nil {
		b.Log.Error(err, "tenant_removed: malformed Data payload — ack to skip", "event_id", ev.ID)
		return nil
	}
	endedAt := ev.Timestamp
	if t, err := time.Parse(time.RFC3339, strings.TrimSpace(payload.EndedAt)); err == nil {
		endedAt = t
	}
	if endedAt.IsZero() {
		endedAt = time.Now()
	}
	outcome, err := endMarketplaceOrganization(ctx, b.Client, payload.Slug, payload.ID, endedAt)
	if err != nil {
		b.Log.Error(err, "tenant_removed: Organization NOT ended — it stays in every region and stays metered until this succeeds; redelivering",
			"slug", payload.Slug, "tenant_id", payload.ID, "event_id", ev.ID)
		return err
	}
	b.Log.Info("tenant_removed: Organization end", "slug", payload.Slug, "tenant_id", payload.ID,
		"outcome", outcome, "ended_at", endedAt.UTC().Format(time.RFC3339), "event_id", ev.ID)
	return nil
}
