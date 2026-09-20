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
 *	build.*           components/BuildNotice.tsx + lib/useAction.ts
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
  'capacity.empty.noPoolsBody': 'A pool is a set of identical machines somebody bought: give it a name, a machine count and what one machine holds.',

  // The zone strip that carries each zone's pools, and its own Add a pool.
  'capacity.zone.poolsNone': 'no pools yet',

  'capacity.filter.region': 'Region',
  'capacity.filter.all': 'All ({count})',
  'capacity.pool.perMachine': 'each',
  'capacity.pool.binds': 'Binds first',
  'capacity.pool.bindsNone': 'nothing is sized yet',
  'capacity.pool.zoneUnknown': 'some usage here had no availability zone and landed in the default zone',
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
  'capacity.res.formula': 'sellable = held + (usable − held) × ratio, where held is the larger of what guaranteed already holds and the guaranteed floor — burstable is only ever sold out of what is left',

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
  'capacity.basket.reset': 'back to what is selling',
  'capacity.basket.perBasket': 'per mix',
  'capacity.placements.sub': 'a SKU — or a family of them — sells out of a pool at a class. The same SKU may sit on a pool once per class the pool enforces: three placements, three prices',
  'capacity.placements.none': 'Nothing is placed on this pool yet',
  'capacity.placements.noneBody': 'A placement says a SKU sells out of a pool, at a class. Until one exists its usage counts against nothing.',
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

  'capacity.form.zone': 'Zone',
  'capacity.form.zoneHelp': 'the availability zone these machines sit in',
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
  'capacity.place.sku': 'SKU',
  'capacity.place.class': 'Class',
  'capacity.place.save': 'Place',
  'capacity.place.saved': '{sku} sells out of {pool} as {class}',
  'capacity.place.removed': '{sku} is no longer placed on {pool} as {class}',
  'capacity.place.chooseSku': 'choose a SKU…',

  'capacity.legend.ok': 'below 70 %',
  'capacity.legend.warn': '70 – 85 %',
  'capacity.legend.critical': '85 % and above',
  'capacity.legend.unset': 'not sized',

  // ---- capacity: the tabs, classes per pool, the floor, what is running ----
  'capacity.tab.pools': 'Pools',
  'capacity.tab.placements': 'Placements',
  'capacity.tab.shapes': 'Shapes',
  'capacity.tab.regions': 'Regions & zones',

  'capacity.attention.reclaim': { one: '{count} pool resource holds more spot than it has room for — some has to be given back', other: '{count} pool resources hold more spot than they have room for — some has to be given back' },
  'capacity.attention.unplaced': { one: '{count} metered SKU counts against nothing: no pool takes it', other: '{count} metered SKUs count against nothing: no pool takes them' },
  'capacity.attention.mismatch': { one: '{count} SKU is running at a class it is not placed at', other: '{count} SKUs are running at a class they are not placed at' },
  'capacity.attention.unshaped': { one: '{count} metered SKU has no shape, so nothing knows what it consumes', other: '{count} metered SKUs have no shape, so nothing knows what they consume' },
  'capacity.attention.open': 'Open {tab}',

  'capacity.pools.col.pool': 'Pool',
  'capacity.pools.col.classes': 'Enforces',
  'capacity.pools.col.used': 'Sold / sellable',
  'capacity.pools.open': 'Open {name}',
  'capacity.pools.close': 'Close {name}',
  'capacity.pools.zoneUnknown': 'includes usage with no zone',
  'capacity.pools.none': 'No pools yet',
  'capacity.pools.noZones': 'A pool lives in an availability zone. Add a region and a zone first, under Regions & zones.',
  'capacity.pools.foot': 'Open a pool to see why it binds, how many more fit, and what is running on it by class.',
  'capacity.pool.editNamed': 'Edit pool {name}',
  'capacity.pool.deleteNamed': 'Delete pool {name}',

  'capacity.detail.resources': 'Capacity of {pool}',
  'capacity.detail.placements': { one: '{count} placement on this pool', other: '{count} placements on this pool' },
  'capacity.detail.managePlacements': 'Place a SKU here',

  'capacity.classes.title': 'Classes this pool enforces',
  'capacity.classes.sub': 'a placement may only use a class ticked here, so nobody can sell what the hardware underneath cannot deliver',
  'capacity.classes.inUse': 'SKUs are still placed here as {class}; remove those placements first',
  'capacity.classes.noBurstable': 'no overcommit and no guaranteed floor: both are burstable’s numbers, and this pool does not enforce burstable',

  'capacity.floor.title': 'Guaranteed floor',
  'capacity.floor.help': 'capacity burstable may never be sold into, in the resource’s own units. Spot may still run in it while it is idle — spot gives way the moment a guarantee wants the room',
  'capacity.floor.effect': '{floor} kept for guaranteed · burstable capped at {envelope}',
  'capacity.floor.free': '{amount} of it still free',
  'capacity.floor.envelope': 'burstable {room} left of {envelope}',
  'capacity.floor.none': 'none',
  'capacity.floor.noneNote': 'nothing is ring-fenced: burstable that arrives first can take every physical unit',
  'capacity.res.formulaPlain': 'this pool does not enforce burstable, so nothing is overcommitted: sellable is usable, at 1:1',

  'capacity.form.vector': 'What one machine holds',
  'capacity.form.chooseResource': 'choose a resource…',
  'capacity.form.ownResource': 'a resource kind of my own…',
  'capacity.form.ownResourceKey': 'Resource key',

  'capacity.sku.mode': 'Place',
  'capacity.sku.modeOne': 'One SKU',
  'capacity.sku.modeFamily': 'A family',
  'capacity.sku.modeFamilyHelp': 'every SKU under a prefix, e.g. ecs.m7n.* — a flavour nobody has listed yet is taken too',
  'capacity.sku.noFamilies': 'the known SKUs form no family',
  'capacity.sku.family': 'Family',
  'capacity.sku.familyHelp': 'an exact placement wins over a family, and the longer family over the shorter',
  'capacity.sku.chooseFamily': 'choose a family…',
  'capacity.sku.familyCount': { one: '{count} known SKU', other: '{count} known SKUs' },
  'capacity.sku.loading': 'loading the SKUs…',
  'capacity.sku.groupMetered': 'Metered now',
  'capacity.sku.groupShaped': 'Not metered — has a shape',
  'capacity.sku.groupUnshaped': 'Not metered — no shape yet',
  'capacity.sku.notPriced': 'in no price book',
  'capacity.sku.noShape': 'no shape yet — add one under Shapes',

  'capacity.place.heading': 'Place a SKU on a pool',
  'capacity.place.choosePool': 'choose a pool…',
  'capacity.place.classHelp': 'the classes {pool} enforces',
  'capacity.place.classPickPool': 'choose the pool first: it decides which classes exist',
  'capacity.place.alreadyPlaced': 'already placed',
  'capacity.place.noBurstable': '{pool} does not enforce burstable, so burstable is not offered. Tick it on the pool if its hardware really does throttle at runtime.',
  'capacity.place.noPools': 'There is no pool to place a SKU on yet. Add one under Pools.',
  'capacity.placements.noneAnywhere': 'Nothing is placed yet',
  'capacity.placements.removeNamed': 'Remove {sku} as {class} from {pool}',
  'capacity.placements.resources': { one: '{count} resource', other: '{count} resources' },
  'capacity.placements.familyTook': 'taking {skus}',
  'capacity.placements.familyIdle': 'a family — nothing under it is metered here now',
  'capacity.placements.familyShape': 'each member has its own',
  'capacity.unplaced.place': 'Place it',

  'capacity.mismatch.title': 'Running at a class the SKU is not placed at',
  'capacity.mismatch.sub': 'these still count — nothing is dropped — but at a different class from the one they say',
  'capacity.mismatch.row': 'says {asked}, which this SKU is not placed at here, so it counts as {counted}. Place the SKU as {asked}, or correct the resource.',

  'capacity.basket.mix': 'The mix',
  'capacity.basket.yourMix': 'your mix',
  'capacity.basket.defaultClass': 'as it is placed',
  'capacity.basket.units': 'Units',
  'capacity.basket.unitsInvalid': 'units must be a number above zero',
  'capacity.basket.lineClass': 'Class of this line',
  'capacity.basket.addLine': 'Add to the mix',
  'capacity.basket.removeLine': 'Remove {sku} from the mix',
  'capacity.basket.editOn': 'Build a mix for {pool}',
  'capacity.basket.costs': 'What one mix costs {pool}',

  'capacity.running.title': 'Running on this pool',
  'capacity.running.sub': 'one SKU may be placed at several classes, so each resource says which it was sold at: your choice here, else its lifecycle tag, else the most cautious class its SKU is placed at',
  'capacity.running.col.resource': 'Resource',
  'capacity.running.col.consumes': 'Holds',
  'capacity.running.col.soldAs': 'Sold as',
  'capacity.running.via': 'through {family}',
  'capacity.running.sourceOverride': 'set here',
  'capacity.running.sourceTag': 'from its lifecycle tag',
  'capacity.running.sourceDefault': 'nothing says — counted at the most cautious class',
  'capacity.running.asked': 'it says {class}, which is not placed here',
  'capacity.running.auto': 'follow its tag',
  'capacity.running.oneClass': 'its SKU is placed at one class only, so there is nothing to choose',
  'capacity.running.setClass': 'Class {resource} was sold as',
  'capacity.running.classSet': '{resource} now counts as {class}',
  'capacity.running.classCleared': '{resource} follows its tag again',
  'capacity.running.shared': 'also sells out of {pools}',
  'capacity.running.none': 'Nothing is running on this pool',
  'capacity.running.noneBody': 'No metered resource in the latest hour arrives here through a placement.',

  'capacity.reclaim.col': 'Reclaim',
  'capacity.reclaim.mark': 'give back',
  'capacity.reclaim.needed': '{label}: spot holds {amount} {unit} more than there is room for, and has to give it back.',
  'capacity.reclaim.covered': { one: 'The newest {count} spot resource covers it ({covered} {unit}) and is marked below.', other: 'The newest {count} spot resources cover it ({covered} {unit}) and are marked below.' },
  'capacity.reclaim.short': 'Every spot resource here together frees only {covered} {unit}; the rest has to come from somewhere else.',
  'capacity.reclaim.who': 'This product says how much and proposes which, newest first; the platform does the deleting.',

  'capacity.regions.zonesOf': 'Zones of {code}',
  'capacity.regions.addPoolIn': 'Add a pool in {code}',
  'capacity.regions.deleteRegionNamed': 'Delete region {code}',
  'capacity.regions.deleteZoneNamed': 'Delete zone {code}',

  'capacity.shapes.save': 'Save shape',
  'capacity.shapes.editNamed': 'Edit the shape of {sku}',
  'capacity.shapes.perUnitOf': '{resource} per unit',
  'capacity.unshaped.addNamed': 'Add a shape for {sku}',
  'capacity.unshaped.col.resources': 'Resources',

  // ---- notifications (pages/Notifications.tsx, DESIGN.md §21) ----------
  // The Subject column shows what a PERSON receives. Where the delivery log
  // holds a real send, it is that line and the date it went; otherwise it is
  // the template rendered over the catalogue's example payload, and it says
  // so — an example must never be read as a message somebody was sent.
  'notifications.subjectExample': 'Example',
  'notifications.subjectExampleNote': 'an example — nothing has been sent yet',
  'notifications.subjectLastSent': 'last sent {when}',
  'notifications.subjectRendersAs': 'A recipient reads:',
  // What a MANDATORY notice refuses, said where the control is rather than by
  // disabling the way in. Mandatory means it cannot be switched off and keeps
  // the channel that carries it — it has never meant it cannot be configured.
  'notifications.alwaysSent': 'A mandatory notice is always sent. Its channels and its language are still yours to set.',
  'notifications.channelRequired': 'required — a mandatory notice keeps this channel',

  // ---- build identity --------------------------------------------------
  // What a tab left open across a deploy is told. It names BOTH builds: the
  // operator who reads it is the one who deployed, and the pair is what makes
  // the message checkable rather than a generic "something changed".
  'build.stale.title': 'This page is out of date.',
  'build.stale.body': 'It is running build {page} and the server is now running build {server}. Anything you save from here may be refused until you reload — you can finish what you are typing first.',
  'build.stale.reload': 'Reload the page',
  'build.writeFailed': 'That did not save: this page is running build {page} and the server is now running build {server}, so what it sent is no longer what the server accepts. Reload the page and make the change again. The server said: {error}',
} as const

/** The shape every other locale is typed and checked against. */
export type EnglishCatalogue = typeof EN
