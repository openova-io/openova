// Package api is the JSON API under /api/v1 plus the ops endpoints and the
// embedded UI. Every handler resolves the session first and passes its Scope
// to the store, so a customer principal can only ever read its own rows.
package api

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	"github.com/openova-io/openova/products/chargeback/internal/access"
	"github.com/openova-io/openova/products/chargeback/internal/collections"
	"github.com/openova-io/openova/products/chargeback/internal/commercial"
	"github.com/openova-io/openova/products/chargeback/internal/config"
	"github.com/openova-io/openova/products/chargeback/internal/crypto"
	"github.com/openova-io/openova/products/chargeback/internal/docs"
	"github.com/openova-io/openova/products/chargeback/internal/einvoice"
	"github.com/openova-io/openova/products/chargeback/internal/mail"
	"github.com/openova-io/openova/products/chargeback/internal/metrics"
	"github.com/openova-io/openova/products/chargeback/internal/settle"
	"github.com/openova-io/openova/products/chargeback/internal/store"
)

// VerifyError classifies a failed credential check. Message never carries
// the secret; Code is the gateway/service error code when one was returned.
type VerifyError struct {
	Code         string
	Message      string
	Unauthorized bool
	NotPublished bool
}

func (e *VerifyError) Error() string {
	if e.Code != "" {
		return e.Code + ": " + e.Message
	}
	return e.Message
}

// Verifier performs the activation check against the cloud.
type Verifier interface {
	VerifyProject(ctx context.Context, region, projectID, accessKey, secretKey string) error
}

// StatementHook is notified after a statement is issued (ADR-0014 D6).
// The OpenOva adapter's billing hook implements it; nil = off. A hook
// failure never un-issues the statement — issuing is idempotent, so
// re-POSTing /statements/{id}/issue repeats the (idempotent) hook.
type StatementHook interface {
	StatementIssued(ctx context.Context, st store.Statement, c store.Customer) error
}

// Deps wires the handler.
type Deps struct {
	Store    *store.Store
	Keys     *crypto.Keyring
	Mail     mail.Sender
	Verifier Verifier
	Config   config.Config
	Metrics  *metrics.Registry
	UI       fs.FS
	Now      func() time.Time
	Version  string

	// StatementHook, when set, receives issued statements (ADR-0014 D6).
	// It is the legacy name of the PREPAID settlement gateway: when
	// Settlement carries no prepaid gateway, this one is registered as it,
	// so existing wiring keeps its exact behaviour.
	StatementHook StatementHook

	// Settlement is the payment-gateway seam (DESIGN.md §8): which gateway
	// collects for which settlement method. nil is replaced in New with the
	// built-in registry (manual for invoice and internal), so a customer
	// paying against a purchase order is always serviceable.
	Settlement *settle.Registry

	// Commercial selects WHO invoices on this Sovereign (DESIGN.md §8.10):
	// this product, or the operator's own billing system. nil is replaced in
	// New with a selector over the store, whose default setting is internal.
	Commercial *commercial.Selector
	// Importer applies invoice-status imports from the operator's billing
	// system. nil = the import endpoint answers 503.
	Importer *commercial.Importer
	// Deliverer drains the commercial outbox. The background loop is started
	// by main; the handler holds it only so a Retry can push one row at once
	// instead of waiting for the next tick.
	Deliverer *commercial.Deliverer

	// Docs renders a statement as a PDF through the document renderer
	// (EPIC #6867). A client whose URL is empty reports Enabled() false and
	// GET /statements/{id}.pdf answers 503; nothing else depends on it.
	Docs *docs.Client

	// EInvoice is the e-invoicing profile (DESIGN.md §17). nil = OFF: no
	// document is built at issue and the two e-invoice routes answer 404 on
	// every statement. New builds it from Config.EInvoiceProfile when the
	// caller supplied none, so a deployment with EINVOICE_PROFILE set gets
	// the step without further wiring.
	EInvoice einvoice.Profile

	// DESIGN.md §9 — the account, collections and enforcement. Intents is
	// the gateway seam under the provider check; Enforcer suspends and
	// resumes through the platform seam; Wallet is what prepaid adds;
	// Collections is the daily evaluator, held so an operator can run a
	// pass now. nil = the corresponding endpoints answer 503.
	Intents     *commercial.Intents
	Enforcer    *collections.Enforcer
	Wallet      *collections.Wallet
	Collections *collections.Evaluator
}

// Handler serves the API.
type Handler struct {
	Deps
	// limiter is the per-address budget of the public calculator routes
	// (DESIGN.md §11), sized from Config.PublicCalculatorRatePerMinute.
	limiter *ipLimiter
}

const (
	sessionCookie = "cb_session"
	sessionTTL    = 24 * time.Hour
	pinTTL        = 10 * time.Minute
	pinThrottle   = 30 * time.Second
	pinMaxTries   = 5
	inviteTTL     = 7 * 24 * time.Hour
)

