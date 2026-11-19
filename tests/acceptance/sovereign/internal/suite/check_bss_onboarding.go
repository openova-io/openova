package suite

import (
	"context"
	"fmt"
	"time"
)

// Check BSS-G01, Self-service onboarding. Steps 1 to 7 are the onboarding
// journey of bss_org.go: a new customer of the storefront signs in, redeems a
// voucher that a sovereign-admin issued, orders the smallest package with one
// application paid from credit, and within 30 minutes its Organization is
// Ready, has its plan-quota and the application's host answers HTTP 200,
// with no call of the customer's steps made with the console session.
//
// Step 8 then waits up to 5 minutes for the price document, read as check
// BSS-G02 reads it, to list the package just bought, and records the evidence
// key price_list_state: "published" when it did, and the assertion of BSS-G02
// must then hold again; "not-seen" when it did not, which does not fail the
// check. Step 9, at the end of the run, removes what the journey created.
const onboardID = "BSS-G01"

// onboardPriceKey is the evidence key that BSS-070 reads.
const onboardPriceKey = "price_list_state"

type onboardingCheck struct {
	o onboarding
}

func init() { RegisterCheck(onboardingCheck{o: defaultOnboarding()}) }

func (onboardingCheck) ID() string { return onboardID }

func (c onboardingCheck) Run(ctx context.Context, cc *CheckContext, rec StepRecorder) error {
	res, ran, err := c.o.onboarded(ctx, cc, rec)
	if err != nil {
		if ran {
			return nil // the journey recorded the step that failed
		}
		return fmt.Errorf("the onboarding journey of an earlier check failed: %w", err)
	}
	fqdn := sovereignFQDN(cc.Prior)
	client := c.o.httpClient()
	checkStep(rec, onboardID, 8, "the price document lists the package bought and the assertion of BSS-G02 holds (evidence "+onboardPriceKey+")", func(ev Evidence) error {
		ev["order_id"], ev["order_amount_baisa"], ev["price_source"] = res.OrderID, res.AmountBaisa, res.PriceSource
		ev["org_slug"], ev["package_sku"], ev["app_host"] = res.OrgSlug, res.PackageSKU, res.AppHost
		start := time.Now()
		last, err := poll(ctx, c.o.waits.price, c.o.waits.every, func() (bool, string, error) {
			doc, err := priceListDocument(ctx, client, fqdn, Evidence{})
			if err != nil {
				return false, err.Error(), nil
			}
			for _, p := range doc.Packages {
				if p.SKU == res.PackageSKU {
					return true, "", nil
				}
			}
			return false, fmt.Sprintf("%d packages, not %s", len(doc.Packages), res.PackageSKU), nil
		})
		ev["price_wait_ms"] = time.Since(start).Milliseconds()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			ev[onboardPriceKey], ev["last"] = "not-seen", last
			return nil
		}
		ev[onboardPriceKey] = "published"
		judge := Evidence{}
		ev["bss_g02"] = judge
		return priceListJudge(ctx, client, fqdn, judge)
	})
	return nil
}
