package api

// The beneficiary, standing-order, direct-debit and scheduled-payment operations of the
// account-information document, each for the account of the path and for every account of the
// consent. Each is offered when the ledger's capabilities name it; otherwise it answers 404. The
// rules shared by every read operation are in ais_common.go.
//
// Detail adds the creditor's account. Fields of the documents that the port does not carry are
// left out; so is the port's mandate identifier of a direct debit, because the documents carry it
// only inside mandate information that needs a frequency the port does not have.

import (
	"context"

	ledgerv1 "github.com/openova-io/openova/products/fingate/api/ledger/v1"
	"github.com/openova-io/openova/products/fingate/internal/gen/accountinfo"
	"github.com/openova-io/openova/products/fingate/internal/ledgerclient"
)

// list answers one page of a list as the document object T; data is the key of the list under
// Data, and object turns one item into its document form.
func list[T, I any](ctx context.Context, a *accountInfo, account, data string,
	read func(ctx context.Context, customer, account, page string) (ledgerclient.Page[I], error),
	object func(account string, item I, g grant) map[string]any) (T, error) {
	g, err := a.grantOf(ctx)
	if err != nil {
		var zero T
		return zero, err
	}
	got, next, err := readList(ctx, g, account, read)
	if err != nil {
		var zero T
		return zero, err
	}
	items := make([]any, 0, len(got))
	for _, x := range got {
		items = append(items, object(x.account, x.item, g))
	}
	return fill[T](map[string]any{"Data": map[string]any{data: items}, "Links": a.links(ctx, next)})
}

func beneficiaryObject(account string, b *ledgerv1.Beneficiary, g grant) map[string]any {
	m := map[string]any{"AccountId": account}
	put(m, "BeneficiaryId", cut(b.GetBeneficiaryId(), 40))
	put(m, "Reference", cut(b.GetReference(), 35))
	if g.detail("Beneficiaries") {
		if c := cashAccount(b.GetSchemeName(), b.GetIdentification(), b.GetName(), g.has("ReadPAN")); c != nil {
			m["CreditorAccount"] = c
		}
	}
	return m
}

func standingOrderObject(account string, s *ledgerv1.StandingOrder, g grant) map[string]any {
	m := map[string]any{"AccountId": account}
	put(m, "StandingOrderId", cut(s.GetStandingOrderId(), 40))
	put(m, "StandingOrderStatusCode", s.GetStatus())
	put(m, "NextPaymentDateTime", s.GetNextAt())
	putMoney(m, "NextPaymentAmount", s.GetNextAmount())
	if s.GetFrequency() != "" {
		mandate := map[string]any{"Frequency": map[string]any{"Type": s.GetFrequency()}}
		put(mandate, "FinalPaymentDateTime", s.GetFinalAt())
		m["MandateRelatedInformation"] = mandate
	}
	if r := cut(s.GetReference(), 140); r != "" {
		m["RemittanceInformation"] = map[string]any{"Unstructured": []any{r}}
	}
	if g.detail("StandingOrders") {
		if c := cashAccount(s.GetCreditorSchemeName(), s.GetCreditorIdentification(), s.GetCreditorName(), g.has("ReadPAN")); c != nil {
			m["CreditorAccount"] = c
		}
	}
	return m
}

func directDebitObject(account string, d *ledgerv1.DirectDebit, _ grant) map[string]any {
	m := map[string]any{"AccountId": account, "Name": cut(d.GetName(), 70)}
	put(m, "DirectDebitId", cut(d.GetDirectDebitId(), 40))
	put(m, "DirectDebitStatusCode", d.GetStatus())
	put(m, "PreviousPaymentDateTime", d.GetPreviousAt())
	putMoney(m, "PreviousPaymentAmount", d.GetPreviousAmount())
	return m
}

