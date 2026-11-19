// Package orgpromote picks the Organization provisioning records that the
// console pipeline holds at tenant_registered and runs the pipeline for them
// again, so that each one reaches done once its boundary is Ready.
//
// The pipeline (handler.runOrganizationPipeline, step 7) holds a record at
// tenant_registered until it observes the Organization's boundary Ready. The
// boundary comes up minutes after `POST /api/v1/organizations` has returned,
// and nothing ran the pipeline again: ReconcileAllPending, the re-run the hold
// was written for, has no caller outside the tests. So every Organization
// created through the console stayed at tenant_registered for ever, while its
// vCluster was Ready and its wildcard certificate issued (gate check NET-G07
// on hw307, 19 November 2026: vCluster Ready at 10:23:17, both certificates
// issued by 10:24:00, both records still tenant_registered at 10:51:48).
//
// A record is due only while its Organization CR exists and is not being
// deleted. Step 7 creates the CR when it is missing, so running a record whose
// CR was deleted would bring a deleted Organization back; such a record is
// left alone.
package orgpromote

import (
	"context"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/openova-io/openova/products/catalyst/bootstrap/api/internal/store"
)

// Due returns the records of pending that are held at tenant_registered and
// whose Organization CR is in listed (the CR is named after the lower-case
// subdomain) without a deletion timestamp. The order of pending is kept.
func Due(pending []store.OrganizationProvisionRecord, listed []unstructured.Unstructured) []store.OrganizationProvisionRecord {
	live := make(map[string]bool, len(listed))
	for i := range listed {
		if listed[i].GetDeletionTimestamp() == nil {
			live[listed[i].GetName()] = true
		}
	}
	var due []store.OrganizationProvisionRecord
	for _, rec := range pending {
		if rec.State == store.STSTenantRegistered && live[strings.ToLower(strings.TrimSpace(rec.Subdomain))] {
			due = append(due, rec)
		}
	}
	return due
}

// Run calls run for every record Due returns, one after the other, and stops
// early when ctx ends. It returns how many records reached done.
//
// pending and listed are read at the start of the pass, and each run can take
// seconds, so a record may have been deleted since. Just before running a
// record, Run reads it again through current and skips it unless it is still
// held at tenant_registered; run is given that fresh copy.
func Run(ctx context.Context, pending []store.OrganizationProvisionRecord, listed []unstructured.Unstructured,
	current func(id string) (store.OrganizationProvisionRecord, bool),
	run func(context.Context, store.OrganizationProvisionRecord) store.OrganizationProvisionRecord) int {
	done := 0
	for _, rec := range Due(pending, listed) {
		if ctx.Err() != nil {
			break
		}
		fresh, ok := current(rec.OrganizationID)
		if !ok || fresh.State != store.STSTenantRegistered {
			continue
		}
		if run(ctx, fresh).State == store.STSDone {
			done++
		}
	}
	return done
}
