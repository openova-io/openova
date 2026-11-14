package api

// The account, balance and party operations of the account-information document. Their data
// comes from the ledger port: accounts and balances are always offered; the party is offered
// when the ledger's capabilities name it. The rules shared by every read operation are in
// ais_common.go.

import (
	"context"
	"slices"

	ledgerv1 "github.com/openova-io/openova/products/fingate/api/ledger/v1"
	"github.com/openova-io/openova/products/fingate/internal/gen/accountinfo"
)

// accountObject is one account in the documents' form.
func accountObject(acc *ledgerv1.Account, g grant) map[string]any {
	m := map[string]any{"AccountId": acc.GetAccountId()}
	put(m, "Status", acc.GetStatus())
	put(m, "Currency", acc.GetCurrency())
	put(m, "AccountCategory", acc.GetType())
	put(m, "AccountTypeCode", acc.GetSubType())
	put(m, "Nickname", cut(acc.GetNickname(), 70))
	put(m, "OpeningDate", acc.GetOpenedAt())
	if g.detail("Accounts") {
		if id := cashAccount(acc.GetSchemeName(), acc.GetIdentification(), acc.GetHolderName(), g.has("ReadPAN")); id != nil {
			m["Account"] = []any{id}
		}
	}
	return m
}

// accounts answers the consent's accounts, or the one account of the path.
func (a *accountInfo) accounts(ctx context.Context, account string) (accountinfo.OBReadAccount6, error) {
	g, err := a.grantOf(ctx)
	if err != nil {
		return accountinfo.OBReadAccount6{}, err
	}
	var list []*ledgerv1.Account
	if account != "" {
		acc, err := a.s.cfg.Ledger.Account(ctx, g.customer, account)
		if err != nil {
			return accountinfo.OBReadAccount6{}, ledgerAnswer(err, account, false)
		}
		list = append(list, acc)
	} else {
		all, err := a.s.cfg.Ledger.Accounts(ctx, g.customer)
		if err != nil {
			return accountinfo.OBReadAccount6{}, ledgerAnswer(err, "", false)
		}
		for _, acc := range all {
			if slices.Contains(g.accounts, acc.GetAccountId()) {
				list = append(list, acc)
			}
		}
	}
	items := make([]any, 0, len(list))
	for _, acc := range list {
		items = append(items, accountObject(acc, g))
	}
	return fill[accountinfo.OBReadAccount6](map[string]any{"Data": map[string]any{"Account": items}, "Links": a.links(ctx, "")})
}

// balances answers the balances of the account of the path, or of every account of the consent.
func (a *accountInfo) balances(ctx context.Context, account string) (accountinfo.OBReadBalance1, error) {
	g, err := a.grantOf(ctx)
	if err != nil {
		return accountinfo.OBReadBalance1{}, err
	}
	accounts := g.accounts
	if account != "" {
		accounts = []string{account}
	}
	items := []any{}
	for _, acc := range accounts {
		list, err := a.s.cfg.Ledger.Balances(ctx, g.customer, acc)
		if err != nil {
			return accountinfo.OBReadBalance1{}, ledgerAnswer(err, acc, account == "")
		}
		for _, b := range list {
			m := map[string]any{"AccountId": acc, "CreditDebitIndicator": b.GetCreditDebit(), "Type": b.GetType(), "DateTime": b.GetAt()}
			putMoney(m, "Amount", b.GetAmount())
			items = append(items, m)
		}
	}
	return fill[accountinfo.OBReadBalance1](map[string]any{"Data": map[string]any{"Balance": items}, "Links": a.links(ctx, "")})
}

// partyObject is one party in the documents' form.
func partyObject(p *ledgerv1.Party) map[string]any {
	m := map[string]any{"PartyId": p.GetPartyId()}
	put(m, "PartyType", p.GetType())
	put(m, "Name", cut(p.GetName(), 350))
	put(m, "EmailAddress", cut(p.GetEmail(), 256))
	if phoneRE.MatchString(p.GetPhone()) {
		m["Phone"] = p.GetPhone()
	}
	return m
}

// party reads the holder of the account of the path, or the customer when account is "".
func (a *accountInfo) party(ctx context.Context, account string) (map[string]any, error) {
	g, err := a.grantOf(ctx)
	if err != nil {
		return nil, err
	}
	p, err := a.s.cfg.Ledger.Party(ctx, g.customer, account)
	if err != nil {
		return nil, ledgerAnswer(err, account, true)
	}
	return partyObject(p), nil
}

// GetAccounts implements GET /accounts.
func (a *accountInfo) GetAccounts(ctx context.Context, req accountinfo.GetAccountsRequestObject) (accountinfo.GetAccountsResponseObject, error) {
	body, err := a.accounts(ctx, "")
	if err != nil {
		return nil, err
	}
	return accountinfo.GetAccounts200JSONResponse{N200AccountsReadJSONResponse: accountinfo.N200AccountsReadJSONResponse{Body: body,
		Headers: accountinfo.N200AccountsReadResponseHeaders{XFapiInteractionId: interaction(req.Params.XFapiInteractionId)}}}, nil
}