func scheduledPaymentObject(account string, s *ledgerv1.ScheduledPayment, g grant) map[string]any {
	m := map[string]any{"AccountId": account, "ScheduledPaymentDateTime": s.GetExecutionAt(), "ScheduledType": "Execution"}
	put(m, "ScheduledPaymentId", cut(s.GetScheduledPaymentId(), 40))
	put(m, "Reference", cut(s.GetReference(), 35))
	putMoney(m, "InstructedAmount", s.GetAmount())
	if g.detail("ScheduledPayments") {
		if c := cashAccount(s.GetCreditorSchemeName(), s.GetCreditorIdentification(), s.GetCreditorName(), g.has("ReadPAN")); c != nil {
			m["CreditorAccount"] = c
		}
	}
	return m
}

func (a *accountInfo) beneficiaries(ctx context.Context, account string) (accountinfo.OBReadBeneficiary5, error) {
	return list[accountinfo.OBReadBeneficiary5](ctx, a, account, "Beneficiary", a.s.cfg.Ledger.Beneficiaries, beneficiaryObject)
}

func (a *accountInfo) standingOrders(ctx context.Context, account string) (accountinfo.OBReadStandingOrder6, error) {
	return list[accountinfo.OBReadStandingOrder6](ctx, a, account, "StandingOrder", a.s.cfg.Ledger.StandingOrders, standingOrderObject)
}

func (a *accountInfo) directDebits(ctx context.Context, account string) (accountinfo.OBReadDirectDebit2, error) {
	return list[accountinfo.OBReadDirectDebit2](ctx, a, account, "DirectDebit", a.s.cfg.Ledger.DirectDebits, directDebitObject)
}

func (a *accountInfo) scheduledPayments(ctx context.Context, account string) (accountinfo.OBReadScheduledPayment3, error) {
	return list[accountinfo.OBReadScheduledPayment3](ctx, a, account, "ScheduledPayment", a.s.cfg.Ledger.ScheduledPayments, scheduledPaymentObject)
}

// GetAccountsAccountIdBeneficiaries implements GET /accounts/{AccountId}/beneficiaries.
func (a *accountInfo) GetAccountsAccountIdBeneficiaries(ctx context.Context, req accountinfo.GetAccountsAccountIdBeneficiariesRequestObject) (accountinfo.GetAccountsAccountIdBeneficiariesResponseObject, error) {
	body, err := a.beneficiaries(ctx, req.AccountIdParameter)
	if err != nil {
		return nil, err
	}
	return accountinfo.GetAccountsAccountIdBeneficiaries200JSONResponse{N200AccountsAccountIdBeneficiariesReadJSONResponse: accountinfo.N200AccountsAccountIdBeneficiariesReadJSONResponse{Body: body,
		Headers: accountinfo.N200AccountsAccountIdBeneficiariesReadResponseHeaders{XFapiInteractionId: interaction(req.Params.XFapiInteractionId)}}}, nil
}

// GetBeneficiaries implements GET /beneficiaries.
func (a *accountInfo) GetBeneficiaries(ctx context.Context, req accountinfo.GetBeneficiariesRequestObject) (accountinfo.GetBeneficiariesResponseObject, error) {
	body, err := a.beneficiaries(ctx, "")
	if err != nil {
		return nil, err
	}
	return accountinfo.GetBeneficiaries200JSONResponse{N200BeneficiariesReadJSONResponse: accountinfo.N200BeneficiariesReadJSONResponse{Body: body,
		Headers: accountinfo.N200BeneficiariesReadResponseHeaders{XFapiInteractionId: interaction(req.Params.XFapiInteractionId)}}}, nil
}

// GetAccountsAccountIdStandingOrders implements GET /accounts/{AccountId}/standing-orders.
func (a *accountInfo) GetAccountsAccountIdStandingOrders(ctx context.Context, req accountinfo.GetAccountsAccountIdStandingOrdersRequestObject) (accountinfo.GetAccountsAccountIdStandingOrdersResponseObject, error) {
	body, err := a.standingOrders(ctx, req.AccountIdParameter)
	if err != nil {
		return nil, err
	}
	return accountinfo.GetAccountsAccountIdStandingOrders200JSONResponse{N200AccountsAccountIdStandingOrdersReadJSONResponse: accountinfo.N200AccountsAccountIdStandingOrdersReadJSONResponse{Body: body,
		Headers: accountinfo.N200AccountsAccountIdStandingOrdersReadResponseHeaders{XFapiInteractionId: interaction(req.Params.XFapiInteractionId)}}}, nil
}