// New builds the full http.Handler.
func New(d Deps) http.Handler {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.Metrics == nil {
		d.Metrics = metrics.Default
	}
	// The payment-gateway seam (DESIGN.md §8). A deployment always has the
	// built-in registry — a customer paying by transfer, or settled as an
	// internal recharge, needs no configuration — and the legacy
	// StatementHook is registered under the stripe gateway name when nothing
	// else claimed it, so existing wiring behaves exactly as before.
	if d.Settlement == nil {
		d.Settlement = settle.NewRegistry()
	}
	if d.StatementHook != nil {
		stripe := store.Customer{Charging: store.ChargingBilled, PaymentMethod: store.PaymentMethodGateway, GatewayName: store.GatewayStripe}
		if g, _ := d.Settlement.For(stripe); g == nil {
			d.Settlement.Register(settle.GatewayStripe, settle.FromHook(d.StatementHook))
		}
	}
	// Who invoices (DESIGN.md §8.10). Internal by default, so a Sovereign
	// that never chose behaves exactly as it did.
	if d.Commercial == nil {
		d.Commercial = commercial.NewSelector(d.Store, nil)
	}
	if d.Intents == nil {
		d.Intents = &commercial.Intents{Store: d.Store, Commercial: d.Commercial, Settlement: d.Settlement}
	}
	if d.Enforcer == nil && d.Store != nil {
		d.Enforcer = &collections.Enforcer{Store: d.Store}
	}
	if d.Wallet == nil && d.Store != nil {
		d.Wallet = &collections.Wallet{Store: d.Store, Mail: d.Mail, Enforcer: d.Enforcer, PublicURL: d.Config.PublicURL, Owns: d.Commercial.OwnsCollections}
	}
	if d.Collections == nil && d.Store != nil {
		d.Collections = &collections.Evaluator{Store: d.Store, Mail: d.Mail, Enforcer: d.Enforcer, PublicURL: d.Config.PublicURL, Owns: d.Commercial.OwnsCollections, Now: d.Now}
	}
	// The document renderer (EPIC #6867). Built from the config when the
	// caller did not supply one, so a deployment with DOCRENDER_URL set gets
	// the feature without any further wiring, and one without it gets a
	// client that honestly reports itself unconfigured.
	if d.Docs == nil {
		d.Docs = docs.New(d.Config.DocRenderURL, d.Config.DocRenderToken)
	}
	// E-invoicing (DESIGN.md §17). An empty EINVOICE_PROFILE builds nothing
	// and every statement behaves exactly as it did. A configured profile
	// whose key cannot be parsed is a FATAL misconfiguration reported at
	// start-up rather than a silent fall back to no e-invoicing: a Sovereign
	// that believes it is issuing compliant invoices and is not is the worst
	// of the three outcomes.
	if d.EInvoice == nil && d.Config.EInvoiceProfile != "" {
		p, err := einvoice.New(einvoice.Config{
			Profile:       d.Config.EInvoiceProfile,
			SigningKeyPEM: einvoiceSigningKey(d.Config),
			KeyID:         d.Config.EInvoiceKeyID,
		})
		if err != nil {
			slog.Error("e-invoicing profile could not be built; issuing will refuse until it is fixed", "profile", d.Config.EInvoiceProfile, "error", err)
		}
		d.EInvoice = p
	}
	if d.Importer != nil && d.Importer.Enforcer == nil {
		d.Importer.Enforcer = d.Enforcer
	}
	h := &Handler{Deps: d, limiter: newIPLimiter(d.Config.PublicCalculatorRatePerMinute, d.Now)}
	mux := http.NewServeMux()

	// The public calculator (DESIGN.md §11): unauthenticated, rate-limited,
	// CORS for the configured origins, no cookie, no principal. The session
	// middleware skips this prefix (chain). The catalog is the ONE public
	// list book plus the two platform books; estimates are priced through
	// rating.PriceEstimate — the same Rate and Totals a statement uses.
	mux.HandleFunc("GET /api/v1/public/catalog", h.publicRoute(h.publicCatalog))
	mux.HandleFunc("POST /api/v1/public/estimates", h.publicRoute(h.publicCreateEstimate))
	mux.HandleFunc("GET /api/v1/public/estimates/{id}", h.publicRoute(h.publicGetEstimate))
	mux.HandleFunc("OPTIONS /api/v1/public/", h.publicRoute(func(http.ResponseWriter, *http.Request) {}))
	// Its operator side: the public toggle on a cloud book (rating.manage)
	// and the read-only Leads list (customers.manage).
	mux.HandleFunc("PUT /api/v1/pricebooks/{id}/public", h.setPriceBookPublic)
	mux.HandleFunc("GET /api/v1/leads", h.listLeads)

	// Tax rules and SKU tax categories (DESIGN.md §17). Reading is
	// metering.read; every write is settings.manage.
	mux.HandleFunc("GET /api/v1/tax/rules", h.listTaxRules)
	mux.HandleFunc("POST /api/v1/tax/rules", h.createTaxRule)
	mux.HandleFunc("PUT /api/v1/tax/rules/{id}", h.updateTaxRule)
	mux.HandleFunc("DELETE /api/v1/tax/rules/{id}", h.deleteTaxRule)
	mux.HandleFunc("GET /api/v1/tax/categories", h.listTaxCategories)
	mux.HandleFunc("PUT /api/v1/tax/categories", h.putTaxCategory)
	mux.HandleFunc("DELETE /api/v1/tax/categories/{sku}", h.deleteTaxCategory)

	// Ops.
	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("GET /readyz", h.readyz)
	mux.HandleFunc("GET /metrics", h.metricsHandler)

	// Auth.
	mux.HandleFunc("POST /api/v1/auth/pin/request", h.pinRequest)
	mux.HandleFunc("POST /api/v1/auth/pin/verify", h.pinVerify)
	mux.HandleFunc("POST /api/v1/auth/logout", h.logout)
	mux.HandleFunc("GET /api/v1/auth/me", h.me)
	// /me is the same document at the address DESIGN.md §10 names: email,
	// bindings and the effective permissions per scope the console hides
	// and shows by.
	mux.HandleFunc("GET /api/v1/me", h.me)

	// Access (DESIGN.md §10) — who holds which role at which scope. Every
	// change is audited as access.binding / access.mapping. settings.manage
	// Sovereign-wide; the roles document is readable by any principal so
	// the console can label what it shows.
	mux.HandleFunc("GET /api/v1/access/roles", h.listRoles)
	mux.HandleFunc("GET /api/v1/access/bindings", h.listBindings)
	mux.HandleFunc("POST /api/v1/access/bindings", h.createBinding)
	mux.HandleFunc("DELETE /api/v1/access/bindings/{id}", h.deleteBinding)
	mux.HandleFunc("GET /api/v1/access/group-mappings", h.listGroupMappings)
	mux.HandleFunc("PUT /api/v1/access/group-mappings", h.putGroupMappings)

	// Partners — resellers and agents (DESIGN.md §13). Sovereign
	// writes need partners.manage; a partner owner holds partner.self.manage
	// on its OWN partner. `tiers` is registered before `{id}` so the literal
	// path wins over the wildcard.
	mux.HandleFunc("GET /api/v1/partners", h.listPartners)
	mux.HandleFunc("POST /api/v1/partners", h.createPartner)
	mux.HandleFunc("GET /api/v1/partners/tiers", h.listPartnerTiers)
	mux.HandleFunc("POST /api/v1/partners/tiers", h.createPartnerTier)
	mux.HandleFunc("PUT /api/v1/partners/tiers/{id}/discounts", h.putTierDiscounts)
	mux.HandleFunc("GET /api/v1/partners/{id}", h.getPartner)
	mux.HandleFunc("PATCH /api/v1/partners/{id}", h.patchPartner)
	mux.HandleFunc("PUT /api/v1/partners/{id}/retail-rule", h.putRetailRule)
	mux.HandleFunc("GET /api/v1/partners/{id}/retail-book", h.getRetailBook)
	mux.HandleFunc("GET /api/v1/partners/{id}/customers", h.listPartnerCustomers)
	mux.HandleFunc("GET /api/v1/partners/{id}/statements", h.listPartnerStatements)
	mux.HandleFunc("GET /api/v1/partners/{id}/margin", h.partnerMargin)
	mux.HandleFunc("GET /api/v1/partners/{id}/account", h.partnerAccount)
	mux.HandleFunc("GET /api/v1/partners/{id}/users", h.listPartnerUsers)
	mux.HandleFunc("POST /api/v1/partners/{id}/users", h.addPartnerUser)
	mux.HandleFunc("DELETE /api/v1/partners/{id}/users/{email}", h.deletePartnerUser)

	// Customers.
	mux.HandleFunc("GET /api/v1/customers", h.listCustomers)
	mux.HandleFunc("POST /api/v1/customers", h.createCustomer)
	mux.HandleFunc("POST /api/v1/customers/import", h.importCustomers)
	mux.HandleFunc("GET /api/v1/customers/{id}", h.getCustomer)
	mux.HandleFunc("PATCH /api/v1/customers/{id}", h.patchCustomer)
	mux.HandleFunc("DELETE /api/v1/customers/{id}", h.deleteCustomer)
	mux.HandleFunc("POST /api/v1/customers/{id}/invite", h.inviteCustomer)
	mux.HandleFunc("GET /api/v1/customers/{id}/users", h.listUsers)
	mux.HandleFunc("POST /api/v1/customers/{id}/users", h.addUser)
	mux.HandleFunc("DELETE /api/v1/customers/{id}/users/{email}", h.deleteUser)
	mux.HandleFunc("GET /api/v1/customers/{id}/audit", h.customerAudit)

	// Invites (public by token).
	mux.HandleFunc("GET /api/v1/invites/{token}", h.getInvite)
	mux.HandleFunc("POST /api/v1/invites/{token}/activate", h.activateInvite)

	// Sources.
	mux.HandleFunc("GET /api/v1/customers/{id}/sources", h.listSources)
	// #6850/#6867 — the Sovereign allocation view (ADR-0014 D3 case 3):
	// tenant Org rows + the platform-overhead line, in currency, per the
	// editable settings. Operator-only; it spans customers.
	mux.HandleFunc("GET /api/v1/allocation", h.allocation)
	mux.HandleFunc("GET /api/v1/allocation/settings", h.getAllocationSettings)
	mux.HandleFunc("PUT /api/v1/allocation/settings", h.putAllocationSettings)
	// #6862 — discounts and campaigns. Operator-only to create, edit, toggle
	// or delete; a customer must never be able to grant themselves a
	// discount. The customer-scoped list also carries the global campaigns
	// (customer_id null, #6867) so a customer sees what applies to it.
	mux.HandleFunc("GET /api/v1/customers/{id}/discounts", h.listDiscounts)
	mux.HandleFunc("POST /api/v1/customers/{id}/discounts", h.createDiscount)
	mux.HandleFunc("GET /api/v1/discounts", h.listAllDiscounts)
	mux.HandleFunc("POST /api/v1/discounts", h.createGlobalDiscount)
	mux.HandleFunc("GET /api/v1/discounts/{id}", h.getDiscount)
	mux.HandleFunc("PUT /api/v1/discounts/{id}", h.updateDiscount)
	mux.HandleFunc("PATCH /api/v1/discounts/{id}", h.setDiscountActive)
	mux.HandleFunc("DELETE /api/v1/discounts/{id}", h.deleteDiscount)
	// DESIGN.md §2.11 — the discount combination rule. Operator-only; read at
	// statement run time and recorded on every statement the run writes.
	// Contracts and commercial terms (DESIGN.md §15). `/contracts/renewals`
	// is a literal path and so wins over `/contracts/{id}` in the mux.
	mux.HandleFunc("GET /api/v1/contracts", h.listContracts)
	mux.HandleFunc("POST /api/v1/contracts", h.createContract)
	mux.HandleFunc("GET /api/v1/contracts/renewals", h.renewalsDue)
	mux.HandleFunc("GET /api/v1/contracts/{id}", h.getContract)
	mux.HandleFunc("PATCH /api/v1/contracts/{id}", h.patchContract)
	mux.HandleFunc("DELETE /api/v1/contracts/{id}", h.deleteContract)
	mux.HandleFunc("PUT /api/v1/contracts/{id}/items", h.putContractItems)
	mux.HandleFunc("POST /api/v1/contracts/{id}/sla-credit", h.issueSLACredit)
	mux.HandleFunc("GET /api/v1/customers/{id}/contracts", h.customerContracts)

	mux.HandleFunc("GET /api/v1/billing-settings", h.getBillingSettings)
	mux.HandleFunc("PUT /api/v1/billing-settings", h.putBillingSettings)
	mux.HandleFunc("POST /api/v1/customers/{id}/sources", h.createSource)
	// DESIGN.md §2 — a price book is assigned PER SOURCE: GET/PATCH a source
	// under its customer (price_book_id, scope-checked against the source's
	// layer), the operator-wide source directory, and one source by id.
	mux.HandleFunc("GET /api/v1/customers/{id}/sources/{sid}", h.getSource)
	mux.HandleFunc("PATCH /api/v1/customers/{id}/sources/{sid}", h.patchSource)
	mux.HandleFunc("GET /api/v1/sources", h.listAllSources)
	mux.HandleFunc("GET /api/v1/sources/{id}", h.getSource)
	mux.HandleFunc("PATCH /api/v1/sources/{id}", h.patchSource)
	mux.HandleFunc("POST /api/v1/sources/{id}/credential", h.rotateCredential)
	mux.HandleFunc("POST /api/v1/sources/{id}/verify", h.verifySource)
	mux.HandleFunc("DELETE /api/v1/sources/{id}", h.deleteSource)
	mux.HandleFunc("POST /api/v1/sources/{id}/purge-excluded", h.purgeExcluded)

	// Usage + inventory.
	mux.HandleFunc("GET /api/v1/customers/{id}/usage", h.customerUsage)
	mux.HandleFunc("GET /api/v1/customers/{id}/inventory", h.customerInventory)

	// Resources with cost (#6867, DESIGN.md §3.4). A resource id may itself
	// contain slashes (k8s "namespace/pod"), hence the rest-of-path wildcard.
	mux.HandleFunc("GET /api/v1/resources", h.listResources)
	mux.HandleFunc("GET /api/v1/resources.csv", h.resourcesCSV)
	mux.HandleFunc("GET /api/v1/resources/{source_id}/{resource_id...}", h.getResource)
	mux.HandleFunc("GET /api/v1/customers/{id}/resources", h.customerResources)
	mux.HandleFunc("GET /api/v1/customers/{id}/resources.csv", h.customerResourcesCSV)

	// Price books.
	mux.HandleFunc("GET /api/v1/pricebooks", h.listPriceBooks)
	mux.HandleFunc("POST /api/v1/pricebooks", h.createPriceBook)
	mux.HandleFunc("GET /api/v1/pricebooks/template.csv", h.priceBookTemplate)
	mux.HandleFunc("GET /api/v1/pricebooks/{id}", h.getPriceBook)
	mux.HandleFunc("PUT /api/v1/pricebooks/{id}", h.updatePriceBook)
	mux.HandleFunc("DELETE /api/v1/pricebooks/{id}", h.deletePriceBook)
	mux.HandleFunc("POST /api/v1/pricebooks/{id}/clone", h.clonePriceBook)
	mux.HandleFunc("GET /api/v1/pricebooks/{id}/export.csv", h.exportPriceBook)
	mux.HandleFunc("GET /api/v1/pricebooks/{id}/coverage", h.priceBookCoverage)
	mux.HandleFunc("PUT /api/v1/pricebooks/{id}/items", h.putPriceItems)
	mux.HandleFunc("POST /api/v1/pricebooks/{id}/items", h.addPriceItem)
	mux.HandleFunc("PATCH /api/v1/pricebooks/{id}/items/{sku}", h.patchPriceItem)
	mux.HandleFunc("DELETE /api/v1/pricebooks/{id}/items/{sku}", h.deletePriceItem)
	mux.HandleFunc("POST /api/v1/pricebooks/{id}/import", h.importPriceBook)

	// Statements.
	mux.HandleFunc("POST /api/v1/statements/run", h.runStatements)
	mux.HandleFunc("GET /api/v1/statements", h.listAllStatements)
	mux.HandleFunc("GET /api/v1/customers/{id}/statements", h.listCustomerStatements)
	mux.HandleFunc("GET /api/v1/statements/{id}", h.getStatement)
	mux.HandleFunc("POST /api/v1/statements/{id}/issue", h.issueStatement)
	// Post-paid invoicing (DESIGN.md §8) — operator-only, every transition
	// audited. PATCH edits the purchase-order reference and terms of a DRAFT;
	// send / payments / cancel walk the invoice lifecycle.
	mux.HandleFunc("PATCH /api/v1/statements/{id}", h.patchStatement)
	mux.HandleFunc("POST /api/v1/statements/{id}/send", h.sendStatement)
	mux.HandleFunc("POST /api/v1/statements/{id}/payments", h.recordStatementPayment)
	mux.HandleFunc("GET /api/v1/statements/{id}/payments", h.listStatementPayments)
	// The e-invoice (DESIGN.md §17): the structured document and the signed
	// archival XML. Both follow reading the statement.
	mux.HandleFunc("GET /api/v1/statements/{id}/einvoice", h.getEInvoice)
	mux.HandleFunc("GET /api/v1/statements/{id}/einvoice.xml", h.getEInvoiceXML)
	mux.HandleFunc("POST /api/v1/statements/{id}/cancel", h.cancelStatement)
	// The operator's billing system reports back on the invoices we exported
	// (DESIGN.md §8.10). Authenticated by an HMAC over the raw body, not by a
	// session: the caller is a machine in the operator's estate.
	mux.HandleFunc("POST /api/v1/commercial/import/invoice-status", h.importInvoiceStatus)
	mux.HandleFunc("POST /api/v1/commercial/import/payment-status", h.importPaymentStatus)
	mux.HandleFunc("POST /api/v1/commercial/import/account-balance", h.importAccountBalance)
	mux.HandleFunc("POST /api/v1/commercial/import/enforcement", h.importEnforcement)
	mux.HandleFunc("GET /api/v1/commercial/outbox", h.listOutbox)
	mux.HandleFunc("POST /api/v1/commercial/outbox/{id}/retry", h.retryOutbox)
	mux.HandleFunc("DELETE /api/v1/statements/{id}", h.deleteStatement)

	// The customer account, payments, credit notes, collections and
	// enforcement (DESIGN.md §9). Reads follow the session scope; writes are
	// operator-only and audited.
	mux.HandleFunc("GET /api/v1/customers/{id}/account", h.getAccount)
	mux.HandleFunc("GET /api/v1/customers/{id}/payments", h.listCustomerPayments)
	mux.HandleFunc("POST /api/v1/customers/{id}/payments", h.recordPayment)
	mux.HandleFunc("POST /api/v1/payments", h.recordPayment)
	mux.HandleFunc("GET /api/v1/payments/{id}", h.getPayment)
	mux.HandleFunc("POST /api/v1/payments/{id}/allocate", h.allocatePayment)
	mux.HandleFunc("POST /api/v1/payments/{id}/refund", h.refundPayment)
	mux.HandleFunc("POST /api/v1/customers/{id}/account/apply-credit", h.applyCredit)
	mux.HandleFunc("POST /api/v1/customers/{id}/payment-intents", h.createPaymentIntent)
	mux.HandleFunc("GET /api/v1/customers/{id}/payment-intents", h.listPaymentIntents)
	mux.HandleFunc("POST /api/v1/statements/{id}/credit-notes", h.createCreditNote)
	mux.HandleFunc("GET /api/v1/statements/{id}/credit-notes", h.listStatementCreditNotes)
	mux.HandleFunc("GET /api/v1/customers/{id}/credit-notes", h.listCustomerCreditNotes)
	mux.HandleFunc("GET /api/v1/credit-notes/{id}", h.getCreditNote)
	mux.HandleFunc("GET /api/v1/collections/aging", h.aging)
	mux.HandleFunc("POST /api/v1/collections/run", h.runCollections)
	mux.HandleFunc("POST /api/v1/customers/{id}/suspend", h.suspendCustomer)
	mux.HandleFunc("POST /api/v1/customers/{id}/resume", h.resumeCustomer)
	mux.HandleFunc("GET /api/v1/customers/{id}/suspensions", h.listSuspensions)
	// The gateway's OWN confirmation (DESIGN.md §9.2): unauthenticated,
	// verified by the gateway's signature through settle.Gateway.VerifyCallback,
	// booked through the commercial provider, idempotent on the reference.
	mux.HandleFunc("POST /api/v1/gateways/{name}/callback", h.gatewayCallback)

	// Customer self-service (DESIGN.md §16) — what a paying customer does
	// without the operator. A saved payment method rides the SAME gateway
	// registry that collects (settle.Registry), and a dispute's credit note
	// is the SAME credit note §9.3 issues; neither is a second mechanism.
	// Writes need account.topup on the customer (an owner or a billing user
	// on its own account) or billing.collect; resolving a dispute is the
	// operator's billing.collect.
	mux.HandleFunc("GET /api/v1/customers/{id}/payment-methods", h.listPaymentMethods)
	mux.HandleFunc("POST /api/v1/customers/{id}/payment-methods", h.createPaymentMethod)
	mux.HandleFunc("POST /api/v1/customers/{id}/payment-methods/{mid}/confirm", h.confirmPaymentMethod)
	mux.HandleFunc("DELETE /api/v1/customers/{id}/payment-methods/{mid}", h.deleteCustomerPaymentMethod)
	mux.HandleFunc("GET /api/v1/payment-methods/{id}", h.getPaymentMethod)
	mux.HandleFunc("DELETE /api/v1/payment-methods/{id}", h.deletePaymentMethod)
	mux.HandleFunc("POST /api/v1/statements/{id}/disputes", h.createDispute)
	mux.HandleFunc("GET /api/v1/statements/{id}/disputes", h.listStatementDisputes)
	mux.HandleFunc("GET /api/v1/customers/{id}/disputes", h.listCustomerDisputes)
	mux.HandleFunc("GET /api/v1/disputes/{id}", h.getDispute)
	mux.HandleFunc("POST /api/v1/disputes/{id}/resolve", h.resolveDispute)

	// Currency rates (#6867 follow-up, DESIGN.md §3.10) — operator-only.
	// per_base of a price-book currency relative to the reporting currency
	// (allocation_settings.currency); every cost surface converts with them.
	mux.HandleFunc("GET /api/v1/currencies", h.listCurrencies)
	mux.HandleFunc("GET /api/v1/currencies/{code}", h.getCurrency)
	mux.HandleFunc("PUT /api/v1/currencies/{code}", h.putCurrency)
	mux.HandleFunc("DELETE /api/v1/currencies/{code}", h.deleteCurrency)

	// Saved views (#6867) — per signed-in user, any role.
	mux.HandleFunc("GET /api/v1/views", h.listViews)
	mux.HandleFunc("POST /api/v1/views", h.createView)
	mux.HandleFunc("DELETE /api/v1/views/{id}", h.deleteView)

	// Operator.
	mux.HandleFunc("GET /api/v1/overview", h.overview)

	// Capacity (DESIGN.md §11, EPIC #6867). Reads need metering.read at the
	// Sovereign — a customer never sees capacity — writes capacity.manage;
	// every write is audited as capacity.region / .zone / .pool /
	// .footprint / .cap.
	mux.HandleFunc("GET /api/v1/capacity/overview", h.capacityOverview)
	mux.HandleFunc("GET /api/v1/capacity/regions", h.listCapacityRegions)
	mux.HandleFunc("POST /api/v1/capacity/regions", h.createCapacityRegion)
	mux.HandleFunc("DELETE /api/v1/capacity/regions/{id}", h.deleteCapacityRegion)
	mux.HandleFunc("POST /api/v1/capacity/regions/{id}/zones", h.createCapacityZone)
	mux.HandleFunc("DELETE /api/v1/capacity/zones/{id}", h.deleteCapacityZone)
	mux.HandleFunc("GET /api/v1/capacity/zones/{id}/pools", h.listCapacityPools)
	mux.HandleFunc("PUT /api/v1/capacity/pools/{id}", h.putCapacityPool)
	mux.HandleFunc("GET /api/v1/capacity/footprints", h.listFootprints)
	mux.HandleFunc("PUT /api/v1/capacity/footprints/{sku}", h.putFootprint)
	mux.HandleFunc("GET /api/v1/capacity/caps", h.listCaps)
	mux.HandleFunc("PUT /api/v1/capacity/caps", h.putCap)

	// Cost analysis (#6867, DESIGN.md §3.1-3.3).
	mux.HandleFunc("GET /api/v1/cost/explore", h.explore)
	mux.HandleFunc("GET /api/v1/cost/export.csv", h.exploreCSV)
	mux.HandleFunc("GET /api/v1/cost/summary", h.summary)
	mux.HandleFunc("GET /api/v1/cost/dimensions", h.costDimensions)
	mux.HandleFunc("GET /api/v1/customers/{id}/cost/explore", h.customerExplore)
	mux.HandleFunc("GET /api/v1/customers/{id}/cost/export.csv", h.customerExploreCSV)
	mux.HandleFunc("GET /api/v1/customers/{id}/cost/summary", h.customerSummary)
	mux.HandleFunc("GET /api/v1/customers/{id}/cost/dimensions", h.customerCostDimensions)

	// Budgets (#6867, DESIGN.md §3.5). Reads follow the session scope;
	// writes are operator-only.
	mux.HandleFunc("GET /api/v1/budgets", h.listBudgets)
	mux.HandleFunc("POST /api/v1/budgets", h.createBudget)
	mux.HandleFunc("GET /api/v1/budgets/{id}", h.getBudget)
	mux.HandleFunc("PUT /api/v1/budgets/{id}", h.updateBudget)
	mux.HandleFunc("DELETE /api/v1/budgets/{id}", h.deleteBudget)
	mux.HandleFunc("GET /api/v1/budgets/{id}/status", h.budgetStatus)
	mux.HandleFunc("GET /api/v1/customers/{id}/budgets", h.customerBudgets)
	// Anomalies + recommendations (#6867, DESIGN.md §3.6-3.7).
	mux.HandleFunc("GET /api/v1/anomalies", h.anomalies)
	mux.HandleFunc("GET /api/v1/customers/{id}/anomalies", h.customerAnomalies)
	mux.HandleFunc("GET /api/v1/recommendations", h.recommendations)
	mux.HandleFunc("GET /api/v1/customers/{id}/recommendations", h.customerRecommendations)

	// Scheduled cost reports (#6867 follow-up). Reads follow the session
	// scope; the operator writes any schedule, a customer-admin only its
	// own customer's (customer_id forced), a customer-viewer none.
	mux.HandleFunc("GET /api/v1/reports/schedules", h.listReportSchedules)
	mux.HandleFunc("POST /api/v1/reports/schedules", h.createReportSchedule)
	mux.HandleFunc("GET /api/v1/reports/schedules/{id}", h.getReportSchedule)
	mux.HandleFunc("PUT /api/v1/reports/schedules/{id}", h.updateReportSchedule)
	mux.HandleFunc("DELETE /api/v1/reports/schedules/{id}", h.deleteReportSchedule)
	mux.HandleFunc("POST /api/v1/reports/schedules/{id}/send", h.sendReportNow)
	mux.HandleFunc("GET /api/v1/reports/schedules/{id}/preview", h.previewReport)
	mux.HandleFunc("GET /api/v1/reports/schedules/{id}/deliveries", h.listReportDeliveries)
	mux.HandleFunc("GET /api/v1/customers/{id}/reports/schedules", h.customerReportSchedules)

	// Anything else under /api is 404 JSON; everything else is the UI.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { writeErr(w, http.StatusNotFound, "not found") })
	mux.Handle("/", h.uiHandler())

	return h.chain(mux)
}

