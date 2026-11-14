package api

// The offer, product and statement operations of the account-information document. The ledger
// port carries no offer, product or statement, so the bank offers none of them: after the token
// check of the route, with the operation's permission (rule T9), each answers 404, the answer
// the standard asks of a bank for a resource it does not implement. The document declares 404
// for each of them.

import (
	"context"

	"github.com/openova-io/openova/products/fingate/internal/gen/accountinfo"
)

// GetAccountsAccountIdOffers implements GET /accounts/{AccountId}/offers.
func (a *accountInfo) GetAccountsAccountIdOffers(context.Context, accountinfo.GetAccountsAccountIdOffersRequestObject) (accountinfo.GetAccountsAccountIdOffersResponseObject, error) {
	return nil, notImplemented
}

// GetOffers implements GET /offers.
func (a *accountInfo) GetOffers(context.Context, accountinfo.GetOffersRequestObject) (accountinfo.GetOffersResponseObject, error) {
	return nil, notImplemented
}

// GetAccountsAccountIdProduct implements GET /accounts/{AccountId}/product.
func (a *accountInfo) GetAccountsAccountIdProduct(context.Context, accountinfo.GetAccountsAccountIdProductRequestObject) (accountinfo.GetAccountsAccountIdProductResponseObject, error) {
	return nil, notImplemented
}

// GetProducts implements GET /products.
func (a *accountInfo) GetProducts(context.Context, accountinfo.GetProductsRequestObject) (accountinfo.GetProductsResponseObject, error) {
	return nil, notImplemented
}

// GetAccountsAccountIdStatements implements GET /accounts/{AccountId}/statements.
func (a *accountInfo) GetAccountsAccountIdStatements(context.Context, accountinfo.GetAccountsAccountIdStatementsRequestObject) (accountinfo.GetAccountsAccountIdStatementsResponseObject, error) {
	return nil, notImplemented
}

// GetAccountsAccountIdStatementsStatementId implements GET /accounts/{AccountId}/statements/{StatementId}.
func (a *accountInfo) GetAccountsAccountIdStatementsStatementId(context.Context, accountinfo.GetAccountsAccountIdStatementsStatementIdRequestObject) (accountinfo.GetAccountsAccountIdStatementsStatementIdResponseObject, error) {
	return nil, notImplemented
}

// GetAccountsAccountIdStatementsStatementIdFile implements GET /accounts/{AccountId}/statements/{StatementId}/file.
func (a *accountInfo) GetAccountsAccountIdStatementsStatementIdFile(context.Context, accountinfo.GetAccountsAccountIdStatementsStatementIdFileRequestObject) (accountinfo.GetAccountsAccountIdStatementsStatementIdFileResponseObject, error) {
	return nil, notImplemented
}

// GetAccountsAccountIdStatementsStatementIdTransactions implements GET /accounts/{AccountId}/statements/{StatementId}/transactions.
func (a *accountInfo) GetAccountsAccountIdStatementsStatementIdTransactions(context.Context, accountinfo.GetAccountsAccountIdStatementsStatementIdTransactionsRequestObject) (accountinfo.GetAccountsAccountIdStatementsStatementIdTransactionsResponseObject, error) {
	return nil, notImplemented
}

// GetStatements implements GET /statements.
func (a *accountInfo) GetStatements(context.Context, accountinfo.GetStatementsRequestObject) (accountinfo.GetStatementsResponseObject, error) {
	return nil, notImplemented
}
