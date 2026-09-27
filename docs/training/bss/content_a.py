"""Modules 0-5: orientation, leads, customers, metering, price books and rating, contracts."""


def add(d):
    # ------------------------------------------------------------------ 0
    d.title("Catalyst BSS — the training course",
            "Business support for a Sovereign: from a lead to a closed month. Concepts, worked examples and the exact screens.",
            "Environment in the screenshots: hw307 (chargeback.hw307.omani.works), BSS 0.1.53, 27 Sep 2026. Example figures are illustrative unless a slide says they are pinned by a test.")
    d.section_name = "orientation"
    d.bullets("How to use this course", [
        "Twelve modules, each in the same shape: the concept, a worked example with numbers, the screen where it happens, a self-check.",
        "Follow the journey order the first time; each module builds on the previous one.",
        "Every screenshot is the live console on hw307. The page path is written under the title so you can open the same screen while you read.",
        "Money examples use OMR with 5 % tax. Where a figure is pinned by an automated test, the slide says so — those are exact.",
        "Terms: a Sovereign runs Catalyst; it bills Customers; a customer's usage arrives through Sources; a Blueprint is not a BSS concept.",
    ], kicker="Orientation")
    d.table("The journey — one line, twelve modules", ["Step", "What happens", "Module", "Console page"], [
        ["1", "A prospect prices a basket and leaves an address", "Leads", "/estimate → /leads"],
        ["2", "The customer is created with its commercial profile and its sources", "Customers & sources", "/customers"],
        ["3", "Collectors measure what the sources use", "Metering", "/resources"],
        ["4", "Usage is priced: price books, plans, discounts, tax", "Price books & rating", "/pricebooks · /discounts · /tax"],
        ["5", "Negotiated terms: allowances, commitments, minimums, SLA", "Contracts", "/contracts"],
        ["6", "Everyone reads the cost: overview, explorer, budgets, anomalies", "Cost analysis", "/overview · /explore"],
        ["7", "The period is run, issued, sent, paid, chased", "Billing", "/statements · /collections · /billing"],
        ["8", "Money is posted, reconciled and the month is closed", "Finance", "/finance/*"],
        ["9", "Selling through resellers and agents", "Partners", "/partners"],
        ["10", "What the Sovereign can sell: pools, shapes, placements", "Capacity", "/capacity"],
        ["11", "Who may do what, and what the product sends", "Access & notifications", "/access · /notifications"],
    ], kicker="Orientation", col_widths=[0.6, 5, 2.4, 3])
    d.two_col("Two layers that never meet in billing",
              "Cloud layer — resold National Cloud", [
                  "A customer's cloud project (ECS, EVS, EIP, RDS…) collected read-only through AK/SK.",
                  "Priced by a cloud-scoped book, e.g. 'National Cloud 2026 list'.",
                  "The Sovereign's own cloud project is the 'landlord': its cost is the pool that Allocation spreads — as a report, never a bill.",
              ],
              "Platform layer — Organizations on Catalyst", [
                  "An Organization on this Sovereign, collected by the platform collector (plan-hours, k8s vCPU, memory, PVC).",
                  "Priced by a platform-scoped book: 'OpenOva plans' (committed) or 'Organization PAYG' (pay-per-use).",
                  "The per-Organization platform stack is excluded from the customer's usage and shown as Platform overhead.",
              ], kicker="Orientation",
              note="A source belongs to exactly one layer and is priced by exactly one book. That is the whole ownership model.")
    d.table("Who does what — the six roles", ["Role", "Scope", "Can"], [
        ["sovereign-admin", "Sovereign", "everything, including access and settings"],
        ["billing-operator", "Sovereign", "read, price books, customers, issue, collect, audit, capacity"],
        ["finance-viewer", "Sovereign", "read and export only, plus audit"],
        ["customer-owner", "one customer", "read its own cost, top up its account, manage its users, sources, PO and tax registration"],
        ["customer-billing", "one customer", "read and top up"],
        ["customer-viewer", "one customer", "read"],
    ], kicker="Orientation", col_widths=[2, 1.6, 7], note="A partner principal gets a third scope, partner:<id>, that expands to the customers assigned to it (module 9).")
    d.shot("The console, oriented", "01-overview.png", [
        "Left navigation: the modules in journey order — Customers, Resources, Price books, Contracts, Statements, Collections, Finance, Partners, Capacity, Access, Notifications.",
        "The Overview is the Sovereign-wide cost picture: month to date, compared with the previous window.",
        "Everything on this page is scoped by your role: a customer-owner sees only its own customer here (as /my/overview).",
    ], kicker="Orientation · /overview")

    # ------------------------------------------------------------------ 1
    d.section(1, "Leads and the public calculator", "How a prospect prices a basket before anyone has created a customer — and what becomes a lead.",
              ["What is public and what never is", "One pricing function, two callers", "Estimate → lead → customer", "The calculator, screen by screen"])
    d.bullets("What is public — and what never is", [
        "Public: one list price book the Sovereign chose to publish, the catalogue plans, and a pricing endpoint that prices a basket from them.",
        "Never public: customers, discounts, contracts, usage, statements. An estimate belongs to nobody — no customer, no session, no cookie.",
        "One pricing function: the public estimate and the billing run call the same rating code on the same book, so an estimate never quotes a price the bill could not produce.",
        "A saved estimate is valid 30 days and has a shareable link.",
    ], kicker="Leads")
    d.bullets("Estimate, lead, customer — three different things", [
        "Estimate: a priced basket. Saved with its lines, currency, region, monthly and yearly totals, and the name of the book it was priced from.",
        "Lead: an estimate on which the prospect left an email. Nothing else changes; the flag makes it appear on the Leads page.",
        "Leads page (/leads): a read-only list — received, address, per month, per year, the link. It needs customers.manage.",
        "Customer: created by the operator in Customers (module 2). The lead is context for that conversation; nothing converts by itself.",
    ], kicker="Leads")
    d.shot("The public calculator — a catalogue by product family", "08-estimate.png", [
        "Region at the top applies to every line; 'Find a service' searches by plain name.",
        "Families — Compute, Storage, Networking, Databases, Containers, Platform plans — each service with a one-line description and its 'from' price.",
        "The estimate on the right starts empty and tells the prospect what to do; 'Send me this estimate' with an address is what makes a lead.",
    ], kicker="Leads · /estimate")
    d.shot("Configuring a service — no SKU typed anywhere", "08b-estimate-configurator.png", [
        "Family (general purpose / compute-optimised / memory-optimised), number of servers, usage (always on 730 h · business hours 176 h · custom).",
        "Size is a grid of chips: vCPU · GB with the monthly price; the SKU resolves from the choice and shows only as a small hint.",
        "An attached disk and an Elastic IP per server become extra lines under the same item.",
    ], kicker="Leads · configurator")
    d.shot("The estimate — grouped, visual, editable", "08c-estimate-summary.png", [
        "Per month and 12 months in large type, tax included; the gauge and the bar show each family's share.",
        "Items grouped by family › service with their lines; Edit re-opens the configurator with its values, Remove drops the item.",
        "Every figure is priced by the server from the published list book — the same function that prices an invoice.",
    ], kicker="Leads · summary")
    d.shot("Leads — what the prospect left behind", "07-leads.png", [
        "One row per estimate that carries an address, newest first.",
        "Per month and per year are the totals the prospect saw, in the book's currency.",
        "The link reopens the estimate exactly as saved; use it in the first conversation, then create the customer.",
    ], kicker="Leads · /leads")
    d.quiz("Leads — check yourself", [
        "A prospect prices 3 servers and closes the tab without an email. Is that a lead?",
        "Can a public estimate ever include a customer's negotiated discount?",
        "Where is the price an estimate uses defined?",
    ], [
        "No. It is a saved estimate; only an estimate with an address is a lead.",
        "No. Discounts are per customer and the estimate has none; it prices from the one published list book.",
        "In the published price book (module 4), read through the same rating function billing uses.",
    ])

    # ------------------------------------------------------------------ 2
    d.section(2, "Customers and sources", "The billed party, its commercial profile, and where its usage comes from.",
              ["Customer = party", "The four commercial fields", "Sources and their price book", "Users, invites, import", "The customer page, tab by tab"])
    d.bullets("A customer is the party that is billed", [
        "One row per party: name, currency, PO reference, payment terms, tax profile, contacts.",
        "A partner is also a party and owns a customer row flagged partner (module 9) — one ledger for everyone who owes or is owed money.",
        "Everything downstream is scoped by customer: usage, cost, statements, account, budgets, users, audit.",
        "Party kinds you will meet on hw307: Gulf Retail Group, Nizwa Fintech, Dhofar Logistics, Muscat Health Systems, Omantel, Acme Walk (customers) and Walk Reseller (partner).",
    ], kicker="Customers")
    d.table("The four commercial fields decide the whole billing path", ["Field", "Values", "Meaning"], [
        ["charging", "billed · informational", "billed produces invoices; informational is showback only"],
        ["payment_model", "prepaid · postpaid", "prepaid settles an invoice from the account balance at issue and service depends on that balance; postpaid leaves it open on terms"],
        ["payment_method", "transfer · gateway · internal", "how money arrives: bank transfer against the invoice, a card gateway, or an internal recharge with no cash"],
        ["gateway", "omantel · stripe · —", "which gateway collects, when the method is gateway"],
    ], kicker="Customers", col_widths=[1.8, 2.4, 6.8])
    d.table("Profiles you will actually configure", ["Who", "charging", "payment_model", "method", "gateway", "Reads as"], [
        ["Omantel corporate, invoiced against a PO on terms", "billed", "postpaid", "transfer", "—", "real"],
        ["Omantel SME on Omantel's gateway", "billed", "prepaid", "gateway", "omantel", "real"],
        ["The same SME today, on Stripe", "billed", "prepaid", "gateway", "stripe", "real"],
        ["An internal department recharged", "billed", "postpaid", "internal", "—", "chargeback"],
        ["A department that only wants to see its cost", "informational", "—", "—", "—", "showback"],
    ], kicker="Customers", col_widths=[4, 1.3, 1.5, 1.3, 1.1, 1.3])
    d.shot("Creating a customer", "04-customer-new.png", [
        "Name, currency and the commercial profile are set here; the tax profile and PO can follow on the Settings tab.",
        "Nothing is billed until a source is attached and a period is run.",
        "Bulk onboarding: /customers/import takes a CSV with the same fields.",
    ], kicker="Customers · /customers/new")
    d.shot("The customer list", "03-customers.png", [
        "One row per party with its commercial profile and month-to-date cost.",
        "Open a customer to get its twelve tabs — the rest of this module walks them.",
    ], kicker="Customers · /customers")
    d.bullets("Sources — where a customer's usage comes from", [
        "Cloud source: a National Cloud project, read-only AK/SK, a region, an optional scope token. The cloud collector measures it.",
        "Platform source: an Organization on this Sovereign. The platform collector measures its plan-hours and Kubernetes meters.",
        "Each source is assigned ONE price book. The book decides the layer's prices; the customer's discounts and contract apply on top.",
        "Disable a source to stop collecting without deleting history. A source's credentials belong to the customer-owner to rotate.",
    ], kicker="Customers")
    d.shot("Sources tab — attach, assign a book, disable", "05-customer-04-sources.png", [
        "Add a cloud project (AK/SK, region) or link an Organization.",
        "Price book per source: this is where 'which prices apply' is decided.",
        "The last-collected stamp tells you the collector is alive for this source.",
    ], kicker="Customers · /customers/{id}?tab=sources")
    d.shot("Settings tab — the commercial profile", "05-customer-11-settings.png", [
        "charging / payment model / method / gateway — the four fields from the table.",
        "PO reference and payment terms are copied onto every draft statement at run time.",
        "Tax registration and exemption feed the tax rules (module 4).",
    ], kicker="Customers · ?tab=settings")
    d.shot("Users tab — who signs in for this customer", "05-customer-10-users.png", [
        "Invite sends an activation link (notification customer.invite); the user gets a customer-scoped role.",
        "customer-owner may manage users, sources and the PO; customer-viewer only reads.",
        "The same people see the customer lens at /my/* — the pages you see here, confined to their customer.",
    ], kicker="Customers · ?tab=users")
    d.shot("Overview tab — the customer's month", "05-customer-01-overview.png", [
        "Month to date, previous window, unpriced and not-sold-per-use notes, top services.",
        "This is the page that answered in 10 s before 0.1.52 and answers from one ledger read since 0.1.53.",
    ], kicker="Customers · ?tab=overview")
    d.shot("Audit tab — every change to this customer", "05-customer-12-audit.png", [
        "Who changed what and when: profile edits, source changes, invitations, suspensions.",
        "audit.read is a Sovereign permission; a customer does not read its own trail.",
    ], kicker="Customers · ?tab=audit")
    d.quiz("Customers — check yourself", [
        "Which field makes an invoice settle from the account at issue?",
        "A customer has a cloud project and an Organization. How many price books apply?",
        "Who may rotate a source's AK/SK: the operator, the customer-owner, or both?",
    ], [
        "payment_model = prepaid.",
        "Two — one per source: a cloud-scoped book for the project, a platform-scoped book for the Organization.",
        "Both: customers.manage at the Sovereign, customer.self.manage for the owner on its own customer.",
    ])

    # ------------------------------------------------------------------ 3
    d.section(3, "Metering", "What the collectors measure, how a record looks, and the two traps: resizes and Elastic IPs.",
              ["Two collectors", "Resources and usage records", "A resize splits at the minute", "The Elastic IP meters by its charge mode", "Platform overhead"])
    d.table("Two collectors, one record shape", ["", "Cloud collector", "Platform collector"], [
        ["Reads", "National Cloud project through AK/SK", "the Sovereign's own cluster"],
        ["Inventory", "every 15 minutes: ECS, EVS, EIP, ELB, NAT, RDS, DDS, GaussDB, CCE…", "Organizations and their pods, PVCs"],
        ["Events", "CTS change events (create, delete, resize)", "controller events"],
        ["Metrics", "CES hourly (CPU utilisation, EIP traffic)", "requests and usage per pod"],
        ["Writes", "resources (inventory + tags) and usage_records (sku, unit, quantity, window)", "the same two tables"],
        ["Meters", "ecs.*, evs.*, eip, eip.bandwidth_mbps, eip.traffic_gb, rds.*, …", "plan.* (plan-hours), k8s.vcpu, k8s.mem_gb, k8s.pvc_gb"],
    ], kicker="Metering", col_widths=[1.5, 5, 4])
    d.example("A usage record, read aloud", [
        "usage_records",
        "  source_id     1169e528-…    (Gulf Retail Group · cloud project)",
        "  resource_id   ecs-0a4f…     (server 'erp-app-01')",
        "  sku           ecs.m7n.2xlarge.8      unit  instance-hour",
        "  quantity      1.000000",
        "  window        2026-09-27 10:00 → 11:00 UTC",
        "  labels.tags   {env: prod, cost-centre: erp}",
        "",
        "One row per resource per hour per meter.",
        "The rating run multiplies quantity × the source's book price",
        "and writes the priced ledger (module 4).",
    ], explain=[
        "Quantity is time-weighted: a server that existed 30 minutes of the hour has quantity 0.5.",
        "Tags travel with the record — cost centres (module 6) and the explorer's tag grouping read them here.",
        "The SKU is the Huawei flavour name: m7n = memory-optimised, 2xlarge = 8 vCPU, .8 = 8 GB per vCPU → 64 GB.",
    ])
    d.example("A resize is billed at both sizes, split at the minute", [
        "10:00  ecs.c7n.xlarge.2  (4 vCPU · 8 GB)   running",
        "10:36  resize → ecs.c7n.2xlarge.2 (8 vCPU · 16 GB)",
        "",
        "records for the 10:00–11:00 window",
        "  ecs.c7n.xlarge.2    quantity 0.600000   (36 min)",
        "  ecs.c7n.2xlarge.2   quantity 0.400000   (24 min)",
        "",
        "a workload that never resizes emits byte-identical",
        "records to before — pinned by test.",
    ], explain=[
        "Before this fix an in-place resize was billed at whichever size the collector happened to see. Now the CTS event marks the minute.",
        "The same rule holds on the platform layer for a pod resized in place.",
    ])
    d.table("The Elastic IP meters by its charge mode", ["Address shape", "Hourly address fee", "Reservation", "Traffic"], [
        ["bandwidth-billed (a reserved pipe)", "yes", "eip.bandwidth_mbps × Mbps × hours", "—"],
        ["traffic-billed (pay per GB)", "yes", "—", "eip.traffic_gb per GB, hourly from CES"],
        ["shared bandwidth (several addresses on one pipe)", "yes", "once per pipe, not per address", "—"],
    ], kicker="Metering", col_widths=[3.5, 1.6, 3, 3],
        note="hw307's own Elastic IPs are traffic-billed: the collector stores bandwidth_charge_mode per address and never bills a reservation the cloud did not make.")
    d.bullets("Platform overhead is measured, not billed to the customer", [
        "Each Organization carries a platform stack the customer did not ask for: Keycloak, newapi, openclaw, Agenity, the vCluster control plane.",
        "The platform collector excludes those pods from the customer's usage and books them on the Platform overhead line.",
        "Allocation (module 6) shows overhead beside each customer's share, so the landlord cost is explained, not hidden.",
        "The host namespace quota is the purchased plan plus that stack — derived from the rendered charts and pinned by tests.",
    ], kicker="Metering")
    d.shot("Resources — the inventory with its cost", "09-resources.png", [
        "One row per resource across sources: kind, SKU, region, tags, month-to-date cost.",
        "Filter by customer, source, kind or tag; the sum at the top follows the filter.",
        "Open a row for the resource's own history (next slide).",
    ], kicker="Metering · /resources")
    d.shot("Resource detail — one resource, hour by hour", "10-resource-detail.png", [
        "The usage records behind the number: meter, quantity, window, price applied.",
        "A resize shows as two SKUs in the same hour; a deleted resource stops at its last window.",
    ], kicker="Metering · /resources/{source}/{resource}")
    d.quiz("Metering — check yourself", [
        "A server runs 45 minutes of an hour. What quantity does the record carry?",
        "Why does a traffic-billed Elastic IP never carry eip.bandwidth_mbps?",
        "Where does the cost of Keycloak inside a customer's Organization go?",
    ], [
        "0.75 instance-hours.",
        "Because the cloud reserved no pipe for it; it is billed per GB from CES traffic instead.",
        "To the Platform overhead line; it is excluded from the customer's usage.",
    ])

    # ------------------------------------------------------------------ 4
    d.section(4, "Price books and rating", "Where prices, plans, allowances, tiers, discounts and tax are defined — and the order they apply in.",
              ["Price books and scopes", "Where plans live", "Where allowances and tiers live", "Plan vs pay-per-use", "Discounts, currency, tax", "The waterfall"])
    d.bullets("A price book is the list price", [
        "A book has a scope (cloud or platform), a currency, an annual divisor (annual ÷ 8,760 = the hourly unit price), and items.",
        "An item is one SKU: unit, unit price, optional terms (tiers or an allowance — next slides).",
        "A book is assigned per source (module 2). Changing a source's book changes its prices from the next rating run.",
        "hw307 has four: 'National Cloud 2026 list' (cloud), 'OpenOva plans' (platform, committed), 'Organization PAYG' (platform, pay-per-use), and the partner's derived 'Walk Reseller · retail'.",
    ], kicker="Price books")
    d.shot("Price books — the list", "11-pricebooks.png", [
        "Scope badge (cloud / platform), currency, item count, and how many sources are assigned.",
        "The derived retail book is read-only: it follows a partner rule (module 9).",
        "'New book', 'Import CSV' and 'Clone' start from here.",
    ], kicker="Price books · /pricebooks")
    d.shot("Inside the cloud list book", "12-pricebook-cloud.png", [
        "One row per SKU: ecs.*, evs.*, eip, rds.*, … with unit and unit price.",
        "Coverage tells you which metered SKUs have no price yet ('unpriced') — fix them here, not in the customer.",
        "Edit settings: name, currency, annual divisor, public or not (module 1).",
    ], kicker="Price books · /pricebooks/{id}")
    d.shot("WHERE PLANS ARE DEFINED — the 'OpenOva plans' book", "13-pricebook-plans.png", [
        "A plan is a price-book item like any other: SKU plan.s / plan.m / plan.l / plan.xl, unit plan-hour, resource_kind plan.",
        "Unit price = annual ÷ 8,760 = monthly ÷ 730. An Organization on plan M is metered 730 plan-hours a month.",
        "What a plan INCLUDES is written as allowances on the plan item (next slide) — that is why there is no separate 'Plans' menu.",
    ], kicker="Price books · the plans book",
        note="The platform's own committed product is a SKU in a platform book, priced per plan-hour; 'Organization PAYG' holds the pay-per-use meters instead.")
    d.shot("WHERE ALLOWANCES AND TIERS ARE — 'Add tiers or allowance' on an item", "14-pricebook-plans-terms-dialog.png", [
        "Each item row has 'Add tiers or allowance' (or 'Edit shapes' once it has one).",
        "Allowance: N units of a SKU included per billing period, on the plan item; e.g. plan M includes 50 GB of eip.traffic_gb.",
        "Tiers: a price ladder with a mode, graduated or all_units; bands must ascend, the unbounded band last.",
        "A contract can add MORE allowance for one customer (module 5); the two add up.",
    ], kicker="Price books · item terms")
    d.table("Plan vs pay-per-use — disjoint meters, never both", ["", "OpenOva plans (committed)", "Organization PAYG (pay-per-use)"], [
        ["What is metered", "plan.s/m/l/xl plan-hours (730 a month)", "k8s.vcpu, k8s.mem_gb, k8s.pvc_gb as used"],
        ["Who is on it", "an Organization that bought a size", "an Organization on 'flexi'"],
        ["What the vCPUs cost", "nothing extra: the plan covers them (shown as 'not sold per use', not 'unpriced')", "the metered rate per vCPU-hour"],
        ["Allowances", "written on the plan item", "none by default; a contract may add one"],
        ["Pinned by", "TestPlanAndPAYGBooksAreDisjoint — the two books price disjoint SKU sets", ""],
    ], kicker="Price books", col_widths=[2, 4.5, 4.5])
    d.example("Plan arithmetic — one Organization, one month", [
        "plan.m   annual 540.000  → unit 540 / 8760 = 0.061644 per plan-hour",
        "September (720 h, the Organization existed all month)",
        "  plan.m         720 × 0.061644 =  44.383562",
        "  k8s.vcpu       covered by the plan → not sold per use (0.00)",
        "  evs.ssd.gb     200 GB × 720 h × 0.000090 = 12.960000   (metered)",
        "  eip.traffic_gb 180 GB, allowance 50 on plan.m → 130 × 0.012 = 1.560000",
        "  ---------------------------------------------------------",
        "  list                                                58.903562",
    ], explain=[
        "The plan is metered by the hour it existed, so a mid-month start bills part of a month without a proration rule.",
        "The covered meter is reported as 'not sold per use' so nobody chases an 'unpriced' vCPU.",
        "The allowance is consumed first, the rest rates at the item price.",
    ])
    d.example("Volume tiers — the mode is a decision, not a detail", [
        "ladder   0.010 up to 10,240 · 0.008 up to 102,400 · 0.006 above",
        "usage    51,200 units",
        "",
        "graduated  10,240 × 0.010 + 40,960 × 0.008 = 430.080000",
        "all_units  51,200 × 0.008                  = 409.600000",
        "",
        "difference 20.48 on one line — pinned by",
        "TestGraduatedAndAllUnitsDifferOnTheSameVolume",
    ], explain=[
        "graduated = Stripe graduated / AWS tiered storage: each band at its own price.",
        "all_units = Stripe volume / Zuora volume: the whole quantity at the band reached.",
        "The ladder is validated, never repaired: ascending bounds, unbounded band last, no negative price; a ladder that stops carries its last price upward.",
    ])
    d.example("Allowance — included quantity, per period, lapsing", [
        "plan.m includes   50 GB eip.traffic_gb   (on the plan item)",
        "contract adds    100 GB eip.traffic_gb   (module 5)",
        "included         150 GB per billing period",
        "",
        "September usage 180 GB → 150 free, 30 × 0.012 = 0.360000",
        "October   usage 120 GB → 120 free, 0.000000 (30 GB unused lapses)",
    ], explain=[
        "Allowances add up across plan and contract.",
        "Nothing carries over unless the contract item says rollover.",
        "Allowance is applied BEFORE tiers: the included quantity resets the ladder (module 5 shows why that matters).",
    ])
    d.shot("Discounts — percent or amount, with a combination rule", "17-discounts.png", [
        "A discount is scoped: all customers, one customer, one SKU or service, a validity window; kind percent or amount.",
        "The combination rule on billing settings decides what two matching discounts do: most-specific, highest, stack, or compound.",
        "A partner tier is a discount row too (tier_id set, customer NULL) — one engine (module 9).",
    ], kicker="Rating · /discounts")
    d.bullets("Currency — one reporting currency, rates with a date", [
        "Books price in their own currency; the Sovereign reports in one reporting currency (billing settings).",
        "Rates carry an effective date; the ledger stores the converted amount and the rate used, so a later rate change never rewrites history.",
        "A line that could not be converted is reported as 'unconverted' beside the totals rather than silently dropped.",
        "Budgets are set in the reporting currency; the explorer can show either.",
    ], kicker="Rating")
    d.table("Tax rules — the kind is not derivable from the rate", ["kind", "rate", "what it is", "required on the rule"], [
        ["standard", "e.g. 5 %", "the normal rate for a country, region, category, from a date", "—"],
        ["zero_rated", "0 %", "taxable at zero — a different line on the return than exempt", "the provision"],
        ["exempt", "0 %", "outside the tax; a certificate that can expire", "the article"],
        ["reverse_charge", "0 % here", "the buyer accounts for the tax", "the wording"],
        ["out_of_state", "0 %", "supply outside the jurisdiction", "—"],
    ], kicker="Rating · /tax", col_widths=[1.8, 1.2, 5, 2.2],
        note="Resolution order: customer exemption → rule for (country, region, category) valid on the date → default. The snapshot is frozen on the invoice at issue and never re-derived.")
    d.shot("Tax — rules, categories and the Oman profile", "18-tax.png", [
        "Rules by country/region/category with validity; the SKU-to-category map tells which rule a line falls under.",
        "An issued invoice keeps its own snapshot: changing a rule later changes future invoices only.",
        "The e-invoicing seam signs the issued document with the configured key.",
    ], kicker="Rating · /tax")
    d.table("The waterfall — one engine, this order, always", ["#", "Step", "What it does", "Example on 200 units"], [
        ["1", "allowance", "usage up to the included quantity rates to zero", "100 included → 100 to price"],
        ["2", "tiers", "the remainder priced by the item's bands or flat price", "bands 1.00 ≤100 / 0.10 above → 100.00"],
        ["3", "commitment", "the committed head at the committed price, the excess at list", "see module 5"],
        ["4", "discounts", "customer discounts and a partner tier under the combination rule", "−10 %"],
        ["5", "true-up", "the shortfall against the monthly minimum, as a named line", "see module 5"],
        ["6", "tax", "on the net subtotal, true-up included", "5 %"],
    ], kicker="Rating", col_widths=[0.5, 1.6, 5, 3.5],
        note="rating.ApplyTerms → rating.ApplyDiscounts → rating.TrueUp → rating.TotalsWithDiscount. TestOrderOfOperations pins each step against its plausible alternative.")
    d.quiz("Price books & rating — check yourself", [
        "Where do you change what plan L includes?",
        "A customer on 'OpenOva plans' shows k8s.vcpu as 'not sold per use'. Is something unpriced?",
        "Two discounts match one line: a 10 % for all customers and a 15 % for this customer. What applies under 'most-specific'?",
        "Same ladder, 51,200 units: which mode bills more?",
    ], [
        "On the plan.l item in the 'OpenOva plans' book → 'Add tiers or allowance'.",
        "No. The plan covers it; the meter is the allocation basis, not a missing rate.",
        "The 15 % (the customer-specific one); 'highest' would also give 15 %, 'stack' 25 %, 'compound' 23.5 %.",
        "graduated (430.08) bills more than all_units (409.60).",
    ])

    # ------------------------------------------------------------------ 5
    d.section(5, "Contracts", "Negotiated terms for one customer: allowances on top, committed use, a monthly minimum with its true-up, SLA credits and renewal.",
              ["What a contract is", "Creating one", "Committed-use and allowance lines", "Minimum commitment and true-up", "SLA credits", "Renewal and status", "What the statement shows"])
    d.bullets("What a contract is — and is not", [
        "A contract is the set of terms that changes how ONE customer's usage is rated in a period: extra allowances, committed quantities at a rate, a monthly minimum, a term with renewal.",
        "It is not a document store and not free text: the notes field is the only prose; every other field is arithmetic the rating run reads.",
        "Only an ACTIVE contract covering the first day of a period rates that period. Draft, expired and cancelled contracts rate nothing.",
        "Two active contracts on the same day: the one that started last is in force — that is a renegotiation.",
        "A statement records the contract_id it was rated under, so the invoice says which agreement produced its numbers.",
    ], kicker="Contracts")
    d.table("The contract record", ["Field", "Meaning", "Example"], [
        ["name", "how it is referred to", "Nizwa Fintech 2026 agreement"],
        ["starts_on · term_months · ends_on", "the window; the end follows the term (12 months from 2026-01-01 ends 2026-12-31) and can be overridden", "2026-01-01 · 12 · 2026-12-31"],
        ["status", "draft · active · expired · cancelled", "active"],
        ["minimum_commitment · currency", "the monthly floor on the NET total; empty = none", "1,000.00 OMR"],
        ["auto_renew · renewal_notice_days", "renew for another term, and when it appears in the renewals-due list", "yes · 60"],
        ["po_reference", "the customer's order for this agreement", "PO-4471"],
        ["items", "committed-use and allowance lines (next slide)", "3 lines"],
    ], kicker="Contracts", col_widths=[3, 5, 3])
    d.shot("Contracts — the list and the renewals due", "19-contracts.png", [
        "One row per contract with customer, status, term end and minimum.",
        "Inside the notice window a contract appears in 'Renewals due' — the list an operator acts on.",
        "'New contract' opens the form (next slide).",
    ], kicker="Contracts · /contracts")
    d.shot("Creating a contract", "20-contract-new-dialog.png", [
        "Customer, name, start, term (end derived), status, monthly minimum, renewal notice, auto-renew, PO.",
        "Leave the minimum empty for 'no floor'; a zero is not the same as empty.",
        "Save as draft first; set active when signed — only active rates.",
    ], kicker="Contracts · New contract")
    d.shot("The contract page", "21-contract-detail.png", [
        "KPIs: monthly minimum, committed lines, allowance lines, renews/ends date.",
        "'Committed use and allowances' is the table the rating engine reads; 'Add items' edits it.",
        "'Issue SLA credit' is here because the SLA is a term of this agreement.",
    ], kicker="Contracts · /contracts/{id}")
    d.table("The two item kinds", ["kind", "fields", "what the rating run does with it"], [
        ["allowance", "sku, unit, quantity, rollover", "adds quantity to whatever the plan item already includes; usage up to the total rates to zero, per period"],
        ["commitment", "sku, unit, quantity, committed_price OR discount_pct", "the committed quantity rates at the committed price (the HEAD of the volume), the excess at list; a commitment at list is refused"],
    ], kicker="Contracts", col_widths=[1.5, 3.5, 6])
    d.shot("Adding committed-use and allowance lines", "23-contract-items-dialog.png", [
        "Kind, SKU from the customer's books, quantity per period, and either a committed price or a percentage off list.",
        "Rollover on an allowance lets an unused remainder carry into the next period; default is lapse.",
    ], kicker="Contracts · Add items")
    d.example("Committed use — head of the volume, consumed per period", [
        "item  commitment  ecs.m7n.2xlarge.8  7,440 h/period  at 0.35 (list 0.50)",
        "      (= 10 servers × 744 h, 12 months, 30 % off)",
        "",
        "month with  5,000 h:  5,000 × 0.35                 = 1,750.000000",
        "month with 10,000 h:  7,440 × 0.35 + 2,560 × 0.50  = 3,884.000000",
        "same 10,000 h at list: 10,000 × 0.50               = 5,000.000000",
        "",
        "pinned by TestCommitmentUnderAndOver",
    ], explain=[
        "Under-consumption is NOT charged by the commitment line — that is what the minimum commitment is for, visibly.",
        "The commitment displaces the dearest band of a ladder, because it covers baseline usage.",
        "Each period offers the whole committed quantity again.",
    ])
    d.example("Minimum commitment — the true-up is a named line", [
        "minimum_commitment  1,000.00 per month (on the NET)",
        "",
        "September lines at list            1,100.00",
        "customer discount 10 %              −110.00   (module 4)",
        "net                                  990.00",
        "true-up   1 period                    10.00   ← added line",
        "subtotal                           1,000.00",
        "tax 5 %                               50.00",
        "total                              1,050.00",
    ], explain=[
        "The floor compares against the NET, so a discount agreed in the same contract cannot slide the bill under the minimum.",
        "The true-up is charged like any service line and taxed; it is explained on the invoice, never hidden in a rate.",
        "A month above the minimum carries no true-up line at all.",
    ])
    d.example("Order of operations — why it is pinned (TestOrderOfOperations)", [
        "ladder 1.00 up to 100 units, 0.10 above · usage 200",
        "",
        "allowance 100, then tiers:  100 free, 100 × 1.00 = 100.00  ← ours",
        "  (allowance 'holding its place' would give 100 × 0.10 = 10.00)",
        "",
        "commitment 100 at 0.50 on the HEAD: 100 × 0.50 + 100 × 0.10 = 60.00 ← ours",
        "  (on the tail: 100 × 1.00 + 100 × 0.50 = 150.00)",
        "",
        "minimum 1,000 · lines 1,100 · discount 200:",
        "  discounts first → net 900 → true-up 100 → 1,000 ← ours",
        "  (true-up first → clears on 1,100, discount → 900, under the minimum)",
    ], explain=[
        "Three places where two defensible readings differ by a factor; the console states ours and the tests refuse the other.",
    ])
    d.bullets("SLA credits — the credit note the agreement promised", [
        "POST /contracts/{id}/sla-credit: pick the statement the breach fell in, enter the percentage owed and the measured availability.",
        "The amount is that percentage of the statement's total. It is a normal credit note: the same gapless CN-<year>-<seq> number, allocated against the invoice, the remainder becoming account credit, the same ledger entry.",
        "The credit note records contract_id, sla_pct and measured_availability, so the document says what it answers.",
        "No parallel 'service credit' table — one ledger to reconcile.",
    ], kicker="Contracts")
    d.shot("Issuing an SLA credit", "24-contract-sla-dialog.png", [
        "Statement, percentage, measured availability — three fields, one credit note.",
        "Example: statement total 1,050.00, SLA owes 10 % → CN of 105.00 against that invoice.",
    ], kicker="Contracts · Issue SLA credit")
    d.bullets("Renewal, expiry, status", [
        "auto_renew: at the end date the same agreement moves forward by term_months; renewal_count increments, renewed_at is stamped.",
        "Without auto_renew the contract becomes expired and stops rating the next period.",
        "renewal_notice_days before the end the contract appears in the renewals-due list (/contracts and GET /contracts/renewals).",
        "The renew/expire decision runs inside the daily collections pass, idempotently — a restart cannot advance a term twice.",
        "Cancelled: stops rating from the next period; issued statements keep their contract_id.",
    ], kicker="Contracts")
    d.bullets("What the customer sees on the statement", [
        "Lines priced at the committed rate show the rate they were priced at; excess lines show list.",
        "A true-up line named 'true-up · 1 period' with the shortfall.",
        "An allowance shows as quantity included, so a 0.00 line is explained.",
        "The statement carries the contract_id; the customer's Contract tab (module 2) shows the agreement in force.",
    ], kicker="Contracts")
    d.shot("The customer's Contract tab", "05-customer-09-contract.png", [
        "The agreement in force for this customer, its minimum and its lines — read-only here, edited under /contracts.",
    ], kicker="Contracts · /customers/{id}?tab=contract")
    d.quiz("Contracts — check yourself", [
        "A contract is 'draft' and the period starts. Does it rate the period?",
        "Committed 7,440 h at 0.35; the customer used 6,000 h. Is the unused 1,440 h charged?",
        "Net 950 against a minimum of 1,000 with a 5 % tax. What is the total?",
        "How is an SLA credit different from a credit note?",
    ], [
        "No. Only an active contract covering the first day rates.",
        "No. Under-consumption is not charged by the commitment; only a minimum commitment would add a true-up.",
        "true-up 50 → subtotal 1,000 → tax 50 → 1,050.",
        "It is not: it IS a credit note with three extra fields (contract, SLA %, measured availability).",
    ])
