# Writing a ledger adapter

The open-banking product reaches core banking through one typed port, the gRPC service `openova.fingate.ledger.v1.Ledger` in [`api/ledger/v1/ledger.proto`](../api/ledger/v1/ledger.proto). A ledger adapter is a program that serves this port in front of one bank's core-banking system. The product's own test bank, which serves made-up data, is the first adapter. The adapter of a real core-banking system is written in the bank's or a partner's own repository, never in this one.

This guide says what the core-banking system must supply for each operation of the port, and how to prove the adapter with the conformance program.

## Start from the template

1. Copy the folder [`adapter-template`](../adapter-template) into your repository. It is a complete program: it serves the port over gRPC and TLS, with the standard gRPC health service, and stops on SIGTERM. Every operation answers `UNIMPLEMENTED`, and `GetCapabilities` answers empty lists.
2. Delete `kit_test.go` in your copy. It imports the conformance kit, an internal package of this repository, and builds only here.
3. Keep the import path of the port package, `github.com/openova-io/openova/products/fingate/api/ledger/v1`. It lies in the Go module `github.com/openova-io/openova/products/fingate`: require that module at the commit of this repository whose port you serve. Never edit the generated files of the port.
4. Fill in the methods of `ledger.go`, one operation at a time, in the order of the sections below. Each method's comment names what the core-banking system must supply.
5. Run the conformance program after each operation (see "Prove the adapter"). The adapter is done when it exits with code 0.

The template reads its certificate once, at start. If your certificate issuer renews the files in place, serve the certificate through `GetCertificate` of `tls.Config` and read the files again when their modification time has changed.

## Rules for every operation

- **Transport.** gRPC over TLS 1.2 or later. The certificate comes from the Sovereign's internal issuer or is one the bank supplies.
- **Formats.** An amount is a decimal string that matches `^\d{1,13}$|^\d{1,13}\.\d{1,5}$`. A currency is an ISO 4217 code of three capital letters. A time is RFC 3339. A credit or debit indicator is `Credit` or `Debit`.
- **Error codes.** `NOT_FOUND`: an unknown customer, account or payment. `PERMISSION_DENIED`: the account exists but is not the customer's. `FAILED_PRECONDITION`: the bank refuses the payment; the status message says why. `UNIMPLEMENTED`: a resource or payment type that `GetCapabilities` does not name. `UNAVAILABLE`: the core-banking system cannot answer now and a retry is safe. `ALREADY_EXISTS`: an idempotency key reused with different content.
- **Pages.** A list request carries `page_token` and `page_size`. Return at most `page_size` items and a `next_page_token`; an empty `next_page_token` ends the list. Walking a list with pages of one and with pages of two must give the same items in the same order, and no item twice.
- **What the port does not carry.** The port has no consent, subscription, token or event operation. Version 1 carries domestic payments only: immediate, scheduled and standing order.

## What each operation needs from core banking

**GetCapabilities.** The optional resources the bank serves, any of `beneficiaries`, `standing-orders`, `direct-debits`, `scheduled-payments` and `party`; the payment types, any of `domestic`, `domestic-scheduled` and `domestic-standing-order`; and the currencies of its accounts, at least one. No value may repeat. Fill this in first: the conformance kit stops here while the list of currencies is empty.

**ResolveCustomer.** A lookup from the identity a customer signed in with, a `scheme` and a `subject` (and optional `attributes`), to the bank's own customer identifier, returned as `customer_id`, with a `display_name`. An unknown subject is `NOT_FOUND`. Two different people resolve to two different identifiers.

**ListAccounts.** Every account of the customer: identifier, currency, type and sub-type, nickname, scheme name and identification (such as a sort code and account number), holder name, status and opening time. An unknown customer is `NOT_FOUND`.

**GetAccount.** One account of the customer, field for field the same as its entry in `ListAccounts`. An unknown account is `NOT_FOUND`; another customer's account is `PERMISSION_DENIED`.

**ListBalances.** At least one balance per account, each with its type code, amount and currency, credit or debit indicator and the time it was taken.

**ListTransactions.** The booked transactions of one account, in pages, between the times `from` and `to`: identifier, amount, credit or debit indicator, status, booking time, value time, information, reference, counterparty name and identification, and the balance after the transaction where the bank has it. The conformance run needs an account of its test customer with at least two transactions.

**ListBeneficiaries, ListStandingOrders, ListDirectDebits, ListScheduledPayments.** Optional, one per resource of `GetCapabilities`. The payees saved on an account; its standing orders with frequency, next and final payment, creditor and status; its direct-debit mandates with the previous collection; its future-dated payments with execution time, amount and creditor. A resource the bank does not serve is left out of `GetCapabilities`, and its operation keeps answering `UNIMPLEMENTED`.

**GetParty.** Optional, resource `party`. The customer when `account_id` is empty, otherwise the holder of that account: identifier, type, name, e-mail and phone.

**CheckFunds.** Whether one account of the customer can pay an amount in its currency now, and the time of that answer.

**SubmitPayment.** The bank accepts one payment of a type that `GetCapabilities` names: debtor account, amount, creditor name, scheme and identification, reference, end-to-end identifier, an execution time for a scheduled payment, and the recurrence for a standing order (frequency code, first payment time, number of payments, first amount). It answers with a payment identifier, a status (`pending`, `accepted`, `settled` or `rejected`) and the time of that status. The same idempotency key with the same content returns the same payment; the same key with other content is `ALREADY_EXISTS`. A payment the account cannot make is `FAILED_PRECONDITION` with a message.

**GetPayment.** The current status of a payment that `SubmitPayment` returned. An unknown payment is `NOT_FOUND`.

## Prove the adapter

The conformance program `fingate-ledger-conformance` calls every operation of the port and prints one line per check, `PASS` or `FAIL` with the reason, then the number of checks passed and of operations called. It submits real payments: run it against a test system of the bank only, never against customers' accounts.

1. Build the program in a checkout of this repository:
   ```sh
   cd products/fingate && go build -o /tmp/fingate-ledger-conformance ./cmd/fingate-ledger-conformance
   ```
2. Write the fixture, a JSON file that describes the test data of your system: `customer` and `other_customer` (each an object with `scheme` and `subject`, resolving to two customers that both hold an account), `debtor_account_id` (an account of `customer`), `amount` (an amount that account can pay), `refused_amount` (an amount it cannot pay), and `creditor_name`, `creditor_scheme_name` and `creditor_identification` (the payee of every test payment). An unknown key is an error.
3. Start your adapter, then run the program against it:
   ```sh
   /tmp/fingate-ledger-conformance --target <host:port> --fixture <fixture.json> --ca-file <ca.pem>
   ```
   Exit code 0: every check passed. 1: a check failed. 2: the run could not start.

Run against the template as it is, the program prints these lines and exits with code 1:

```
FAIL capabilities: currencies is empty
FAIL every-operation-called: not called: ResolveCustomer, ListAccounts, GetAccount, ListBalances, ListTransactions, ListBeneficiaries, ListStandingOrders, ListDirectDebits, ListScheduledPayments, GetParty, CheckFunds, SubmitPayment, GetPayment
checks passed: 0/2
operations called: 1/14
```

As you fill in the operations in the order of this guide, the kit gets further: more checks run, and each operation it reaches leaves the `not called` line. The adapter is done when the program exits with code 0 and prints `operations called: 14/14`.