// chain applies recovery, request logging, security headers and session
// loading around the mux.
func (h *Handler) chain(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := h.Now()
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic in handler", "path", r.URL.Path, "panic", rec, "stack", string(debug.Stack()))
				writeErr(w, http.StatusInternalServerError, "internal error")
			}
		}()
		w.Header().Set("X-Content-Type-Options", "nosniff")
		h.frameHeaders(w, r)
		w.Header().Set("Referrer-Policy", "same-origin")
		// The public calculator routes resolve no principal at all
		// (DESIGN.md §11): a cookie sent to them is ignored, not looked up.
		if strings.HasPrefix(r.URL.Path, "/api/") && !isPublicPath(r.URL.Path) {
			r = r.WithContext(h.loadSession(r))
		}
		sw := &statusWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(sw, r)
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Metrics.Inc("chargeback_http_requests_total", "API requests by method and status", map[string]string{"method": r.Method, "status": http.StatusText(sw.status)}, 1)
			slog.Info("http", "method", r.Method, "path", r.URL.Path, "status", sw.status, "ms", time.Since(start).Milliseconds())
		}
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

// ---------------------------------------------------------------------------
// sessions in context
// ---------------------------------------------------------------------------

type ctxKey int

const sessionKey ctxKey = 1

// loadSession resolves the principal of an API request: the cb_session
// cookie, else the identity the Sovereign's SSO gate forwarded. Either way
// the session's bindings are resolved NOW, from role_bindings, the
// directory-group mappings and OPERATOR_EMAILS — never from what was stored
// at sign-in — so a revoked binding takes effect at the next request and a
// granted one needs no re-login. A principal left with no binding is
// unauthenticated: the API answers 401 rather than inventing access.
func (h *Handler) loadSession(r *http.Request) context.Context {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		sess, err := h.Store.GetSession(r.Context(), c.Value)
		if err == nil {
			resolved, err := h.resolveSession(r, sess.Email, nil)
			if err != nil {
				slog.Warn("session bindings lookup", "error", err)
				return r.Context()
			}
			if len(resolved.Roles) == 0 {
				return r.Context()
			}
			resolved.Token, resolved.ExpiresAt = sess.Token, sess.ExpiresAt
			return context.WithValue(r.Context(), sessionKey, resolved)
		}
		if !errors.Is(err, store.ErrNotFound) {
			slog.Warn("session lookup", "error", err)
		}
	}
	return h.loadGateSession(r)
}

