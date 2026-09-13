/**
 * The ENGLISH catalogue — every word the converted console surfaces show.
 *
 * Each entry renders, character for character, the string the component had
 * inline before this catalogue existed. That is not a nicety: the console has
 * 71 test files pinning rendered text, and a refactor that quietly reworded a
 * column header or dropped a full stop would be a defect nobody would notice
 * until an operator did. Nothing here is a rewording.
 *
 * KEY NAMING — `<surface>.<thing>`, dot-separated, camelCase inside a segment:
 *
 *	common.*          atoms more than one surface shows (Cancel, Email, —)
 *	nav.*             the sidebar, one key per destination and group
 *	ui.*              the shared components in components/ui.tsx
 *	table.*           components/DataTable.tsx
 *	signin.*          pages/SignIn.tsx
 *	activate.*        pages/Activate.tsx
 *	customerImport.*  pages/CustomerImport.tsx
 *	collections.*     pages/Collections.tsx
 *	capacity.*        pages/Capacity.tsx
 *
 * A key is STABLE: it is what a translation is written against, and renaming
 * one silently drops that translation back to English. Add keys; do not
 * rename them.
 *
 * TWO ENTRY SHAPES. A plain string, interpolating `{name}`; or a plural entry
 * — one string per CLDR category, chosen by a numeric `count`. English needs
 * `one` and `other`; a locale that needs more supplies them in its own file
 * with its own selector. Copy that already said "invoice(s)" is left saying
 * "invoice(s)": making it count properly would change what a reader sees, and
 * this conversion changes nothing a reader sees.
 *
 * A SECOND LOCALE IS THIS FILE AGAIN, under another tag — `ar.ts` exporting
 * `locale` and `entries`. Nothing else changes: not this file, not the lookup,
 * not a component, not a test. No Arabic is written here; none has been
 * provided, and inventing one would be worse than having none.
 */