// GetStandingOrders implements GET /standing-orders.
func (a *accountInfo) GetStandingOrders(ctx context.Context, req accountinfo.GetStandingOrdersRequestObject) (accountinfo.GetStandingOrdersResponseObject, error) {
	body, err := a.standingOrders(ctx, "")
	if err != nil {
		return nil, err
	}
	return accountinfo.GetStandingOrders200JSONResponse{N200StandingOrdersReadJSONResponse: accountinfo.N200StandingOrdersReadJSONResponse{Body: body,
		Headers: accountinfo.N200StandingOrdersReadResponseHeaders{XFapiInteractionId: interaction(req.Params.XFapiInteractionId)}}}, nil
}

// GetAccountsAccountIdDirectDebits implements GET /accounts/{AccountId}/direct-debits.
func (a *accountInfo) GetAccountsAccountIdDirectDebits(ctx context.Context, req accountinfo.GetAccountsAccountIdDirectDebitsRequestObject) (accountinfo.GetAccountsAccountIdDirectDebitsResponseObject, error) {
	body, err := a.directDebits(ctx, req.AccountIdParameter)
	if err != nil {
		return nil, err
	}
	return accountinfo.GetAccountsAccountIdDirectDebits200JSONResponse{N200AccountsAccountIdDirectDebitsReadJSONResponse: accountinfo.N200AccountsAccountIdDirectDebitsReadJSONResponse{Body: body,
		Headers: accountinfo.N200AccountsAccountIdDirectDebitsReadResponseHeaders{XFapiInteractionId: interaction(req.Params.XFapiInteractionId)}}}, nil
}

// GetDirectDebits implements GET /direct-debits.
func (a *accountInfo) GetDirectDebits(ctx context.Context, req accountinfo.GetDirectDebitsRequestObject) (accountinfo.GetDirectDebitsResponseObject, error) {
	body, err := a.directDebits(ctx, "")
	if err != nil {
		return nil, err
	}
	return accountinfo.GetDirectDebits200JSONResponse{N200DirectDebitsReadJSONResponse: accountinfo.N200DirectDebitsReadJSONResponse{Body: body,
		Headers: accountinfo.N200DirectDebitsReadResponseHeaders{XFapiInteractionId: interaction(req.Params.XFapiInteractionId)}}}, nil
}

// GetAccountsAccountIdScheduledPayments implements GET /accounts/{AccountId}/scheduled-payments.
func (a *accountInfo) GetAccountsAccountIdScheduledPayments(ctx context.Context, req accountinfo.GetAccountsAccountIdScheduledPaymentsRequestObject) (accountinfo.GetAccountsAccountIdScheduledPaymentsResponseObject, error) {
	body, err := a.scheduledPayments(ctx, req.AccountIdParameter)
	if err != nil {
		return nil, err
	}
	return accountinfo.GetAccountsAccountIdScheduledPayments200JSONResponse{N200AccountsAccountIdScheduledPaymentsReadJSONResponse: accountinfo.N200AccountsAccountIdScheduledPaymentsReadJSONResponse{Body: body,
		Headers: accountinfo.N200AccountsAccountIdScheduledPaymentsReadResponseHeaders{XFapiInteractionId: interaction(req.Params.XFapiInteractionId)}}}, nil
}

// GetScheduledPayments implements GET /scheduled-payments.
func (a *accountInfo) GetScheduledPayments(ctx context.Context, req accountinfo.GetScheduledPaymentsRequestObject) (accountinfo.GetScheduledPaymentsResponseObject, error) {
	body, err := a.scheduledPayments(ctx, "")
	if err != nil {
		return nil, err
	}
	return accountinfo.GetScheduledPayments200JSONResponse{N200ScheduledPaymentsReadJSONResponse: accountinfo.N200ScheduledPaymentsReadJSONResponse{Body: body,
		Headers: accountinfo.N200ScheduledPaymentsReadResponseHeaders{XFapiInteractionId: interaction(req.Params.XFapiInteractionId)}}}, nil
}
