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
} as const

/** The shape every other locale is typed and checked against. */
export type EnglishCatalogue = typeof EN