// loadGateSession derives a session from the identity the Sovereign's OIDC
// gate already verified, so a user who signed in once at the Sovereign SSO is
// not asked to sign in a second time here (#6841 — the zero-click contract of
// docs/SECURITY.md §6 / bp-oidc-gate).
//
// The identity is per-request: no session row is written and no cookie is set,
// because the gate is the session. Sign-out is the gate's /oauth2/sign_out.
//
// The header is trusted WITHOUT verification, which is only safe because the
// gate owns the public hostname and the app has no route of its own — see
// Config.TrustedForwardAuthHeader. When TRUSTED_FORWARD_AUTH_HEADER is unset
// (the default) this returns the context untouched and the header, if any, is
// ignored: an unconfigured deployment cannot be spoofed.
func (h *Handler) loadGateSession(r *http.Request) context.Context {
	name := h.Config.TrustedForwardAuthHeader
	if name == "" {
		return r.Context()
	}
	email := strings.ToLower(strings.TrimSpace(r.Header.Get(name)))
	if email == "" || !strings.Contains(email, "@") {
		return r.Context()
	}
	// The directory groups ride on a second header from the same gate and
	// are trusted under exactly the same conditions (DESIGN.md §10). Each
	// named group adds the roles group_role_mappings binds to it.
	var groups []string
	if gh := h.Config.TrustedForwardGroupsHeader; gh != "" {
		for _, g := range strings.Split(r.Header.Get(gh), ",") {
			if g = strings.TrimSpace(g); g != "" {
				groups = append(groups, g)
			}
		}
	}
	sess, err := h.resolveSession(r, email, groups)
	if err != nil {
		slog.Warn("gate identity role lookup", "error", err)
		return r.Context()
	}
	if len(sess.Roles) == 0 {
		// Authenticated at the gate but granted nothing here. Fall through
		// unauthenticated so the API answers 401 rather than inventing access.
		return r.Context()
	}
	sess.ExpiresAt = time.Now().Add(sessionTTL).UTC()
	return context.WithValue(r.Context(), sessionKey, sess)
}

