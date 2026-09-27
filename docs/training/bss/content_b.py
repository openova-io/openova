"""Modules 6-12: cost analysis, billing, finance, partners, capacity, access & notifications, wrap-up."""


def add(d):
    # ------------------------------------------------------------------ 6
    d.section(6, "Cost analysis", "Everyone reads the same priced ledger: overview, explorer, cost centres, allocation, budgets, anomalies, recommendations, reports.",
              ["Overview and the explorer", "Cost centres", "Allocation is a report", "Budgets", "Anomalies and recommendations", "Reports", "The daily rollup"])
    d.shot("Overview — the month at a glance", "01-overview.png", [
        "Month to date against the previous window; run-rate; top services and top customers.",
        "Unpriced and not-sold-per-use are shown beside the total so a gap is visible, never absorbed.",
        "Everything here is one read of the daily rollup since 0.1.53 (12 statements, ~0.4 s).",
    ], kicker="Cost analysis · /overview")
    d.shot("Cost Explorer — group, filter, compare", "02-explore.png", [
        "Group by kind, SKU, region, tag, customer, source, cost centre; granularity day / month / hour; top-N with 'other'.",
        "Filters include and exclude; a custom compare window sits beside the main one.",
        "Export CSV gives exactly the rows on screen.",
    ], kicker="Cost analysis · /explore")
    d.bullets("Cost centres — one customer's spend, labelled", [
        "A cost centre has a code; a rule says 'a resource tagged key = value belongs to it'; an override pins one resource.",
        "Resolution, in order: resource override → the most specific matching tag rule → the customer's default centre → 'unassigned'.",
        "It is a dimension like region or tag, so the explorer, the report and the invoice breakdown come for free.",
        "The invoice breakdown by cost centre sums exactly to the invoice — pinned identity — and is frozen with the closed period.",
        "Rules read the tags the collector already captures; changing tagging next month changes next month, never history.",
    ], kicker="Cost analysis")
    d.shot("Allocation — a report over the two layers, never billing", "25-allocation.png", [
        "Pool = the landlord customer's rated cloud cost; shares = each Organization's platform usage; overhead = the internal source.",
        "The unattributed figure is what no rule explains; the settings editor names the landlord and the basis.",
        "Nothing here writes a bill; it explains where the cloud bill went.",
    ], kicker="Cost analysis · /allocation")
    d.shot("Budgets — thresholds in the reporting currency", "26-budgets.png", [
        "Scope all customers or one; a monthly amount; thresholds (e.g. 50 / 80 / 100 %); notification emails.",
        "The status bar shows actual (converted), a forecast marker, and which thresholds crossed.",
        "The hourly evaluator sends budget.threshold once per crossing (module 11).",
    ], kicker="Cost analysis · /budgets")
    d.shot("Anomalies — the daily series that moved", "27-anomalies.png", [
        "Per customer and per service: today's spend against the expected band from the recent series.",
        "An anomaly is a reading, not an alert rule: open the explorer from the row to see what changed.",
    ], kicker="Cost analysis · /anomalies")
    d.shot("Recommendations — what to change", "28-recommendations.png", [
        "Example: the oversized-reservation recommendation compares an Elastic IP's reserved Mbps with its measured traffic and quantifies the saving.",
        "Each recommendation names the resource, the evidence window and the money.",
    ], kicker="Cost analysis · /recommendations")
    d.shot("Reports — scheduled and on demand", "29-reports.png", [
        "A saved explorer shape delivered on a schedule to named addresses (notification report.scheduled, five-minute poll).",
        "The cost-centre report is here too: one customer's month by centre.",
    ], kicker="Cost analysis · /reports")
    d.bullets("The daily rollup — why every page is fast and still exact", [
        "cost_usage_daily holds the priced ledger summed per day, per customer, per source, per SKU, per region, per tags; the hourly chart alone reads live.",
        "The unit of freshness is a partition (a customer-day). A change to any input re-opens exactly that partition; a reader never touches a stale one.",
        "Since 0.1.53 every summary (Sovereign, customer, budgets, anomalies) is answered from ONE statement over that table: 98 → 12 statements per overview.",
        "Exactness is stated, not hoped: batched answers are tested equal to one-by-one answers across a re-opened day, unpriced and unconverted rows, rollup on and off.",
    ], kicker="Cost analysis")
    d.quiz("Cost analysis — check yourself", [
        "A resource has an override to centre A and a tag matching a rule for centre B. Where does it land?",
        "Does Allocation change any invoice?",
        "In which currency is a budget amount entered?",
    ], [
        "Centre A — the override wins over rules.",
        "No. It is a read-only report over the two layers.",
        "The reporting currency; the form shows it read-only.",
    ])

    # ------------------------------------------------------------------ 7
    d.section(7, "Billing — the journey of one period", "Run → draft → issue → send → pay → overdue → collect → credit → refund. One customer, real numbers, every screen.",
              ["The lifecycle", "Run and the draft", "Issue: number, snapshot, document", "Send", "Payments and part payments", "Overdue and collections", "Credit notes and refunds", "Prepaid and the account", "Self-service"])
    d.table("The statement lifecycle", ["From", "To", "By"], [
        ["(period)", "draft", "POST /statements/run {period} — the rating run"],
        ["draft", "issued · cancelled", "POST /statements/{id}/issue · /cancel; PATCH edits PO and terms while draft"],
        ["issued", "sent · paid · cancelled", "POST /statements/{id}/send ({notify:true} also emails the document)"],
        ["sent", "paid · overdue", "payments; overdue is DERIVED: sent + due date passed + money outstanding"],
        ["overdue", "paid", "payments, or a credit note that clears it"],
        ["paid", "— final", ""],
    ], kicker="Billing", col_widths=[1.5, 2.5, 7])
    d.bullets("Our running example: Nizwa Fintech, September 2026", [
        "Profile: billed · postpaid · transfer · PO-4471 · Net 30 · 10 % customer discount · Oman standard 5 %.",
        "September rating: list 1,000.00 → discount −100.00 → net 900.00 → tax 45.00 → total 945.00.",
        "We will run, issue, send, receive 600 then 400, watch it go overdue in between, chase it, credit 100 after a dispute, and refund the leftover.",
    ], kicker="Billing")
    d.shot("Step 1 — run the period", "30-statements.png", [
        "'Run period' rates every billed customer for the month and writes one DRAFT per customer (a partner's statement follows its customers).",
        "Running again re-rates drafts only; issued statements never move.",
        "The list shows status, number, customer, period, total, paid, outstanding.",
    ], kicker="Billing · /statements")
    d.shot("Step 2 — the draft", "32-statement-draft.png", [
        "Lines with quantity, unit price, terms applied (allowance, tier, committed rate), discount and tax.",
        "PO reference and payment terms were copied from the customer; edit them here while draft (PATCH).",
        "The contract it was rated under is named at the top.",
    ], kicker="Billing · /statements/{id} (draft)")
    d.example("Step 3 — issue: what freezes at that moment", [
        "POST /statements/{id}/issue",
        "",
        "number         INV-2026-0042   (gapless, per year)",
        "issued_at      2026-10-01",
        "due_at         2026-10-31      (Net 30)",
        "tax_snapshot   [{rule: 'Oman standard', kind: standard, rate: 5%,",
        "                 base: 900.00, tax: 45.00}]",
        "document       rendered PDF, the customer's copy",
        "journal        Dr receivable 945 · Dr discounts 100",
        "               Cr revenue.ecs 1,000 · Cr tax_payable 45",
        "status         issued",
    ], explain=[
        "From here nothing on the statement changes. A correction is a credit note.",
        "The number is gapless: a cancelled issued statement keeps its number.",
        "The tax snapshot is the invoice's own; a later rule change does not touch it.",
    ])
    d.shot("Step 3 — the issued invoice", "31-statement-issued.png", [
        "Number, dates, PO, terms, the frozen tax block, totals, and the document download.",
        "Actions from here: Send, Record payment, Credit note, Cancel.",
    ], kicker="Billing · /statements/{id} (issued)")
    d.bullets("Step 4 — send", [
        "POST /statements/{id}/send marks issued → sent. With {notify: true} it also emails the document (statement.issued).",
        "The default is not to email, because an operator often marks what was sent by other means (portal, post, an external billing system).",
        "With the outbox in external mode (module 8) the invoice is also emitted as an event to the billing system of record.",
        "The customer's users see it under /my/statements immediately, with the PDF.",
    ], kicker="Billing")
    d.example("Step 5 — payments and part payment", [
        "10 Oct  POST /statements/{id}/payments {amount 600, paid_at, ref TRF-1187}",
        "        settles 600 of 945 → outstanding 345 · shown as part-paid",
        "        journal  Dr cash 600 / Cr receivable 600",
        "",
        "31 Oct  due date passes, 345 outstanding → OVERDUE (derived)",
        "",
        "12 Nov  payment 400",
        "        345 settles the invoice → PAID",
        "        55 has nothing to settle → customer_advances (account credit)",
        "        journal  Dr cash 400 / Cr receivable 345 · Cr customer_advances 55",
    ], explain=[
        "A gateway payment goes through the gateway's ConfirmSettlement first; a signed gateway callback books it without an operator.",
        "Allocate lets you spread one payment over several invoices; Refund returns a whole payment.",
        "paid_total is the sum of received payments linked to the statement.",
    ])
    d.shot("Payments on a paid statement", "33-statement-paid.png", [
        "The payment history with reference, date, amount and what each settled.",
        "Allocate and Refund live on the payment row; a refund reverses the whole payment.",
    ], kicker="Billing · /statements/{id} (paid)")
    d.bullets("Step 6 — collections: aging, reminders, escalation, suspend", [
        "Aging per customer in five buckets (current, 1–30, 31–60, 61–90, 90+), total owed, overdue, oldest due, credit available, suspended badge.",
        "The schedule lives on billing settings: reminder days as a list (e.g. 3, 10, 20 days after due), escalation day and action.",
        "The daily evaluator sends collections.reminder per stage and collections.escalation once; every send is in the delivery log.",
        "Suspend — by escalation, by a prepaid balance at zero, or by the operator — calls the platform: the Organization CR gets spec.suspended with the reason and the actor; Resume clears it.",
        "Nothing is written off automatically; write-off is an explicit action with its own posting.",
    ], kicker="Billing")
    d.shot("Collections — the aging board", "35-collections.png", [
        "One row per customer with the five buckets and the actions: Remind now, Escalate, Suspend / Resume.",
        "The strip at the top: total owed, overdue, customers overdue, credit available.",
    ], kicker="Billing · /collections")
    d.shot("Billing settings — numbering, terms, schedule, combination rule", "34-billing.png", [
        "Invoice and credit-note numbering, default terms, the reporting currency, the discount combination rule, tax defaults.",
        "The collections schedule: reminder days, escalation day and action.",
        "The billing system of record: internal, or external through the outbox (module 8).",
    ], kicker="Billing · /billing")
    d.example("Step 7 — dispute, credit note, refund", [
        "Nizwa disputes one line on INV-2026-0042 (already paid).",
        "",
        "POST /statements/{id}/credit-notes {amount 100, reason}",
        "  CN-2026-0007 · applied 0 (nothing open) · unapplied 100 → account credit",
        "  journal  Dr credit_notes 100 / Cr customer_advances 100",
        "  account balance: 55 + 100 = 155 in credit",
        "",
        "Refund 55 by transfer",
        "  journal  Dr customer_advances 55 / Cr cash 55",
        "  balance 100 — applied to October's invoice at issue",
    ], explain=[
        "A credit note against an OPEN invoice reduces the receivable first; only the remainder becomes credit.",
        "The account tab shows the ledger: every payment, credit note, top-up, refund and application in order.",
    ])
    d.shot("The customer's Account tab — the ledger and the balance", "05-customer-06-account.png", [
        "Balance, then every entry: invoice, payment, credit note, top-up, apply-credit, refund.",
        "Top-up and Apply credit are actions here; a prepaid customer's service depends on this balance.",
    ], kicker="Billing · /customers/{id}?tab=account")
    d.bullets("Prepaid — the same machinery, one difference", [
        "The customer tops up first (transfer booked by the operator, or a gateway checkout the customer starts — account.topup).",
        "At issue the invoice settles from the balance immediately; what the balance cannot cover stays outstanding.",
        "account.low_balance warns when the balance falls under the threshold; at zero the wallet suspends the Organization if the setting says so.",
        "Account credit is universal: a postpaid customer can hold credit too (an overpayment, a credit note).",
    ], kicker="Billing")
    d.shot("Statements tab — the customer's own history", "05-customer-05-statements.png", [
        "Every period for this customer with status and outstanding; this is what its own users see at /my/statements.",
    ], kicker="Billing · ?tab=statements")
    d.bullets("Self-service — what the customer does without you", [
        "/my/overview and /my/explore: its own cost, same explorer, confined to its customer.",
        "/my/statements: the list, the PDF download, the payment history.",
        "/my/account and /my/payment-methods: balance, top-up through the gateway, a saved payment method.",
        "Dispute an invoice: opens a dispute the operator answers with a credit note or a rejection; an open dispute blocks the period close (module 8).",
        "/my/users, /my/sources, /my/notifications, /my/cost-centres, /my/budgets: the owner manages its own people, credentials, preferences, centres and budgets.",
    ], kicker="Billing")
    d.quiz("Billing — check yourself", [
        "Is 'overdue' a status you can set?",
        "A 400 payment arrives against 345 outstanding. Where does the 55 go?",
        "Can a customer cancel its own issued invoice?",
        "What does {notify:true} on send change?",
    ], [
        "No. It is derived from sent + due date passed + outstanding money.",
        "To the customer's account as credit (customer_advances).",
        "No. It can dispute it; the operator answers with a credit note.",
        "The document is emailed (statement.issued) in addition to the status change.",
    ])

    # ------------------------------------------------------------------ 8
    d.section(8, "Finance", "The journal every billing event posts to, the period close, gateway reconciliation and the seam to an external system of record.",
              ["Accounts", "Events → postings", "Which month", "Reconciliation", "Period close", "The outbox"])
    d.table("The accounts", ["Key", "Default", "What posts to it"], [
        ["receivable", "1100", "what customers owe"],
        ["cash", "1000", "money that moved, for a transfer or an internal recharge"],
        ["gateway_clearing", "1010", "money a gateway holds between collecting and settling"],
        ["customer_advances", "2100", "money held on account: a top-up, a payment beyond what it settled"],
        ["tax_payable", "2200", "tax an invoice owes, one line per frozen rule"],
        ["revenue · revenue.<service>", "4000", "revenue, per service when the operator added the account"],
        ["discounts", "4800", "what discounts took off list, as contra revenue"],
        ["credit_notes", "4900", "the reduction of an invoice through a credit note"],
        ["write_offs · gateway_fees · commission", "6100 · 6200 · 6300", "a receivable given up · what a gateway kept · what a partner is owed"],
    ], kicker="Finance", col_widths=[3.2, 1.6, 6.2])
    d.table("Events and their postings", ["Event", "Debit", "Credit"], [
        ["Invoice issued", "receivable (total) · discounts (discount total)", "revenue.<service> (list, per service) · tax_payable (per rule)"],
        ["Payment received", "cash or gateway_clearing", "receivable (what it settled) · customer_advances (the rest)"],
        ["Top-up", "cash or gateway_clearing", "customer_advances"],
        ["Account credit applied", "customer_advances", "receivable"],
        ["Credit note", "credit_notes (total)", "receivable (applied) · customer_advances (unapplied)"],
        ["Write-off", "write_offs (total)", "receivable (applied) · customer_advances (unapplied)"],
        ["Refund", "receivable, or customer_advances for a refunded top-up", "cash or gateway_clearing"],
        ["Settlement fee", "gateway_fees", "gateway_clearing"],
        ["Commission statement issued", "commission", "the partner's account (credit)"],
    ], kicker="Finance", col_widths=[2.4, 4.3, 4.3])
    d.example("Nizwa's September, as the journal sees it", [
        "01 Oct  invoice INV-2026-0042 issued",
        "        Dr receivable        945.00     Cr revenue.ecs    1,000.00",
        "        Dr discounts         100.00     Cr tax_payable       45.00",
        "10 Oct  payment 600 (transfer)",
        "        Dr cash              600.00     Cr receivable       600.00",
        "12 Nov  payment 400",
        "        Dr cash              400.00     Cr receivable       345.00",
        "                                       Cr customer_advances 55.00",
        "        credit note CN-2026-0007 100",
        "        Dr credit_notes      100.00     Cr customer_advances 100.00",
        "        refund 55",
        "        Dr customer_advances  55.00     Cr cash              55.00",
    ], explain=[
        "Every event balances on its own; the period balance is asserted at close.",
        "The discount is a contra line, never apportioned into revenue.",
        "Multi-rate tax posts one tax_payable line per frozen rule.",
    ])
    d.bullets("Which month an event belongs to", [
        "An invoice posts to its billing period. A payment posts to the month it was received, split between what it settled and the advance.",
        "A credit note posts to the month it was issued, against the invoice it names.",
        "A closed month accepts nothing new: a late payment against a September invoice posts to the month it arrives.",
        "GET /finance/journal for a closed period is served from the frozen lines, so an export is stable forever.",
    ], kicker="Finance")
    d.shot("The journal", "36-finance-journal.png", [
        "Period selector; one row per posting line with account, debit, credit, the event and the document it came from.",
        "Totals per account at the bottom; the balance check is visible before you close.",
    ], kicker="Finance · /finance/journal")
    d.table("Gateway settlement reconciliation — four buckets", ["Bucket", "Meaning", "What you do"], [
        ["matched", "the reference is on both sides for the same amount", "nothing"],
        ["amount-mismatch", "both sides have the reference, amounts differ — both figures shown, neither believed", "check the gateway fee or a partial capture; post the fee"],
        ["missing-in-ledger", "the gateway settled something no payment records", "record the payment or find the failed callback"],
        ["missing-in-settlement", "a payment is recorded the gateway has not settled", "wait for the next settlement file or void the payment"],
    ], kicker="Finance", col_widths=[2.2, 5, 3.8])
    d.shot("Reconciliation", "37-finance-reconciliation.png", [
        "Upload or fetch the settlement, see the four buckets, drill into each row.",
        "The settlement fee posts gateway_fees / gateway_clearing from here.",
    ], kicker="Finance · /finance/reconciliation")
    d.bullets("Period close — refuses while anything still moves", [
        "GET /finance/periods/{period} shows, before you press anything: drafts still open, disputes still open, and the balance check as a figure.",
        "POST …/close refuses with 409 NAMING each draft and each open dispute; refuses with 422 if the journal does not balance.",
        "Then it stamps the period closed and freezes its journal lines in one transaction.",
        "After close: no statement in the period can be issued or credited into it, no notification is sent from it, the cost-centre breakdown is frozen.",
    ], kicker="Finance")
    d.shot("Periods", "38-finance-periods.png", [
        "One row per month: open / closed, statements, drafts, disputes, balance check, close button.",
    ], kicker="Finance · /finance/periods")
    d.shot("Accounts", "39-finance-accounts.png", [
        "The chart of accounts with defaults; add revenue.<service> accounts to split revenue per service.",
    ], kicker="Finance · /finance/accounts")
    d.bullets("The outbox — the seam to an external billing system", [
        "Billing settings choose the system of record: internal (BSS issues, collects, posts) or external.",
        "External: every commercial event (invoice issued, credit note, payment, balance, enforcement) is written to a TMF-shaped outbox and delivered; a CSV exporter serves systems that take files.",
        "Inbound: signed imports for status, payment, balance and enforcement, so the external system can tell BSS what it did.",
        "A failed delivery is retried and visible; 'Retry' needs billing.issue. Nothing is dropped silently.",
        "Adapters for Omantel's gateway and billing wait on Omantel's specification; the seam is what they plug into.",
    ], kicker="Finance")
    d.shot("Outbox", "40-finance-outbox.png", [
        "Event, target, attempts, last status, payload preview, Retry.",
    ], kicker="Finance · /finance/outbox")
    d.quiz("Finance — check yourself", [
        "An invoice of 945 with a 100 discount: what is credited to revenue?",
        "A September invoice is paid in December; September is closed. Where does the payment post?",
        "Two things that stop a period from closing?",
    ], [
        "1,000 — the list amount; the discount is a separate debit line.",
        "December.",
        "An open draft in the period and an open dispute on an invoice in it (and a journal that does not balance).",
    ])

    # ------------------------------------------------------------------ 9
    d.section(9, "Partners", "Resellers and agents: one list price, two independent reductions, a derived margin, and two billing models.",
              ["The waterfall", "Resell vs agent", "A partner is a party", "The derived retail book", "Partner scope and portal", "Deleting"])
    d.example("The waterfall — pinned by TestIntegrationPartnerWaterfallResellAndAgent", [
        "partner  Walk Reseller · tier Silver = 30 % off list",
        "customer Nizwa Fintech · 10 % customer discount",
        "one unit, list 100",
        "",
        "list                    100.000000",
        "customer discount 10 %  −10.000000",
        "CUSTOMER NET             90.000000   what Nizwa pays",
        "tier discount 30 %      −30.000000",
        "PARTNER BUY              70.000000   what Walk Reseller pays us",
        "MARGIN (net − buy)       20.000000   derived, never typed",
        "tax 5 % on 90 = 4.50 → invoice total 94.500000",
    ], explain=[
        "The two reductions are independent: Nizwa's discount does not move the partner's price and the tier does not move Nizwa's bill.",
        "There is no markup field anywhere — a second typed price drifts from the first.",
        "A tier is a discount row (tier_id set, customer NULL); the same combination rule decides two tiers.",
    ])
    d.table("Two billing models (partners.bill_to)", ["", "Resell (bill_to = partner)", "Agent (bill_to = customer)"], [
        ["Who we invoice", "the partner: a wholesale statement, every customer's lines at list less the tier, grouped by end customer", "the end customer, at our own books — nothing changes because an agent introduced it"],
        ["What the end customer sees", "showback at the partner's derived retail book; the partner bills them", "our invoice, 94.50 in the example"],
        ["What the partner gets", "a real invoice on its account: numbered, due, collected, suspendable", "a commission statement: net − buy per end customer (20.00), or commission % × net without a tier; posted as a CREDIT on its account — money we owe, nothing collected"],
        ["Produced by", "the same POST /statements/run, after the customers' statements, reconcilable line for line", "the same run"],
    ], kicker="Partners", col_widths=[2.2, 4.4, 4.4])
    d.bullets("A partner is a party", [
        "Each partner owns one customers row flagged partner (billed postpaid by transfer). Balance, invoices, payments, credit notes, aging and suspension are the customer machinery.",
        "Its customers are assigned to it; assignment takes effect at the next request and at the next rating run.",
        "Tiers are the partner's price level: Silver 30 %, Gold 35 %… each a discount row.",
    ], kicker="Partners")
    d.example("The derived retail book (resell only)", [
        "rule   base = buy | list, ± pct, overridable per service or SKU",
        "",
        "base = buy  (70) + 5 %   →  73.500000   no warning",
        "base = list (100) + 5 %  → 105.000000",
        "base = buy  (70) − 10 %  →  63.000000   below the buy price 70 → 'below_buy', warned, never refused",
        "",
        "materialised as a real, read-only price_books row per list book",
        "re-derived on: list change, tier change, rule change, billing-model change,",
        "customer assignment; removed when the partner becomes an agent",
    ], explain=[
        "Every reader that knows a price book (rating, coverage, export) reads the retail book unchanged.",
        "Selling at a loss is the partner's decision; hiding it is not ours.",
    ])
    d.shot("Partners — the list", "41-partners.png", [
        "Name, status, model (resell / agent), tier, customers, retail rule, balance.",
    ], kicker="Partners · /partners")
    d.shot("A partner — tiers, customers, statements, margin, users", "42-partner-detail.png", [
        "Tabs: the party's balance and statements, its customers with net / buy / margin per period, the retail rule, and the partner users.",
        "Delete is refused by name while customers, tiers, issued statements or ledger entries depend on it; suspend instead.",
    ], kicker="Partners · /partners/{id}")
    d.bullets("Partner scope and the partner portal", [
        "A binding at partner:<id> expands, per request, to the partner's customers plus its own party.",
        "One predicate: every scoped read confines through store.Scope.Confine; a customer named outside the scope is 'not found', never the partner's own rows.",
        "/partner/overview, /partner/explore, /partner/resources, /partner/statements, /partner/account, /partner/margin, /partner/retail, /partner/users — the same pages, confined.",
    ], kicker="Partners")
    d.quiz("Partners — check yourself", [
        "Resell model: who invoices Nizwa?",
        "Agent model with no tier and commission 15 %: what is the partner's line on 90 net?",
        "Can you type a markup per SKU for a reseller?",
    ], [
        "The partner; we invoice the partner a wholesale statement at list less the tier.",
        "13.50 (15 % of the customer net), credited to the partner's account.",
        "No. A retail rule (base ± pct) derives the whole book; a retail price below the buy price is warned, not refused.",
    ])

    # ------------------------------------------------------------------ 10
    d.section(10, "Capacity", "What the Sovereign can actually sell: pools of machines, shapes, placements, the classes a pool can enforce, the floor and the envelope.",
              ["Pools and shapes", "Placements and classes", "Guaranteed floor and burstable envelope", "Reclaim", "Regions and zones"])
    d.bullets("The model in five lines", [
        "Pool = N identical machines × a per-machine resource vector (vCPU, memory, storage), in a zone, with reserve machines held back.",
        "Shape = a sellable SKU's footprint (e.g. ecs.m7n.2xlarge.8 = 8 vCPU, 64 GB). A family (ecs.m7n.*) can stand for all its shapes; exact wins over family.",
        "Placement = (pool, SKU or family, class): 'this pool sells this shape at this class'. A pool lists the classes it can ENFORCE: guaranteed and spot everywhere; burstable only where the host throttles at runtime.",
        "A running resource's class: its override → its lifecycle tag → the most cautious class placed for its SKU.",
        "Guaranteed floor per resource keeps burstable out of guaranteed room; the overcommit ratio applies only to what is left.",
    ], kicker="Capacity")
    d.example("Floor and envelope — the founder's case", [
        "pool usable            100 vCPU",
        "guaranteed placed (G)   60",
        "guaranteed floor        40",
        "overcommit ratio         2.5   (burstable enforced)",
        "",
        "held      = max(G, floor)           = 60",
        "envelope  = (usable − held) × ratio = 40 × 2.5 = 100",
        "sellable  = held + envelope          = 160",
        "",
        "if G drops to 20: held = max(20, 40) = 40 → envelope 150 → sellable 190",
    ], explain=[
        "The floor is what guaranteed can always claim back; burstable may never be sold into it.",
        "Spot may use idle floor and is the first to be reclaimed, newest first.",
        "Where burstable is not enforced, ratio is forced to 1 and floor to 0 — the arithmetic collapses to plain counting.",
    ])
    d.shot("Pools", "43-capacity-pools.png", [
        "One row per pool: zone, machines, per-machine vector, classes enforced, ratio, floor, sellable per resource.",
        "Add a pool from the row action; edit opens the same modal with its values.",
    ], kicker="Capacity · /capacity?tab=pools")
    d.shot("A pool — placements, running resources, reclaim", "47-capacity-pool-detail.png", [
        "The pool's placements per class, the running resources and their resolved class, fit against the soft wall.",
        "Reclaim proposes which spot to give back, newest first; BSS says how much and which, the platform deletes.",
    ], kicker="Capacity · pool")
    d.shot("Placements", "44-capacity-placements.png", [
        "(pool, SKU or family, class) rows; a family shows which SKUs it matched; a class mismatch is flagged.",
        "SKU is a dropdown from the price books, two levels: family, then exact shape.",
    ], kicker="Capacity · ?tab=placements")
    d.shot("Shapes", "45-capacity-shapes.png", [
        "The footprint per SKU; families expand to their shapes; edit a shape's vector here.",
    ], kicker="Capacity · ?tab=shapes")
    d.shot("Regions and zones", "46-capacity-regions.png", [
        "A table with row-level add, edit and delete; a zone is added from its region's row.",
    ], kicker="Capacity · ?tab=regions")
    d.quiz("Capacity — check yourself", [
        "A pool enforces guaranteed and spot only. What is its overcommit ratio?",
        "Usable 100, G 60, floor 40, ratio 2.5: how much is sellable?",
        "A resource has no override and no lifecycle tag; its SKU is placed as guaranteed and burstable. What class counts?",
    ], [
        "1 — ratio and floor exist only where burstable is enforced.",
        "160.",
        "Guaranteed — the most cautious placed class.",
    ])

    # ------------------------------------------------------------------ 11
    d.section(11, "Access and notifications", "Who may do what, at which scope; and what the product sends, to whom, with proof it arrived.",
              ["Permissions and roles", "Bindings and directory groups", "The notification catalogue", "Preferences and channels", "The delivery log"])
    d.table("The eleven permissions", ["Permission", "Grants"], [
        ["metering.read", "every read surface: usage, cost, resources, statements, account, budgets, reports, sources, price books"],
        ["rating.manage", "price books and items, discounts, currency rates"],
        ["customers.manage", "create, edit, invite, import, delete customers and their sources, budgets, schedules"],
        ["billing.issue", "run, issue, edit a draft, send, cancel, delete statements; credit notes; retry the outbox"],
        ["billing.collect", "record, allocate, refund payments; apply credit; run collections; suspend and resume"],
        ["account.topup", "start a gateway checkout for the customer's OWN account — the one money write a customer may make"],
        ["settings.manage", "billing settings, allocation settings, access itself; period close"],
        ["audit.read", "audit trails (Sovereign only)"],
        ["customer.self.manage", "the owner's subset on its own customer: users, source credentials, PO, tax registration"],
        ["capacity.manage", "regions, zones, pools, shapes, placements"],
        ["sovereign", "everything"],
    ], kicker="Access", col_widths=[2.4, 8], size=12)
    d.shot("Access — bindings and directory groups", "48-access.png", [
        "Role bindings: email → role → scope (sovereign, customer:<id>, partner:<id>).",
        "Directory group mappings: a group from the SSO gate → role → scope; the permission-gated console follows.",
        "/api/v1/me tells any page what the session may do; a missing permission hides the action and 403s the route.",
    ], kicker="Access · /access")
    d.table("The notification catalogue", ["Event", "What it is", "Mandatory", "Sent by"], [
        ["auth.pin", "the one-time sign-in code", "yes", "POST /auth/pin/request"],
        ["customer.invite", "the activation link", "yes", "POST /customers/{id}/invite"],
        ["statement.issued", "the invoice", "yes", "issue, and send with notify"],
        ["collections.reminder", "one dunning stage", "yes", "the daily collections pass"],
        ["collections.escalation", "the overdue escalation", "yes", "the daily collections pass"],
        ["account.low_balance", "the prepaid low-balance warning", "no", "after any account change"],
        ["budget.threshold", "a budget threshold crossing", "no", "the hourly budget pass"],
        ["report.scheduled", "a scheduled cost report", "no", "the five-minute report poll"],
    ], kicker="Notifications", col_widths=[2.4, 3.6, 1.4, 3.6])
    d.bullets("Preferences, channels, templates, the log", [
        "Preference: per customer and recipient, an event can be switched off unless it is mandatory; the effective state is shown per event.",
        "Channels: one interface; email has a real transport (the Sovereign's SMTP); a channel declared without a transport answers 'unavailable' honestly instead of pretending.",
        "Templates per event and locale; the subject and body are golden-tested to render exactly what the old inline renderer did.",
        "Delivery log: every attempt with recipient, channel, status — sent, retrying, failed (the visible one), suppressed, unavailable — and the reason.",
        "Nothing is sent from a closed period.",
    ], kicker="Notifications")
    d.shot("Notifications — catalogue, preferences, delivery log", "49-notifications.png", [
        "Three tabs: the catalogue with effective channels per event, the preference rules, and the log with status and reason per attempt.",
    ], kicker="Notifications · /notifications")
    d.quiz("Access & notifications — check yourself", [
        "Which single permission lets a customer move money?",
        "Can a customer switch off the invoice email?",
        "A delivery shows 'unavailable'. Is the address wrong?",
    ], [
        "account.topup — and only for its own account, through the gateway.",
        "No. statement.issued is mandatory; low-balance, budget and report mails can be switched off.",
        "No. The channel has no transport configured; 'failed' is the one that means the send was attempted and lost.",
    ])

    # ------------------------------------------------------------------ 12
    d.section(12, "Putting it together", "The journey once more, the vocabulary, and the exercises to run on hw307.", None)
    d.table("The journey, with the numbers from this course", ["Step", "Nizwa Fintech example", "Page"], [
        ["Lead", "priced 10 × ecs.m7n.2xlarge.8, left an address", "/estimate → /leads"],
        ["Customer", "billed · postpaid · transfer · PO-4471 · Net 30", "/customers/new"],
        ["Sources", "cloud project on 'National Cloud 2026 list'; Organization on 'OpenOva plans'", "?tab=sources"],
        ["Metering", "usage records per hour; resize split; EIP by charge mode", "/resources"],
        ["Rating", "list 1,000 → discount 10 % → net 900 → tax 45", "/pricebooks · /discounts · /tax"],
        ["Contract", "7,440 h committed at 0.35; minimum 1,000 → true-up when short", "/contracts"],
        ["Billing", "INV-2026-0042 · 600 + 400 paid · 55 advance · CN 100 · refund 55", "/statements · /collections"],
        ["Finance", "postings balance; September closed; December payment posts to December", "/finance/*"],
        ["Partner", "Walk Reseller buys at 70, Nizwa pays 90, margin 20", "/partners"],
    ], kicker="Wrap-up", col_widths=[1.4, 6.6, 3])
    d.two_col("Vocabulary — one line each",
              "Commercial", [
                  "Customer — the billed party. Partner — a party that resells or introduces.",
                  "Source — where usage comes from; priced by one book.",
                  "Price book — list prices by scope. Plan — a plan-hour item in a platform book.",
                  "Allowance — included quantity per period. Tier — a price ladder with a mode.",
                  "Contract — negotiated terms: allowances, commitments, minimum, SLA, renewal.",
                  "Discount — percent or amount under a combination rule; a tier is one.",
              ],
              "Operational", [
                  "Usage record — one resource, one meter, one hour. Priced ledger — usage × price.",
                  "Statement — a customer's period: draft → issued → sent → paid; overdue is derived.",
                  "Credit note — the only way to correct an issued invoice. Advance — money on account.",
                  "Journal — every event's postings; a closed period is frozen.",
                  "Outbox — the seam to an external billing system.",
                  "Pool · shape · placement · class · floor — what can be sold, and how much.",
              ], kicker="Wrap-up")
    d.bullets("Exercises on hw307 (do them in this order)", [
        "1. On /estimate price 2 servers and a disk, save with an address, find it on /leads.",
        "2. Create customer 'Training Co' (billed, postpaid, transfer, Net 30); add a platform source on 'OpenOva plans'.",
        "3. In 'OpenOva plans' open plan.m → Add tiers or allowance → 50 GB eip.traffic_gb. Explain where the 51st GB is priced.",
        "4. Create a contract for Training Co: minimum 500, one allowance of 100 GB, one commitment; set it active.",
        "5. Run September, open the draft, edit the PO, issue, send with notify, record a part payment, check the account tab.",
        "6. On /collections send a reminder; suspend; resume. Find the three sends in /notifications → log.",
        "7. Issue a credit note of 50 against the invoice; read /finance/journal for the month; try to close the period and read the refusal.",
        "8. Make 'Walk Reseller' the partner of Training Co; run again; compare the wholesale statement with Training Co's.",
        "9. On /capacity add a pool with burstable enforced, ratio 2, floor 30; place ecs.m7n.* as guaranteed and burstable; read sellable.",
    ], kicker="Wrap-up")
    d.title("Questions, per module", "Ask by module number and screen; every slide names its page path.", "Catalyst BSS · products/chargeback · DESIGN.md is the reference behind every slide.")
