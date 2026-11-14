package api

// The two transaction operations of the account-information document. The rules shared by
// every read operation are in ais_common.go.
//
// The transactions answered are those booked inside both windows: the one of the consent
// (TransactionFromDateTime, TransactionToDateTime) and the one of the call
// (fromBookingDateTime, toBookingDateTime). ReadTransactionsCredits shows the credits and
// ReadTransactionsDebits the debits; a consent with neither sees no transaction. Detail adds the
// transaction information and the counterparty: the creditor of a debit, the debtor of a
// credit. The port's balance after a transaction is not shown: the documents' balance needs a
// type the port does not carry.

import (
	"context"
	"errors"
	"time"

	ledgerv1 "github.com/openova-io/openova/products/fingate/api/ledger/v1"
	"github.com/openova-io/openova/products/fingate/internal/gen/accountinfo"
	"github.com/openova-io/openova/products/fingate/internal/ledgerclient"
)

// transactionObject is one transaction in the documents' form.
func transactionObject(account string, t *ledgerv1.Transaction, detail bool) map[string]any {
	m := map[string]any{"AccountId": account, "CreditDebitIndicator": t.GetCreditDebit(), "Status": t.GetStatus(), "BookingDateTime": t.GetBookedAt()}
	put(m, "TransactionId", cut(t.GetTransactionId(), 210))
	put(m, "TransactionReference", cut(t.GetReference(), 210))
	put(m, "ValueDateTime", t.GetValueAt())
	putMoney(m, "Amount", t.GetAmount())
	if detail {
		put(m, "TransactionInformation", cut(t.GetInformation(), 500))
		party := map[string]any{}
		put(party, "Identification", cut(t.GetCounterpartyIdentification(), 256))
		put(party, "Name", cut(t.GetCounterpartyName(), 350))
		if len(party) > 0 {
			key := "CreditorAccount"
			if t.GetCreditDebit() == "Credit" {
				key = "DebtorAccount"
			}
			m[key] = party
		}
	}
	return m
}

// window returns the later of two lower bounds and the earlier of two upper bounds, RFC 3339;
// "" is an open bound. ok is false when the window is empty.
func window(consentFrom, consentTo string, from, to *time.Time) (string, string, bool) {
	lo, hi := consentFrom, consentTo
	if from != nil {
		if f := from.UTC().Format(timeLayout); lo == "" || later(f, lo) {
			lo = f
		}
	}
	if to != nil {
		if t := to.UTC().Format(timeLayout); hi == "" || later(hi, t) {
			hi = t
		}
	}
	return lo, hi, lo == "" || hi == "" || !later(lo, hi)
}

// later says whether the time a is after the time b; both are RFC 3339.
func later(a, b string) bool {
	ta, errA := time.Parse(time.RFC3339, a)
	tb, errB := time.Parse(time.RFC3339, b)
	return errA == nil && errB == nil && ta.After(tb)
}

// transactions answers one page of the transactions of the account of the path, or of every
// account of the consent.
func (a *accountInfo) transactions(ctx context.Context, account string, from, to *time.Time) (accountinfo.OBReadTransaction6, error) {
	g, err := a.grantOf(ctx)
	if err != nil {
		return accountinfo.OBReadTransaction6{}, err
	}
	lo, hi, open := window(g.from, g.to, from, to)
	items, next := []any{}, ""
	if open {
		read := func(ctx context.Context, customer, acc, page string) (ledgerclient.Page[*ledgerv1.Transaction], error) {
			return a.s.cfg.Ledger.Transactions(ctx, customer, acc, lo, hi, page)
		}
		list, n, err := readList(ctx, g, account, read)
		if err != nil {
			if account != "" && errors.Is(err, notImplemented) {
				return accountinfo.OBReadTransaction6{}, ledgerclient.ErrNotOffered // the document declares no 404 here
			}
			return accountinfo.OBReadTransaction6{}, err
		}
		next = n
		credits, debits, detail := g.has("ReadTransactionsCredits"), g.has("ReadTransactionsDebits"), g.detail("Transactions")
		for _, x := range list {
			cd := x.item.GetCreditDebit()
			if (cd == "Credit" && credits) || (cd == "Debit" && debits) {
				items = append(items, transactionObject(x.account, x.item, detail))
			}
		}
	}
	return fill[accountinfo.OBReadTransaction6](map[string]any{"Data": map[string]any{"Transaction": items}, "Links": a.links(ctx, next)})
}

// GetAccountsAccountIdTransactions implements GET /accounts/{AccountId}/transactions.
func (a *accountInfo) GetAccountsAccountIdTransactions(ctx context.Context, req accountinfo.GetAccountsAccountIdTransactionsRequestObject) (accountinfo.GetAccountsAccountIdTransactionsResponseObject, error) {
	body, err := a.transactions(ctx, req.AccountIdParameter, req.Params.FromBookingDateTime, req.Params.ToBookingDateTime)
	if err != nil {
		return nil, err
	}
	return accountinfo.GetAccountsAccountIdTransactions200JSONResponse{N200AccountsAccountIdTransactionsReadJSONResponse: accountinfo.N200AccountsAccountIdTransactionsReadJSONResponse{Body: body,
		Headers: accountinfo.N200AccountsAccountIdTransactionsReadResponseHeaders{XFapiInteractionId: interaction(req.Params.XFapiInteractionId)}}}, nil
}

// GetTransactions implements GET /transactions.
func (a *accountInfo) GetTransactions(ctx context.Context, req accountinfo.GetTransactionsRequestObject) (accountinfo.GetTransactionsResponseObject, error) {
	body, err := a.transactions(ctx, "", req.Params.FromBookingDateTime, req.Params.ToBookingDateTime)
	if err != nil {
		return nil, err
	}
	return accountinfo.GetTransactions200JSONResponse{N200TransactionsReadJSONResponse: accountinfo.N200TransactionsReadJSONResponse{Body: body,
		Headers: accountinfo.N200TransactionsReadResponseHeaders{XFapiInteractionId: interaction(req.Params.XFapiInteractionId)}}}, nil
}