// withSession injects a session for tests and internal calls.
func withSession(ctx context.Context, sess store.Session) context.Context {
	return context.WithValue(ctx, sessionKey, sess)
}

func sessionFrom(r *http.Request) (store.Session, bool) {
	s, ok := r.Context().Value(sessionKey).(store.Session)
	return s, ok
}

// requireAuth answers 401 when no session is present.
func (h *Handler) requireAuth(w http.ResponseWriter, r *http.Request) (store.Session, bool) {
	s, ok := sessionFrom(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, "sign in required")
		return store.Session{}, false
	}
	return s, true
}

// requirePermission is THE authorization check (DESIGN.md §10): the session
// must hold perm at the scope — the Sovereign when customerID is "", else
// that customer. A Sovereign-scoped binding carrying the permission passes
// at either scope; a customer-scoped binding passes only on its customer.
//
// Refusals: 401 with no session; 404 when the caller holds no binding at all
// on the customer asked for, so ids of other customers are not confirmed;
// 403 when the caller is on the scope but lacks the permission — the body
// names the permission, which is what the console shows.
func (h *Handler) requirePermission(w http.ResponseWriter, r *http.Request, perm access.Permission, customerID string) (store.Session, bool) {
	return h.requireAnyPermission(w, r, customerID, perm)
}