// GetAccountsAccountId implements GET /accounts/{AccountId}.
func (a *accountInfo) GetAccountsAccountId(ctx context.Context, req accountinfo.GetAccountsAccountIdRequestObject) (accountinfo.GetAccountsAccountIdResponseObject, error) {
	body, err := a.accounts(ctx, req.AccountIdParameter)
	if err != nil {
		return nil, err
	}
	return accountinfo.GetAccountsAccountId200JSONResponse{N200AccountsAccountIdReadJSONResponse: accountinfo.N200AccountsAccountIdReadJSONResponse{Body: body,
		Headers: accountinfo.N200AccountsAccountIdReadResponseHeaders{XFapiInteractionId: interaction(req.Params.XFapiInteractionId)}}}, nil
}

// GetAccountsAccountIdBalances implements GET /accounts/{AccountId}/balances.
func (a *accountInfo) GetAccountsAccountIdBalances(ctx context.Context, req accountinfo.GetAccountsAccountIdBalancesRequestObject) (accountinfo.GetAccountsAccountIdBalancesResponseObject, error) {
	body, err := a.balances(ctx, req.AccountIdParameter)
	if err != nil {
		return nil, err
	}
	return accountinfo.GetAccountsAccountIdBalances200JSONResponse{N200AccountsAccountIdBalancesReadJSONResponse: accountinfo.N200AccountsAccountIdBalancesReadJSONResponse{Body: body,
		Headers: accountinfo.N200AccountsAccountIdBalancesReadResponseHeaders{XFapiInteractionId: interaction(req.Params.XFapiInteractionId)}}}, nil
}

// GetBalances implements GET /balances.
func (a *accountInfo) GetBalances(ctx context.Context, req accountinfo.GetBalancesRequestObject) (accountinfo.GetBalancesResponseObject, error) {
	body, err := a.balances(ctx, "")
	if err != nil {
		return nil, err
	}
	return accountinfo.GetBalances200JSONResponse{N200BalancesReadJSONResponse: accountinfo.N200BalancesReadJSONResponse{Body: body,
		Headers: accountinfo.N200BalancesReadResponseHeaders{XFapiInteractionId: interaction(req.Params.XFapiInteractionId)}}}, nil
}

// GetAccountsAccountIdParty implements GET /accounts/{AccountId}/party.
func (a *accountInfo) GetAccountsAccountIdParty(ctx context.Context, req accountinfo.GetAccountsAccountIdPartyRequestObject) (accountinfo.GetAccountsAccountIdPartyResponseObject, error) {
	p, err := a.party(ctx, req.AccountIdParameter)
	if err != nil {
		return nil, err
	}
	body, err := fill[accountinfo.OBReadParty2](map[string]any{"Data": map[string]any{"Party": p}, "Links": a.links(ctx, "")})
	if err != nil {
		return nil, err
	}
	return accountinfo.GetAccountsAccountIdParty200JSONResponse{N200AccountsAccountIdPartyReadJSONResponse: accountinfo.N200AccountsAccountIdPartyReadJSONResponse{Body: body,
		Headers: accountinfo.N200AccountsAccountIdPartyReadResponseHeaders{XFapiInteractionId: interaction(req.Params.XFapiInteractionId)}}}, nil
}

// GetAccountsAccountIdParties implements GET /accounts/{AccountId}/parties: the port knows one
// party per account, so the list holds that one.
func (a *accountInfo) GetAccountsAccountIdParties(ctx context.Context, req accountinfo.GetAccountsAccountIdPartiesRequestObject) (accountinfo.GetAccountsAccountIdPartiesResponseObject, error) {
	p, err := a.party(ctx, req.AccountIdParameter)
	if err != nil {
		return nil, err
	}
	body, err := fill[accountinfo.OBReadParty3](map[string]any{"Data": map[string]any{"Party": []any{p}}, "Links": a.links(ctx, "")})
	if err != nil {
		return nil, err
	}
	return accountinfo.GetAccountsAccountIdParties200JSONResponse{N200AccountsAccountIdPartiesReadJSONResponse: accountinfo.N200AccountsAccountIdPartiesReadJSONResponse{Body: body,
		Headers: accountinfo.N200AccountsAccountIdPartiesReadResponseHeaders{XFapiInteractionId: interaction(req.Params.XFapiInteractionId)}}}, nil
}

// GetParty implements GET /party: the customer who granted the consent.
func (a *accountInfo) GetParty(ctx context.Context, req accountinfo.GetPartyRequestObject) (accountinfo.GetPartyResponseObject, error) {
	p, err := a.party(ctx, "")
	if err != nil {
		return nil, err
	}
	body, err := fill[accountinfo.OBReadParty2](map[string]any{"Data": map[string]any{"Party": p}, "Links": a.links(ctx, "")})
	if err != nil {
		return nil, err
	}
	return accountinfo.GetParty200JSONResponse{N200PartyReadJSONResponse: accountinfo.N200PartyReadJSONResponse{Body: body,
		Headers: accountinfo.N200PartyReadResponseHeaders{XFapiInteractionId: interaction(req.Params.XFapiInteractionId)}}}, nil
}