export const EN = {
  // ---- common ----------------------------------------------------------
  /** The product's name, in the sidebar and over the sign-in card. */
  'common.product': 'Chargeback',
  /** Stands in for a value the document did not carry. */
  'common.none': '—',
  'common.cancel': 'Cancel',
  'common.confirm': 'Confirm',
  'common.continue': 'Continue',
  'common.email': 'Email',
  'common.region': 'Region',
  'common.status': 'Status',

  // ---- nav: the sidebar (layout/Shell.tsx) ------------------------------
  'nav.group.analyse': 'Analyse',
  'nav.group.plan': 'Plan',
  'nav.group.bill': 'Bill',
  'nav.group.finance': 'Finance',
  'nav.group.configure': 'Configure',
  'nav.overview': 'Overview',
  'nav.explore': 'Cost explorer',
  'nav.resources': 'Resources',
  'nav.anomalies': 'Anomalies',
  'nav.recommendations': 'Recommendations',
  'nav.capacity': 'Capacity',
  'nav.statements': 'Statements',
  'nav.collections': 'Collections',
  'nav.budgets': 'Budgets',
  'nav.reports': 'Reports',
  'nav.journal': 'Journal',
  'nav.reconciliation': 'Reconciliation',
  'nav.periods': 'Period close',
  'nav.accountMapping': 'Account mapping',
  'nav.customers': 'Customers',
  'nav.leads': 'Leads',
  'nav.partners': 'Partners',
  'nav.contracts': 'Contracts',
  'nav.pricebooks': 'Price books',
  'nav.discounts': 'Discounts',
  'nav.allocation': 'Allocation',
  'nav.billing': 'Billing',
  'nav.tax': 'Tax',
  'nav.notifications': 'Notifications',
  'nav.access': 'Access',
  'nav.costCentres': 'Cost centres',
  'nav.account': 'Account',
  'nav.paymentMethods': 'Payment methods',
  'nav.sources': 'Cost sources',
  'nav.users': 'Users',
  'nav.myCustomers': 'My customers',
  'nav.margin': 'Margin',
  'nav.retail': 'Retail prices',

  // ---- the shell (layout/Shell.tsx) -------------------------------------
  'shell.loading': 'Loading…',
  'shell.signOut': 'Sign out',
  /** The banner a non-default profile shows across the top of every page. */
  'shell.profileBanner': 'profile: {profile}',

  // ---- shared components (components/ui.tsx) ----------------------------
  'ui.delta.title': 'change versus the previous period of the same length',

  // ---- the table (components/DataTable.tsx) -----------------------------
  'table.empty': 'Nothing to show',
  /**
   * The row count under every table. `n` is the count already formatted for
   * the reader's locale ("1,204"); `count` is the raw number, which is what
   * chooses the form.
   */
  'table.rows': { one: '{n} row', other: '{n} rows' },
  'table.downloadCsv': 'Download CSV',

  // ---- sign-in (pages/SignIn.tsx) ---------------------------------------
  'signin.sendPin': 'Send sign-in PIN',
  'signin.pinSent': 'A PIN was sent to {email}.',
  'signin.pin': 'PIN',
  'signin.submit': 'Sign in',
  'signin.another': 'Use another address',

  // ---- invite activation (pages/Activate.tsx) ---------------------------
  'activate.step.pin': 'PIN',
  'activate.step.projects': 'Projects and key',
  'activate.step.verify': 'Verify',
  'activate.step.done': 'Done',
  'activate.title': 'Activate',
  'activate.invalid': 'This invite link is not valid: {error}',
  'activate.loading': 'Loading invite…',
  'activate.heading': 'Activate {customer}',
  'activate.confirmAddress': 'Confirm the address this invite was sent to.',
  'activate.sendPin': 'Send PIN',
  'activate.pinSentTo': 'PIN sent to {email}',
  'activate.keyHelp': 'Add a read-only access key for the cloud projects to be metered. Each project is verified with one signed call before anything is collected.',
  'activate.regionPlaceholder': 'om-east-1',
  'activate.projectIds': 'Project ids (one per line)',
  'activate.accessKey': 'Access key (AK)',
  'activate.secretKey': 'Secret key (SK) — write-only',
  'activate.submit': 'Verify and activate',
  /** Kept as "project(s)": counting it properly would change what is shown. */
  'activate.verifying': 'Verifying {count} project(s)…',
  'activate.active': '{customer} is active.',
  'activate.col.project': 'Project',
  'activate.col.detail': 'Detail',
  'activate.openUsage': 'Open my usage',

  // ---- bulk import (pages/CustomerImport.tsx) ---------------------------
  'customerImport.title': 'Import customers',
  'customerImport.downloadSample': 'Download sample CSV',
  'customerImport.columns': 'Columns:',
  'customerImport.columnsSeparator': '— several project ids are separated by',
  'customerImport.columnsTail': '. Existing slugs are updated; new slugs are created pending.',
  'customerImport.paste': '…or paste CSV here',
  'customerImport.preview': 'Preview',
  /** Kept as "row(s)": counting it properly would change what is shown. */
  'customerImport.import': 'Import {count} valid row(s)',
  /** Kept as "line(s)" for the same reason. */
  'customerImport.skipped': '{count} line(s) will be skipped:',
  'customerImport.lineError': 'line {line}: {message}',
  'customerImport.lineNo': 'line {line}',
  'customerImport.col.line': 'Line',
  'customerImport.col.slug': 'Slug',
  'customerImport.col.name': 'Name',
  'customerImport.col.adminEmail': 'Admin email',
  'customerImport.col.projects': 'Projects',
  'customerImport.col.priceBook': 'Price book',
  'customerImport.col.billing': 'Billing',
  'customerImport.col.start': 'Start',
  'customerImport.created': 'Created {created}, updated {updated}',
  /** Kept as "error(s)" for the same reason. */
  'customerImport.errorCount': ', {count} error(s)',
  'customerImport.back': 'Back to customers',

  // ---- collections (pages/Collections.tsx) ------------------------------
  'collections.title': 'Collections',
  'collections.subFallback': 'the aging report',
  'collections.agingAsOf': 'aging as of {day}',
  'collections.openInvoices': { one: '{count} open invoice', other: '{count} open invoices' },
  'collections.acrossCustomers': { one: 'across {count} customer', other: 'across {count} customers' },
  'collections.remindersAt': 'reminders {schedule}',
  'collections.escalateAfter': 'escalate {days} days after, {action}',
  'collections.actionSuspend': 'suspend',
  'collections.actionNotify': 'notify',
  'collections.reminderSchedule': 'Reminder schedule',
  'collections.runNow': 'Run collections now',
  'collections.ranLabel': 'collections run',
  'collections.suspendedLabel': '{customer} suspended at the platform',
  'collections.resumedLabel': '{customer} resumed at the platform',
  'collections.didNotRun': 'Collections did not run.',
  'collections.didNotRunBecause': 'Collections did not run — {reason}.',
  'collections.evaluated': { one: 'Evaluated {count} open invoice', other: 'Evaluated {count} open invoices' },
  'collections.reminders': { one: '{count} reminder', other: '{count} reminders' },
  'collections.escalations': { one: '{count} escalation', other: '{count} escalations' },
  'collections.suspendedCount': '{count} suspended',
  'collections.resumedCount': '{count} resumed',
  'collections.mailsSent': { one: '{count} mail sent', other: '{count} mails sent' },
  'collections.runErrors': { one: '{count} error', other: '{count} errors' },
  'collections.externalNotice': "This Sovereign invoices through the operator's billing system: collections are theirs. The report still reads from our invoice copy, and suspend / resume here execute an explicit command.",
  'collections.col.customer': 'Customer',
  'collections.col.totalOwed': 'Total owed',
  'collections.col.overdue': 'Overdue',
  'collections.col.oldestDue': 'Oldest due',
  'collections.col.creditAvailable': 'Credit available',
  'collections.nothingOverdue': 'none',
  'collections.hideInvoices': 'Hide invoices',
  'collections.showInvoices': 'Invoices',
  'collections.resume': 'Resume',
  'collections.suspend': 'Suspend',
  'collections.kpi.totalOwed': 'Total owed',
  'collections.kpi.overdue': 'Overdue',
  'collections.kpi.customersOverdue': 'Customers overdue',
  'collections.kpi.suspended': 'Suspended',
  'collections.nothingOwed': 'nothing owed',
  'collections.shareOfOwed': '{pct} of what is owed',
  'collections.ofWithOpenInvoice': 'of {count} with an open invoice',
  'collections.everyoneWithinTerms': 'everyone is within terms',
  'collections.heldAtPlatform': 'held at the platform by this product',
  'collections.tableLabel': 'Aging',
  'collections.emptyTitle': 'Nothing is owed',
  'collections.emptyBody': 'Every issued invoice is settled. The report fills as invoices are sent and fall due.',
  'collections.footNote': 'buckets are days past the due date; current is not yet due',
  'collections.invoicesOf': 'Open invoices of {customer}',
  'collections.col.invoice': 'Invoice',
  'collections.col.due': 'Due',
  'collections.col.bucket': 'Bucket',
  'collections.col.outstanding': 'Outstanding',
  'collections.runTitle': 'Run collections now?',
  'collections.runConfirm': 'Run collections',
  'collections.runBody': 'One evaluator pass as of today: every open invoice is checked against the reminder schedule and the escalation rule. Reminders are emailed, escalations executed, and a settled customer this product suspended is resumed.',
  'collections.runBodyWithSchedule': 'One evaluator pass as of today: every open invoice is checked against the reminder schedule ({schedule}) and the escalation rule. Reminders are emailed, escalations executed, and a settled customer this product suspended is resumed.',
  'collections.runNote': 'The same pass runs daily; this only brings it forward. A reminder already sent for a step is not sent again.',
  'collections.suspendTitle': 'Suspend {customer} at the platform?',
  'collections.resumeTitle': 'Resume {customer} at the platform?',
  'collections.suspendBody': 'The Organization is suspended at the platform — its Applications stop — until you resume it. {amount} is overdue, oldest {oldest}.',
  'collections.resumeHolds': 'Lifts the suspension this product holds',
  'collections.resumeSince': 'since {when}',
  'collections.resumeBy': '(by {source})',
  'collections.resumeStillOverdue': '{amount} is still overdue.',
  'collections.resumeNothingOverdue': 'Nothing is overdue.',
  'collections.reason': 'Reason',
  'collections.reasonHelp': 'Kept on the suspension record and shown to the customer.',
  'collections.reasonSuspendPlaceholder': 'invoice 60 days overdue, no response',
  'collections.reasonResumePlaceholder': 'payment received',

  // ---- capacity: pages/Capacity.tsx (DESIGN.md §11) ---------------------
  // A pool is a named set of identical machines; what it can still sell is a
  // VECTOR, and the resource that runs out first is what an operator buys
  // against. Every string this page shows is a key, so the page can be read
  // in another language the day a catalogue for it exists.
  'capacity.title': 'Capacity',
  'capacity.sub': 'Pools of machines per availability zone · what each can still sell, by class · when it runs out and when to order',
  'capacity.asOf': 'as of {when}',
  'capacity.readOnly': 'Read-only: pools, shapes and placements are edited with capacity.manage (sovereign-admin, billing-operator).',

  'capacity.kpi.pools': 'Pools',
  'capacity.kpi.poolsNote': '{sized} of {total} sized',
  'capacity.kpi.pressure': 'Pools past 70 %',
  'capacity.kpi.pressureNote': '{critical} critical',
  'capacity.kpi.order': 'Orders owed',
  'capacity.kpi.orderNote': '{late} already past the order-by date',
  'capacity.kpi.orderNoneNote': 'no pool is growing towards a wall yet',
  'capacity.kpi.measured': 'Measured at',
  'capacity.kpi.sources': '{count} cloud sources',
  'capacity.kpi.sourcesOne': '1 cloud source',
  'capacity.kpi.lagging': '{count} lagging',
  'capacity.kpi.noUsage': 'no cloud usage metered yet',

  'capacity.empty.title': 'No regions yet',
  'capacity.empty.body': 'Add the cloud region as the ledger names it (the region of your cloud sources, e.g. me-east-215), its availability zones, then a POOL for each set of identical machines: how many, and what one machine holds. Consumption is never typed — the latest metered hour is read through the SKU shapes onto the pools the placements name.',
  'capacity.empty.noPools': 'No pools in this zone',
  'capacity.empty.noPoolsBody': 'A pool is a set of identical machines somebody bought: give it a name, a machine count and what one machine holds.',

  'capacity.filter.region': 'Region',
  'capacity.filter.all': 'All ({count})',

  'capacity.pool.machines': '{count} machines',
  'capacity.pool.machinesOne': '1 machine',
  'capacity.pool.perMachine': 'each',
  'capacity.pool.binds': 'Binds first',
  'capacity.pool.bindsNone': 'nothing is sized yet',
  'capacity.pool.leadTime': '{days} days to procure',
  'capacity.pool.zoneUnknown': 'some usage here had no availability zone and landed in the default zone',
  'capacity.pool.orderBy': 'Order by {date}',
  'capacity.pool.orderLate': 'Order was due {when}',
  'capacity.pool.orderNone': 'Not growing — no order owed',
  'capacity.pool.edit': 'Edit pool',
  'capacity.pool.delete': 'Delete',
  'capacity.pool.add': 'Add a pool',

  'capacity.res.usable': 'Usable',
  'capacity.res.sellable': 'Sellable',
  'capacity.res.sold': 'Sold',
  'capacity.res.left': 'Left',
  'capacity.res.ratio': 'Ratio',
  'capacity.res.reserve': 'Reserve',
  'capacity.res.raw': 'Raw',
  'capacity.res.unsized': 'not sized — enter the machines and what one holds',
  'capacity.res.stranded': '{amount} {unit} stranded: free hardware that cannot be sold because {binding} ran out',
  'capacity.res.over': 'over its envelope by {amount} {unit}',
  'capacity.res.formula': 'sellable = guaranteed + (usable − guaranteed) × ratio — a guaranteed unit consumes physical capacity, so only what physically remains is multiplied',

  'capacity.class.guaranteed': 'Guaranteed',
  'capacity.class.burstable': 'Burstable',
  'capacity.class.spot': 'Spot',
  'capacity.class.split': 'By class',
  'capacity.class.spotReclaim': '{amount} {unit} of spot must be freed',
  'capacity.class.spotReclaimNote': 'this product says how much; the platform decides which instances',

  'capacity.trend.title': 'Trend',
  'capacity.trend.sub': 'consumption by class over the complete days before the measured hour, and the two walls it is heading for',
  'capacity.trend.growth': '{amount} {unit} a day',
  'capacity.trend.flat': 'not growing',
  'capacity.trend.unknown': 'growth unknown: fewer than 3 complete days',
  'capacity.trend.softWall': 'Soft wall',
  'capacity.trend.softWallNote': 'the pool reaches what it can sell: spot starts being reclaimed and burstable starts throttling',
  'capacity.trend.hardWall': 'Hard wall',
  'capacity.trend.hardWallNote': 'guaranteed alone reaches usable capacity: buy hardware, no policy avoids it',
  'capacity.trend.orderBy': 'Order by',
  'capacity.trend.orderByNote': 'the nearer wall minus the lead time — an alert on the wall itself fires too late by exactly the time it takes to procure',
  'capacity.trend.noWall': 'no wall at the present growth',

  'capacity.basket.title': 'How many more fit',
  'capacity.basket.sub': 'a mix, not a per-SKU maximum: “50 large fit” and “200 small fit” side by side are mutually exclusive',
  'capacity.basket.current': 'the mix currently selling',
  'capacity.basket.answer': '{count} more of this mix',
  'capacity.basket.binds': 'limited by {resource}',
  'capacity.basket.none': 'Not measured',
  'capacity.basket.unshaped': 'no shape for {skus} — say what one unit consumes before pricing it into a mix',
  'capacity.basket.edit': 'Change the mix',
  'capacity.basket.apply': 'Recalculate',
  'capacity.basket.reset': 'Back to what is selling',
  'capacity.basket.help': 'sku:units, comma separated — e.g. ecs.m7n.2xlarge.8:2,evs.ssd.gb:100',
  'capacity.basket.perBasket': 'per mix',

  'capacity.placements.title': 'What sells out of this pool',
  'capacity.placements.sub': 'the class lives on the placement: the same shape sold guaranteed and sold spot is two SKUs at two prices',
  'capacity.placements.none': 'Nothing is placed on this pool yet',
  'capacity.placements.noneBody': 'A placement says a SKU sells out of this pool, at a class. Until one exists its usage counts against nothing.',
  'capacity.placements.col.sku': 'SKU',
  'capacity.placements.col.class': 'Class',
  'capacity.placements.col.shape': 'Shape / unit',
  'capacity.placements.col.units': 'Running',
  'capacity.placements.remove': 'remove',

  'capacity.unplaced.title': 'Metered here, counted against nothing',
  'capacity.unplaced.noPlacement': 'no pool in this zone takes it',
  'capacity.unplaced.resourceUnplaced': 'placed, but no pool it is placed on holds {resource}',

  'capacity.unshaped.title': 'SKUs with no shape',
  'capacity.unshaped.sub': 'metered in the latest hour, but nothing says how much of each resource one unit consumes',
  'capacity.unshaped.add': 'Add a shape',

  'capacity.unmapped.region': 'Metered usage in {region} — {skus} SKUs, {resources} resources — {why}',
  'capacity.unmapped.noRegion': 'has no region here, so it counts against nothing.',
  'capacity.unmapped.noZones': 'has a region here but no zone to land in.',
  'capacity.unmapped.addRegion': 'Add region {region}',

  'capacity.form.poolName': 'Pool name',
  'capacity.form.poolNameHelp': 'what this set of machines is called, e.g. m7n-a',
  'capacity.form.machines': 'Machines',
  'capacity.form.machinesHelp': 'how many identical machines; two more servers is a change to this one field',
  'capacity.form.leadTime': 'Lead time (days)',
  'capacity.form.leadTimeHelp': 'how long procurement takes — it is what turns a wall into an order-by date',
  'capacity.form.note': 'Note',
  'capacity.form.noteHelp': 'where the number comes from',
  'capacity.form.resource': 'Resource',
  'capacity.form.perMachine': 'Per machine',
  'capacity.form.reserve': 'Reserve',
  'capacity.form.reserveHelp': 'held back for redundancy and maintenance',
  'capacity.form.ratio': 'Overcommit',
  'capacity.form.ratioHelp': 'per resource: vCPU may run 4:1 while the RAM in the same chassis runs 1:1',
  'capacity.form.addResource': 'Add a resource',
  'capacity.form.removeResource': 'remove',
  'capacity.form.nPlusOne': 'N+1',
  'capacity.form.nPlusOneHelp': 'set the reserve to one machine’s worth',
  'capacity.form.save': 'Save pool',
  'capacity.form.saved': 'Pool {name} saved',
  'capacity.form.created': 'Pool {name} added',
  'capacity.form.deleted': 'Pool {name} deleted',
  'capacity.form.deleteTitle': 'Delete pool {name}?',
  'capacity.form.deleteBody': 'Its resource vector, the placements on it and its size history go with it. The usage those SKUs meter will count against nothing until they are placed somewhere else.',

  'capacity.shapes.title': 'SKU shapes',
  'capacity.shapes.sub': 'how much of each resource one unit of a SKU consumes · the National Cloud list SKUs are seeded, an ECS flavour is read from its name',
  'capacity.shapes.unseeded': 'no per-unit shape on the list: {skus}',
  'capacity.shapes.col.sku': 'SKU',
  'capacity.shapes.col.shape': 'Per unit',
  'capacity.shapes.col.source': 'Source',
  'capacity.shapes.derived': 'derived from the name',
  'capacity.shapes.add': 'Add a shape',
  'capacity.shapes.edit': 'Edit',
  'capacity.shapes.removeHint': 'a blank or 0 removes the resource; all blank removes the shape',
  'capacity.shapes.saved': 'Shape of {sku} saved',
  'capacity.shapes.removed': 'Shape of {sku} removed',
  'capacity.shapes.none': 'No shapes',
  'capacity.shapes.noneBody': 'Without shapes nothing counts against the pools.',

  'capacity.regions.title': 'Regions and zones',
  'capacity.regions.sub': 'a region is the code the ledger carries; usage whose zone is unknown lands in the default zone',
  'capacity.regions.addRegion': 'Add region',
  'capacity.regions.addZone': 'Add zone',
  'capacity.regions.regionCode': 'Region code',
  'capacity.regions.regionCodeHelp': 'as the ledger names it, e.g. me-east-215',
  'capacity.regions.zoneCode': 'Zone code',
  'capacity.regions.name': 'Name',
  'capacity.regions.defaultZone': 'default zone',
  'capacity.regions.zonesNone': 'no zones — add one, or its usage stays unmapped',
  'capacity.regions.deleteRegionTitle': 'Delete region {code}?',
  'capacity.regions.deleteRegionBody': 'Its zones, every pool with its history and every placement go with it. Metered usage in {code} will read as unmapped.',
  'capacity.regions.deleteZoneTitle': 'Delete zone {code}?',
  'capacity.regions.deleteZoneBody': 'Its pools, their history and their placements go with it.',
  'capacity.regions.deleteZoneDefault': 'It is the default zone: the oldest remaining zone takes over unknown-zone usage.',
  'capacity.regions.regionAdded': 'Region {code} added — now add its zones',
  'capacity.regions.zoneAdded': 'Zone {code} added to {region}',
  'capacity.regions.regionDeleted': 'Region {code} deleted',
  'capacity.regions.zoneDeleted': 'Zone {code} deleted',

  'capacity.place.title': 'Place a SKU on {pool}',
  'capacity.place.sku': 'SKU',
  'capacity.place.class': 'Class',
  'capacity.place.save': 'Place',
  'capacity.place.saved': '{sku} sells out of {pool} as {class}',
  'capacity.place.removed': '{sku} is no longer placed on {pool}',
  'capacity.place.chooseSku': 'choose a SKU…',

  'capacity.legend.ok': 'below 70 %',
  'capacity.legend.warn': '70 – 85 %',
  'capacity.legend.critical': '85 % and above',
  'capacity.legend.unset': 'not sized',
} as const

/** The shape every other locale is typed and checked against. */
export type EnglishCatalogue = typeof EN