// requireAnyPermission passes when the session holds ANY of the permissions
// at the scope (a checkout may be requested by the customer's own top-up
// permission or by the operator's collect permission).
func (h *Handler) requireAnyPermission(w http.ResponseWriter, r *http.Request, customerID string, perms ...access.Permission) (store.Session, bool) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return s, false
	}
	bindings := access.Bindings(s)
	if customerID != "" && !access.OnCustomer(bindings, customerID) {
		writeErr(w, http.StatusNotFound, "not found")
		return s, false
	}
	if access.HasAny(bindings, customerID, perms...) {
		return s, true
	}
	names := make([]string, len(perms))
	for i, p := range perms {
		names[i] = string(p)
	}
	where := "at the Sovereign"
	if customerID != "" {
		where = "on this customer"
	}
	writeErr(w, http.StatusForbidden, "permission "+strings.Join(names, " or ")+" required "+where)
	return s, false
}

// requireSovereign is requirePermission at the Sovereign scope: what every
// cross-customer surface asks.
func (h *Handler) requireSovereign(w http.ResponseWriter, r *http.Request, perm access.Permission) (store.Session, bool) {
	return h.requirePermission(w, r, perm, "")
}

// requireCrossCustomer guards the surfaces that read ACROSS customers — the
// cost explorer, the summary, resources, anomalies, recommendations. Two
// principals pass: a Sovereign one, which sees every customer, and a PARTNER
// one, which sees the customers assigned to its partner (DESIGN.md §13.5)
// because store.Scope.Confine narrows every query underneath to exactly that
// set. A customer principal is refused: its lens is /customers/{id}/… .
//
// This is requireSovereign plus one clause, not a second authorization path:
// the permission asked for is the same, and the narrowing is the store's.
func (h *Handler) requireCrossCustomer(w http.ResponseWriter, r *http.Request, perm access.Permission) (store.Session, bool) {
	s, ok := h.requireAuth(w, r)
	if !ok {
		return s, false
	}
	bindings := access.Bindings(s)
	if access.Has(bindings, perm, "") || access.HasAnyPartner(bindings, perm) {
		return s, true
	}
	writeErr(w, http.StatusForbidden, "permission "+string(perm)+" required at the Sovereign")
	return s, false
}

// requireCustomer answers 401/403/404 unless the session may act on the
// customer: a read needs metering.read on it, a write customer.self.manage
// (the owner on its own customer, or customers.manage Sovereign-wide).
// Customers outside the caller's bindings read as 404 so ids of other
// customers are not confirmed.
func (h *Handler) requireCustomer(w http.ResponseWriter, r *http.Request, customerID string, write bool) (store.Session, bool) {
	if write {
		return h.requirePermission(w, r, access.CustomerSelfManage, customerID)
	}
	return h.requirePermission(w, r, access.MeteringRead, customerID)
}

func (h *Handler) setSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   strings.HasPrefix(h.Config.PublicURL, "https://"),
		Expires:  expires,
	})
}

func (h *Handler) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   strings.HasPrefix(h.Config.PublicURL, "https://"),
		MaxAge:   -1,
	})
}

// audit records a mutation attributed to the session (or "system").
func (h *Handler) audit(r *http.Request, customerID *string, action string, details map[string]any) {
	actor := "system"
	if s, ok := sessionFrom(r); ok {
		actor = s.Email
	}
	if err := h.Store.Audit(r.Context(), customerID, actor, action, details); err != nil {
		slog.Warn("audit write failed", "action", action, "error", err)
	}
}
