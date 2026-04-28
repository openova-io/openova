package handlers

import (
	"context"
	"log/slog"
	"time"

	"github.com/openova-io/openova/core/services/catalog/store"
)

// SeedIfEmpty checks whether the apps collection is empty and, if so,
// populates the database with default catalog data.
// It also ensures all expected add-ons exist (upserts missing ones).
func (h *Handler) SeedIfEmpty(ctx context.Context) {
	apps, err := h.Store.ListApps(ctx)
	if err != nil {
		slog.Error("seed: failed to check apps", "error", err)
		return
	}

	if len(apps) > 0 {
		slog.Info("seed: catalog already populated, checking migrations")
		h.migrateAppsTo27(ctx)
		h.dedupBySlug(ctx)
		h.seedMissingAddOns(ctx)
		h.migratePlans(ctx)
		h.seedSystemApps(ctx)
		h.migrateAppDependencies(ctx)
		h.migrateAppDeployable(ctx)
		return
	}

	slog.Info("seed: catalog is empty, seeding default data")
	h.seedAllData(ctx)
}

// seedAllData inserts the complete catalog: apps, industries, plans, addons, bundles.
func (h *Handler) seedAllData(ctx context.Context) {
	now := time.Now().UTC()

	// -----------------------------------------------------------------------
	// Apps — exact 27 from marketplace-apps.ts
	// -----------------------------------------------------------------------
	seedApps := []store.App{
		{Slug: "wordpress", Name: "WordPress", Tagline: "The world's most popular content management system", Description: "Build anything from a simple blog to a full e-commerce site with WooCommerce. Thousands of plugins and themes, WYSIWYG editor, and a massive ecosystem.", Category: "cms", Tags: []string{"blog", "ecommerce", "website", "woocommerce"}, Icon: "W", IconBg: "#21759B", MinimumSize: "s", RecommendedSize: "m", Website: "https://wordpress.org", License: "GPLv2", Featured: true, Popular: true, Free: true, Features: []string{"Full content management with WYSIWYG editor", "Thousands of plugins and themes", "WooCommerce for e-commerce", "Multi-user with role-based access", "REST API for headless usage", "SEO tools and analytics integration"}, RelatedApps: []string{"stalwart-mail", "nextcloud", "umami"}, RamMB: 256, CpuMilli: 250, DiskGB: 5, HelmChart: "wordpress", HelmRepo: "https://charts.bitnami.com/bitnami", CreatedAt: now, UpdatedAt: now},
		{Slug: "ghost", Name: "Ghost", Tagline: "Modern publishing with built-in memberships and newsletters", Description: "Independent technology for modern publishing. Built-in membership and subscription management, native email newsletters, and a clean writing experience.", Category: "cms", Tags: []string{"blog", "publishing", "newsletter", "membership"}, Icon: "G", IconBg: "#15171A", MinimumSize: "xs", RecommendedSize: "s", Website: "https://ghost.org", License: "MIT", Featured: true, Popular: true, Free: true, Features: []string{"Clean, distraction-free writing editor", "Built-in membership and subscription system", "Native email newsletters", "Theme marketplace", "SEO and social sharing built in", "Content API for headless usage"}, RelatedApps: []string{"stalwart-mail", "umami", "listmonk"}, RamMB: 256, CpuMilli: 250, DiskGB: 5, HelmChart: "ghost", HelmRepo: "https://charts.bitnami.com/bitnami", CreatedAt: now, UpdatedAt: now},
		{Slug: "stalwart-mail", Name: "Stalwart Mail", Tagline: "All-in-one mail server with IMAP, JMAP, SMTP, CalDAV, and CardDAV", Description: "Modern, high-performance mail server written in Rust. Supports every protocol you need: IMAP, JMAP, SMTP, CalDAV, CardDAV, and WebDAV. Built-in spam filter and web admin.", Category: "email", Tags: []string{"email", "smtp", "imap", "calendar", "contacts"}, Icon: "\u2709", IconBg: "#4F46E5", MinimumSize: "m", RecommendedSize: "m", Website: "https://stalw.art", License: "AGPL-3.0", Featured: true, Popular: true, Free: true, Features: []string{"All protocols: IMAP, JMAP, SMTP, CalDAV, CardDAV, WebDAV", "Built-in spam filter (sieve scripting)", "Web-based admin panel", "Single Rust binary, low resource usage", "Full-text search", "DKIM, SPF, DMARC support"}, RelatedApps: []string{"wordpress", "rocket-chat", "listmonk"}, RamMB: 400, CpuMilli: 250, DiskGB: 10, HelmChart: "stalwart", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
		{Slug: "rocket-chat", Name: "Rocket.Chat", Tagline: "Open-source team chat with omnichannel customer support", Description: "The most feature-rich open-source chat platform. Team messaging, video/audio calls, omnichannel customer support (LiveChat), and a marketplace of integrations.", Category: "communication", Tags: []string{"chat", "messaging", "video", "livechat", "support"}, Icon: "\U0001F680", IconBg: "#F5455C", MinimumSize: "s", RecommendedSize: "m", Website: "https://rocket.chat", License: "MIT", Featured: false, Popular: true, Free: true, Features: []string{"Team channels, DMs, and threads", "Video and audio conferencing", "Omnichannel LiveChat widget for customer support", "Integration marketplace", "Mobile apps for iOS and Android", "End-to-end encryption"}, RelatedApps: []string{"stalwart-mail", "jitsi-meet", "cal-com"}, RamMB: 512, CpuMilli: 500, DiskGB: 5, HelmChart: "rocketchat", HelmRepo: "https://rocketchat.github.io/helm-charts", CreatedAt: now, UpdatedAt: now},
		{Slug: "nextcloud", Name: "Nextcloud", Tagline: "Self-hosted file sync, collaboration, and office suite", Description: "A complete productivity platform: file sync, real-time document editing, calendar, contacts, video calls, and hundreds of apps. Replace Google Workspace and Dropbox.", Category: "productivity", Tags: []string{"files", "office", "calendar", "collaboration"}, Icon: "\u2601", IconBg: "#0082C9", MinimumSize: "s", RecommendedSize: "m", Website: "https://nextcloud.com", License: "AGPL-3.0", Featured: true, Popular: true, Free: true, Features: []string{"File sync across all devices", "Collaborative document editing (OnlyOffice/Collabora)", "Calendar and contacts (CalDAV/CardDAV)", "Video calls and chat (Talk)", "Hundreds of apps in the app store", "Desktop and mobile sync clients"}, RelatedApps: []string{"stalwart-mail", "wordpress", "vaultwarden"}, RamMB: 512, CpuMilli: 500, DiskGB: 20, HelmChart: "nextcloud", HelmRepo: "https://nextcloud.github.io/helm/", CreatedAt: now, UpdatedAt: now},
		{Slug: "twenty", Name: "Twenty", Tagline: "Modern open-source CRM built for the way you work", Description: "A beautiful, modern alternative to Salesforce. Custom objects, Kanban views, email integration, and a clean TypeScript/React codebase. Built by the community.", Category: "crm", Tags: []string{"crm", "sales", "contacts", "pipeline"}, Icon: "XX", IconBg: "#141414", MinimumSize: "s", RecommendedSize: "m", Website: "https://twenty.com", License: "AGPL-3.0", Featured: true, Popular: true, Free: true, Features: []string{"Custom objects and fields", "Kanban and table views", "Email integration", "Activity timeline", "API-first architecture", "Import/export from other CRMs"}, RelatedApps: []string{"stalwart-mail", "cal-com", "invoiceshelf"}, RamMB: 512, CpuMilli: 500, DiskGB: 5, HelmChart: "twenty", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
		{Slug: "umami", Name: "Umami", Tagline: "Privacy-first web analytics without cookies", Description: "A simple, fast, privacy-focused alternative to Google Analytics. No cookies, fully GDPR compliant. Track website visitors, page views, and events.", Category: "analytics", Tags: []string{"analytics", "privacy", "gdpr", "tracking"}, Icon: "U", IconBg: "#000000", MinimumSize: "xs", RecommendedSize: "s", Website: "https://umami.is", License: "MIT", Featured: false, Popular: true, Free: true, Features: []string{"No cookies required, GDPR compliant", "Real-time visitor dashboard", "Custom event tracking", "Multiple website support", "API for data export", "Lightweight tracking script (<1KB)"}, RelatedApps: []string{"wordpress", "ghost", "medusa"}, RamMB: 256, CpuMilli: 250, DiskGB: 5, HelmChart: "umami", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
		{Slug: "medusa", Name: "Medusa", Tagline: "Open-source headless commerce platform", Description: "The most flexible open-source e-commerce platform. Headless architecture, multi-region, multi-currency, and a rich plugin ecosystem.", Category: "ecommerce", Tags: []string{"ecommerce", "shop", "payments", "headless"}, Icon: "M", IconBg: "#7C3AED", MinimumSize: "s", RecommendedSize: "m", Website: "https://medusajs.com", License: "MIT", Featured: false, Popular: false, Free: true, Features: []string{"Headless API-first architecture", "Multi-region and multi-currency", "Payment provider plugins (Stripe, PayPal)", "Inventory and order management", "Admin dashboard", "Custom storefront support"}, RelatedApps: []string{"wordpress", "umami", "stalwart-mail"}, RamMB: 512, CpuMilli: 500, DiskGB: 5, HelmChart: "medusa", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
		{Slug: "plane", Name: "Plane", Tagline: "Open-source project management for modern teams", Description: "A beautiful alternative to Jira, Linear, and Monday. Issues, sprints, cycles, modules, and docs. Modern UI with powerful project tracking.", Category: "project-management", Tags: []string{"project", "issues", "kanban", "sprints", "agile"}, Icon: "P", IconBg: "#3F76FF", MinimumSize: "s", RecommendedSize: "m", Website: "https://plane.so", License: "AGPL-3.0", Featured: true, Popular: true, Free: true, Features: []string{"Issues with multiple views (Board, List, Gantt)", "Sprints and cycles", "Modules for project grouping", "Built-in docs/pages", "GitHub and Slack integration", "Custom workflows and labels"}, RelatedApps: []string{"gitea", "rocket-chat", "cal-com"}, RamMB: 512, CpuMilli: 500, DiskGB: 5, HelmChart: "plane", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
		{Slug: "erpnext", Name: "ERPNext", Tagline: "Full-featured open-source ERP for any business", Description: "100% open-source enterprise resource planning. Accounting, HR, manufacturing, CRM, inventory, and project management. No per-user licensing fees.", Category: "erp", Tags: []string{"erp", "accounting", "hr", "inventory", "manufacturing"}, Icon: "E", IconBg: "#0089FF", MinimumSize: "m", RecommendedSize: "l", Website: "https://erpnext.com", License: "GPL-3.0", Featured: true, Popular: false, Free: true, Features: []string{"Full double-entry accounting", "HR and payroll management", "Inventory and warehouse management", "Manufacturing and BOM", "CRM and sales pipeline", "Project management and timesheets"}, RelatedApps: []string{"twenty", "invoiceshelf", "nocodb"}, RamMB: 1024, CpuMilli: 500, DiskGB: 10, HelmChart: "erpnext", HelmRepo: "https://helm.erpnext.com", CreatedAt: now, UpdatedAt: now},
		{Slug: "invoiceshelf", Name: "InvoiceShelf", Tagline: "Simple, beautiful invoicing for freelancers and small businesses", Description: "Create professional invoices, track expenses, and manage payments. True open-source invoicing solution with a clean, modern interface.", Category: "invoicing", Tags: []string{"invoicing", "billing", "expenses", "payments"}, Icon: "$", IconBg: "#5851DB", MinimumSize: "xs", RecommendedSize: "s", Website: "https://invoiceshelf.com", License: "AGPL-3.0", Featured: false, Popular: false, Free: true, Features: []string{"Professional invoice generation", "Recurring invoices", "Expense tracking", "Payment tracking", "Tax management", "Multi-currency support"}, RelatedApps: []string{"erpnext", "twenty", "nocodb"}, RamMB: 256, CpuMilli: 250, DiskGB: 2, HelmChart: "invoiceshelf", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
		{Slug: "listmonk", Name: "Listmonk", Tagline: "High-performance newsletter and mailing list manager", Description: "Self-hosted newsletter and mailing list manager. Single Go binary, modern dashboard, powerful templating. Replace Mailchimp at a fraction of the cost.", Category: "marketing", Tags: []string{"newsletter", "email", "marketing", "mailing-list"}, Icon: "L", IconBg: "#7C3AED", MinimumSize: "xs", RecommendedSize: "s", Website: "https://listmonk.app", License: "AGPL-3.0", Featured: false, Popular: false, Free: true, Features: []string{"High-performance bulk email sending", "Advanced email templating", "Subscriber management and segmentation", "Campaign analytics and tracking", "Media management", "REST API for automation"}, RelatedApps: []string{"stalwart-mail", "wordpress", "ghost"}, RamMB: 256, CpuMilli: 200, DiskGB: 2, HelmChart: "listmonk", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
		{Slug: "cal-com", Name: "Cal.com", Tagline: "Scheduling infrastructure for everyone", Description: "The open-source Calendly alternative. Booking pages, round-robin scheduling, team availability, and calendar integrations.", Category: "scheduling", Tags: []string{"scheduling", "booking", "calendar", "appointments"}, Icon: "\U0001F4C5", IconBg: "#292929", MinimumSize: "s", RecommendedSize: "s", Website: "https://cal.com", License: "AGPLv3", Featured: false, Popular: true, Free: true, Features: []string{"Individual and team booking pages", "Round-robin and collective scheduling", "Google Calendar and Outlook integration", "Custom availability rules", "Webhook and Zapier integration", "Embeddable booking widget"}, RelatedApps: []string{"stalwart-mail", "rocket-chat", "jitsi-meet"}, RamMB: 256, CpuMilli: 250, DiskGB: 2, HelmChart: "calcom", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
		{Slug: "gitea", Name: "Gitea", Tagline: "Lightweight self-hosted Git with CI/CD and packages", Description: "A painless, self-hosted Git service. Code hosting, pull requests, issues, CI/CD (Actions), and package registry. Written in Go, extremely lightweight.", Category: "devtools", Tags: []string{"git", "ci-cd", "code", "devops"}, Icon: "\U0001F375", IconBg: "#609926", MinimumSize: "xs", RecommendedSize: "s", Website: "https://gitea.com", License: "MIT", Featured: false, Popular: false, Free: true, Features: []string{"Git repository hosting", "Pull requests with code review", "Issue tracking", "CI/CD via Gitea Actions (GitHub Actions compatible)", "Package registry (npm, Docker, Maven, etc.)", "Organization and team management"}, RelatedApps: []string{"plane", "rocket-chat", "uptime-kuma"}, RamMB: 256, CpuMilli: 250, DiskGB: 10, HelmChart: "gitea", HelmRepo: "https://dl.gitea.io/charts/", CreatedAt: now, UpdatedAt: now},
		{Slug: "uptime-kuma", Name: "Uptime Kuma", Tagline: "Beautiful self-hosted monitoring with status pages", Description: "A fancy self-hosted monitoring tool. HTTP, TCP, DNS, and ping monitors with 90+ notification integrations and beautiful public status pages.", Category: "monitoring", Tags: []string{"monitoring", "uptime", "status-page", "alerts"}, Icon: "\U0001F4CA", IconBg: "#5CDD8B", MinimumSize: "xs", RecommendedSize: "xs", Website: "https://uptime.kuma.pet", License: "MIT", Featured: false, Popular: true, Free: true, Features: []string{"HTTP, TCP, DNS, ping, and gRPC monitors", "Beautiful public status pages", "90+ notification integrations (Slack, Discord, Telegram)", "Multi-language dashboard", "Certificate expiry monitoring", "Maintenance windows"}, RelatedApps: []string{"gitea", "rocket-chat", "wordpress"}, RamMB: 128, CpuMilli: 100, DiskGB: 1, HelmChart: "uptime-kuma", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
		{Slug: "librechat", Name: "LibreChat", Tagline: "Multi-provider AI chat with agents and code interpreter", Description: "Enhanced ChatGPT clone supporting multiple AI providers (OpenAI, Anthropic, Google, local models). Multi-user auth, agents, MCP, code interpreter, and DALL-E.", Category: "ai", Tags: []string{"ai", "chat", "llm", "agents", "mcp"}, Icon: "\U0001F916", IconBg: "#6366F1", MinimumSize: "s", RecommendedSize: "m", Website: "https://librechat.ai", License: "MIT", Featured: true, Popular: true, Free: true, Features: []string{"Multiple AI provider support (OpenAI, Anthropic, Google)", "Multi-user authentication", "AI agents with tool use", "Model Context Protocol (MCP) support", "Code interpreter", "File upload and DALL-E image generation"}, RelatedApps: []string{"dify", "openclaw"}, RamMB: 512, CpuMilli: 500, DiskGB: 5, HelmChart: "librechat", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
		{Slug: "documenso", Name: "Documenso", Tagline: "Open-source document signing, the DocuSign alternative", Description: "Create, send, and sign documents digitally. Beautiful interface, API-first design, and full audit trail. Replace DocuSign with your own infrastructure.", Category: "documents", Tags: []string{"signing", "documents", "contracts", "legal"}, Icon: "\u270D", IconBg: "#A2E771", MinimumSize: "xs", RecommendedSize: "s", Website: "https://documenso.com", License: "AGPL-3.0", Featured: false, Popular: false, Free: true, Features: []string{"Digital document signing", "Document templates", "Multi-signer workflows", "Complete audit trail", "REST API for integration", "Email notifications"}, RelatedApps: []string{"stalwart-mail", "twenty", "bookstack"}, RamMB: 256, CpuMilli: 250, DiskGB: 2, HelmChart: "documenso", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
		{Slug: "vaultwarden", Name: "Vaultwarden", Tagline: "Lightweight Bitwarden-compatible password manager", Description: "An unofficial Bitwarden server written in Rust. Full compatibility with all Bitwarden clients (browser, desktop, mobile). Team password sharing, 2FA, and secure notes.", Category: "security", Tags: []string{"passwords", "security", "2fa", "secrets"}, Icon: "\U0001F512", IconBg: "#175DDC", MinimumSize: "xs", RecommendedSize: "xs", Website: "https://github.com/dani-garcia/vaultwarden", License: "AGPL-3.0", Featured: false, Popular: true, Free: true, Features: []string{"Full Bitwarden client compatibility", "Team and organization password sharing", "Two-factor authentication (TOTP, WebAuthn)", "Secure notes and file attachments", "Password generator", "Emergency access"}, RelatedApps: []string{"nextcloud", "stalwart-mail", "rocket-chat"}, RamMB: 64, CpuMilli: 50, DiskGB: 1, HelmChart: "vaultwarden", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
		{Slug: "bookstack", Name: "BookStack", Tagline: "Simple, self-hosted wiki with a book/chapter/page structure", Description: "A platform for organizing and storing information. Content is structured as Books, Chapters, and Pages for intuitive navigation. WYSIWYG and Markdown editors.", Category: "knowledge-base", Tags: []string{"wiki", "docs", "knowledge", "documentation"}, Icon: "\U0001F4DA", IconBg: "#0288D1", MinimumSize: "xs", RecommendedSize: "s", Website: "https://bookstackapp.com", License: "MIT", Featured: false, Popular: false, Free: true, Features: []string{"Book/Chapter/Page content structure", "WYSIWYG and Markdown editors", "Full-text search", "Role-based access control", "Diagram drawing (diagrams.net)", "API for content management"}, RelatedApps: []string{"nextcloud", "rocket-chat", "plane"}, RamMB: 256, CpuMilli: 250, DiskGB: 2, HelmChart: "bookstack", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
		{Slug: "formbricks", Name: "Formbricks", Tagline: "Open-source survey and experience management platform", Description: "The open-source alternative to Qualtrics and Typeform. In-app surveys, website popups, link surveys, and advanced targeting.", Category: "forms", Tags: []string{"forms", "surveys", "feedback", "nps"}, Icon: "\U0001F4DD", IconBg: "#00C4B8", MinimumSize: "xs", RecommendedSize: "s", Website: "https://formbricks.com", License: "AGPL-3.0", Featured: false, Popular: false, Free: true, Features: []string{"In-app surveys and website popups", "Link surveys for external distribution", "Advanced targeting and segmentation", "Pre-built survey templates (NPS, CSAT, CES)", "Response analytics and dashboards", "Webhook and Zapier integration"}, RelatedApps: []string{"wordpress", "umami", "twenty"}, RamMB: 256, CpuMilli: 250, DiskGB: 2, HelmChart: "formbricks", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
		{Slug: "dify", Name: "Dify", Tagline: "Build AI agents and workflows with a visual editor", Description: "Production-ready LLMOps platform. Visual workflow builder, RAG pipelines, prompt management, multi-model support, and observability.", Category: "ai", Tags: []string{"ai", "agents", "rag", "llm", "workflows"}, Icon: "\U0001F9E0", IconBg: "#1570EF", MinimumSize: "m", RecommendedSize: "l", Website: "https://dify.ai", License: "Custom (Apache-based)", Featured: true, Popular: true, Free: true, Features: []string{"Visual AI workflow builder", "RAG pipeline with document ingestion", "Multi-model support (OpenAI, Anthropic, Ollama)", "Prompt management and versioning", "API publishing for built workflows", "Observability and usage analytics"}, RelatedApps: []string{"librechat", "openclaw"}, RamMB: 1024, CpuMilli: 1000, DiskGB: 10, HelmChart: "dify", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
		{Slug: "openclaw", Name: "OpenClaw", Tagline: "Personal AI assistant across all your messaging apps", Description: "Connect your AI assistant to WhatsApp, Telegram, Slack, Discord, Teams, Signal, and 20+ channels. 5,400+ skills, persistent memory, and voice support.", Category: "ai", Tags: []string{"ai", "assistant", "chatbot", "whatsapp", "telegram", "slack"}, Icon: "\U0001F980", IconBg: "#FF6B35", MinimumSize: "xs", RecommendedSize: "s", Website: "https://openclaw.ai", License: "MIT", Featured: true, Popular: true, Free: true, Features: []string{"24+ messaging platform integrations", "5,400+ skills in the ClawHub registry", "Persistent memory and knowledge base", "Multi-agent support", "Voice chat on mobile", "Local model support via Ollama"}, RelatedApps: []string{"dify", "librechat"}, RamMB: 256, CpuMilli: 250, DiskGB: 2, HelmChart: "openclaw", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
		{Slug: "chatwoot", Name: "Chatwoot", Tagline: "Omnichannel customer support platform", Description: "Open-source alternative to Intercom and Zendesk. Live chat widget, email, WhatsApp, Facebook, Telegram, and SMS in one inbox.", Category: "support", Tags: []string{"support", "helpdesk", "livechat", "ticketing"}, Icon: "\U0001F4AC", IconBg: "#1F93FF", MinimumSize: "s", RecommendedSize: "m", Website: "https://chatwoot.com", License: "MIT", Featured: true, Popular: true, Free: true, Features: []string{"Omnichannel inbox (chat, email, WhatsApp, FB, Telegram)", "Embeddable live chat widget", "Shared team inbox with assignments", "Canned responses and automation rules", "Customer satisfaction surveys (CSAT)", "Knowledge base / help center"}, RelatedApps: []string{"stalwart-mail", "rocket-chat", "twenty"}, RamMB: 512, CpuMilli: 500, DiskGB: 5, HelmChart: "chatwoot", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
		{Slug: "postiz", Name: "Postiz", Tagline: "AI-powered social media scheduler and manager", Description: "Schedule and manage posts across 14+ social media platforms. AI-powered content suggestions, analytics, and team collaboration.", Category: "social-media", Tags: []string{"social", "marketing", "scheduling", "content"}, Icon: "\U0001F4F1", IconBg: "#000000", MinimumSize: "xs", RecommendedSize: "s", Website: "https://postiz.com", License: "AGPLv3", Featured: false, Popular: true, Free: true, Features: []string{"Schedule posts across 14+ platforms", "AI-powered content suggestions", "Visual content calendar", "Team collaboration and approval workflows", "Post performance analytics", "Bulk scheduling and CSV import"}, RelatedApps: []string{"umami", "wordpress", "ghost"}, RamMB: 256, CpuMilli: 250, DiskGB: 2, HelmChart: "postiz", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
		{Slug: "nocodb", Name: "NocoDB", Tagline: "Open-source Airtable alternative on any database", Description: "Turn any SQL database into a smart spreadsheet. Grid, Kanban, Gallery, and Form views. API generation, automations, and collaboration.", Category: "database", Tags: []string{"database", "spreadsheet", "nocode", "airtable"}, Icon: "\U0001F5C3", IconBg: "#1348FC", MinimumSize: "xs", RecommendedSize: "s", Website: "https://nocodb.com", License: "AGPLv3", Featured: true, Popular: true, Free: true, Features: []string{"Spreadsheet UI on any SQL database", "Grid, Kanban, Gallery, and Form views", "Automatic REST API generation", "Automations and webhooks", "Role-based access control", "Import from Airtable, CSV, Excel"}, RelatedApps: []string{"erpnext", "twenty", "formbricks"}, RamMB: 256, CpuMilli: 250, DiskGB: 5, HelmChart: "nocodb", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
		{Slug: "jitsi-meet", Name: "Jitsi Meet", Tagline: "Self-hosted video conferencing, no account required", Description: "Secure, fully-featured video conferencing that runs in your browser. No downloads, no accounts required for participants.", Category: "video-conferencing", Tags: []string{"video", "conferencing", "meetings", "webrtc"}, Icon: "\U0001F4F9", IconBg: "#17A0DB", MinimumSize: "s", RecommendedSize: "m", Website: "https://jitsi.org", License: "Apache-2.0", Featured: false, Popular: true, Free: true, Features: []string{"Browser-based, no download required", "No account needed for participants", "Screen sharing and recording", "Breakout rooms", "End-to-end encryption", "Calendar integration"}, RelatedApps: []string{"rocket-chat", "cal-com", "stalwart-mail"}, RamMB: 1024, CpuMilli: 1000, DiskGB: 5, HelmChart: "jitsi-meet", HelmRepo: "https://jitsi-contrib.github.io/jitsi-helm/", CreatedAt: now, UpdatedAt: now},
		{Slug: "immich", Name: "Immich", Tagline: "Self-hosted Google Photos with ML-powered search", Description: "High-performance photo and video management. Automatic backup from mobile, ML-powered search and face recognition, shared albums, and a beautiful timeline view.", Category: "photo-management", Tags: []string{"photos", "videos", "backup", "gallery", "ml"}, Icon: "\U0001F4F7", IconBg: "#4250AF", MinimumSize: "s", RecommendedSize: "m", Website: "https://immich.app", License: "AGPL-3.0", Featured: false, Popular: true, Free: true, Features: []string{"Automatic backup from iOS and Android", "ML-powered search (CLIP)", "Face recognition and person grouping", "Shared albums and partner sharing", "Timeline and map views", "RAW photo support"}, RelatedApps: []string{"nextcloud", "vaultwarden"}, RamMB: 1024, CpuMilli: 1000, DiskGB: 50, HelmChart: "immich", HelmRepo: "", CreatedAt: now, UpdatedAt: now},
	}

	for i := range seedApps {
		if err := h.Store.CreateApp(ctx, &seedApps[i]); err != nil {
			slog.Error("seed: failed to create app", "slug", seedApps[i].Slug, "error", err)
		}
	}
	slog.Info("seed: created apps", "count", len(seedApps))

	// -----------------------------------------------------------------------
	// Industries
	// -----------------------------------------------------------------------
	seedIndustries := []store.Industry{
		{Slug: "restaurant", Name: "Restaurant", Emoji: "\U0001F37D", Description: "Restaurants, cafes, and food service businesses", DisplayOrder: 1, SuggestedApps: []string{"wordpress", "cal-com", "chatwoot", "invoiceshelf", "umami"}, BundleID: "starter-pack"},
		{Slug: "retail", Name: "Retail", Emoji: "\U0001F6D2", Description: "Shops, e-commerce, and retail businesses", DisplayOrder: 2, SuggestedApps: []string{"erpnext", "medusa", "chatwoot", "invoiceshelf", "umami"}, BundleID: "business-suite"},
		{Slug: "legal", Name: "Legal", Emoji: "\u2696\uFE0F", Description: "Law firms and legal services", DisplayOrder: 3, SuggestedApps: []string{"nextcloud", "bookstack", "vaultwarden", "cal-com", "stalwart-mail"}, BundleID: "business-suite"},
		{Slug: "healthcare", Name: "Healthcare", Emoji: "\U0001FA7A", Description: "Clinics, hospitals, and healthcare providers", DisplayOrder: 4, SuggestedApps: []string{"nextcloud", "cal-com", "rocket-chat", "jitsi-meet", "vaultwarden"}, BundleID: "comms-hub"},
		{Slug: "education", Name: "Education", Emoji: "\U0001F393", Description: "Schools, universities, and training centers", DisplayOrder: 5, SuggestedApps: []string{"nextcloud", "bookstack", "jitsi-meet", "plane", "rocket-chat"}, BundleID: "comms-hub"},
		{Slug: "finance", Name: "Finance", Emoji: "\U0001F4B0", Description: "Banks, insurance, and financial services", DisplayOrder: 6, SuggestedApps: []string{"erpnext", "vaultwarden", "nocodb", "umami", "stalwart-mail"}, BundleID: "business-suite"},
		{Slug: "real-estate", Name: "Real Estate", Emoji: "\U0001F3E0", Description: "Property management and real estate agencies", DisplayOrder: 7, SuggestedApps: []string{"twenty", "wordpress", "cal-com", "chatwoot", "invoiceshelf"}, BundleID: "starter-pack"},
		{Slug: "technology", Name: "Technology", Emoji: "\U0001F4BB", Description: "Software companies and tech startups", DisplayOrder: 8, SuggestedApps: []string{"gitea", "uptime-kuma", "plane", "librechat", "dify"}, BundleID: "business-suite"},
		{Slug: "manufacturing", Name: "Manufacturing", Emoji: "\U0001F3ED", Description: "Factories, production, and supply chain", DisplayOrder: 9, SuggestedApps: []string{"erpnext", "nocodb", "nextcloud", "uptime-kuma", "bookstack"}, BundleID: "business-suite"},
		{Slug: "creative-agency", Name: "Creative Agency", Emoji: "\U0001F3A8", Description: "Design studios, marketing agencies, and creative firms", DisplayOrder: 10, SuggestedApps: []string{"nextcloud", "rocket-chat", "wordpress", "immich", "cal-com"}, BundleID: "comms-hub"},
	}

	for i := range seedIndustries {
		if err := h.Store.CreateIndustry(ctx, &seedIndustries[i]); err != nil {
			slog.Error("seed: failed to create industry", "slug", seedIndustries[i].Slug, "error", err)
		}
	}
	slog.Info("seed: created industries", "count", len(seedIndustries))

	// -----------------------------------------------------------------------
	// Plans
	// -----------------------------------------------------------------------
	seedPlans := []store.Plan{
		{Slug: "s", Name: "S", Description: "For personal projects and small teams", CPU: "2 vCPU", Memory: "4 GB", Storage: "25 GB", PriceOMR: 5, Popular: false, SortOrder: 1,
			Features: []string{"Unlimited apps", "SSO included", "API access", "TLS certificates", "Daily snapshots"}},
		{Slug: "m", Name: "M", Description: "For growing businesses up to 30 users", CPU: "4 vCPU", Memory: "8 GB", Storage: "50 GB", PriceOMR: 9, Popular: true, SortOrder: 2,
			Features: []string{"Unlimited apps", "SSO included", "API access", "TLS certificates", "Daily backups", "Priority support", "Custom domain"}},
		{Slug: "l", Name: "L", Description: "For teams with 30\u2013100 users", CPU: "8 vCPU", Memory: "16 GB", Storage: "100 GB", PriceOMR: 16, Popular: false, SortOrder: 3,
			Features: []string{"Unlimited apps", "SSO included", "API access", "TLS certificates", "Hourly backups", "Priority support", "Custom domain", "WAF/IPS", "Dedicated support"}},
		{Slug: "xl", Name: "XL", Description: "For enterprises with 100+ users", CPU: "16 vCPU", Memory: "32 GB", Storage: "200 GB", PriceOMR: 30, Popular: false, SortOrder: 4,
			Features: []string{"Unlimited apps", "SSO included", "API access", "TLS certificates", "Continuous backups", "Priority support", "Custom domain", "WAF/IPS", "Dedicated support", "SLA 99.9%", "Audit logs"}},
		{Slug: "flexi", Name: "Flexi", Description: "Pay as you go \u2014 scale resources on demand", CPU: "On demand", Memory: "On demand", Storage: "On demand", PriceOMR: 0, Popular: false, SortOrder: 5,
			Features: []string{"Unlimited apps", "SSO included", "API access", "TLS certificates", "Pay per use", "Scale on demand"}},
	}

	for i := range seedPlans {
		if err := h.Store.CreatePlan(ctx, &seedPlans[i]); err != nil {
			slog.Error("seed: failed to create plan", "slug", seedPlans[i].Slug, "error", err)
		}
	}
	slog.Info("seed: created plans", "count", len(seedPlans))

	// -----------------------------------------------------------------------
	// Add-Ons
	// -----------------------------------------------------------------------
	seedAddOns := expectedAddOns()

	for i := range seedAddOns {
		if err := h.Store.CreateAddOn(ctx, &seedAddOns[i]); err != nil {
			slog.Error("seed: failed to create addon", "slug", seedAddOns[i].Slug, "error", err)
		}
	}
	slog.Info("seed: created addons", "count", len(seedAddOns))

	// -----------------------------------------------------------------------
	// Bundles
	// -----------------------------------------------------------------------
	seedBundles := []store.Bundle{
		{Slug: "starter-pack", Name: "Starter Pack", Tagline: "Everything you need to get online", Apps: []string{"wordpress", "stalwart-mail", "chatwoot", "cal-com", "umami"}, Discount: 10, RecommendedSize: "s"},
		{Slug: "comms-hub", Name: "Comms Hub", Tagline: "Unified team communication", Apps: []string{"rocket-chat", "jitsi-meet", "stalwart-mail", "cal-com", "nextcloud"}, Discount: 15, RecommendedSize: "m"},
		{Slug: "business-suite", Name: "Business Suite", Tagline: "Complete business operations stack", Apps: []string{"erpnext", "twenty", "nextcloud", "invoiceshelf", "plane", "nocodb", "bookstack"}, Discount: 20, RecommendedSize: "l"},
	}

	for i := range seedBundles {
		if err := h.Store.CreateBundle(ctx, &seedBundles[i]); err != nil {
			slog.Error("seed: failed to create bundle", "slug", seedBundles[i].Slug, "error", err)
		}
	}
	slog.Info("seed: created bundles", "count", len(seedBundles))

	slog.Info("seed: catalog seeding complete")
}

// expectedAddOns returns the canonical set of add-ons.
func expectedAddOns() []store.AddOn {
	return []store.AddOn{
		{Slug: "daily-backup", Name: "Daily Backup", Description: "Automated daily backups with 30-day retention", PriceOMR: 3, Included: false, Category: "reliability"},
		{Slug: "priority-support", Name: "Priority Support", Description: "Get help fast when it matters \u2014 4h response SLA", PriceOMR: 5, Included: false, Category: "support"},
		{Slug: "custom-domain", Name: "Custom Domain", Description: "Bring your own domain \u2014 free DNS configuration with automatic TLS", PriceOMR: 0, Included: true, Category: "networking"},
		{Slug: "api-access", Name: "API Access", Description: "Full REST API for integration, automation, and custom workflows", PriceOMR: 5, Included: false, Category: "developer"},
		{Slug: "dedicated-ip", Name: "Dedicated IP", Description: "Dedicated IPv4 address with reverse DNS (PTR) registration", PriceOMR: 5, Included: false, Category: "networking"},
		{Slug: "waf", Name: "Web Application Firewall", Description: "Block attacks before they reach your apps \u2014 OWASP Core Rule Set", PriceOMR: 0, Included: true, Category: "security"},
		{Slug: "ips", Name: "Intrusion Prevention", Description: "Community-powered threat intelligence \u2014 CrowdSec", PriceOMR: 0, Included: true, Category: "security"},
		{Slug: "vuln-scan", Name: "Vulnerability Scanning", Description: "Find vulnerabilities before attackers do \u2014 Trivy", PriceOMR: 0, Included: true, Category: "security"},
		{Slug: "log-management", Name: "Log Management", Description: "Search and analyze all your app logs \u2014 Grafana Loki", PriceOMR: 3, Included: false, Category: "monitoring"},
	}
}

// migrateAppsTo27 checks if the catalog has the old set of apps (with slugs like
// "stalwart" instead of "stalwart-mail") and replaces them with the correct 27.
func (h *Handler) migrateAppsTo27(ctx context.Context) {
	// Check if we already have the correct slugs.
	app, err := h.Store.GetApp(ctx, "stalwart-mail")
	if err != nil {
		slog.Error("seed: failed to check for stalwart-mail", "error", err)
		return
	}
	if app != nil {
		slog.Info("seed: apps already migrated to v2 slugs")
		return
	}

	slog.Info("seed: migrating apps to v2 (27 apps with correct slugs)")

	// Delete all existing apps.
	existing, err := h.Store.ListApps(ctx)
	if err != nil {
		slog.Error("seed: failed to list apps for migration", "error", err)
		return
	}
	for _, a := range existing {
		if err := h.Store.DeleteApp(ctx, a.ID); err != nil {
			slog.Error("seed: failed to delete old app", "slug", a.Slug, "error", err)
		}
	}

	// Delete all existing industries (they reference old slugs).
	existingInd, err := h.Store.ListIndustries(ctx)
	if err != nil {
		slog.Error("seed: failed to list industries for migration", "error", err)
		return
	}
	for _, ind := range existingInd {
		if err := h.Store.DeleteIndustry(ctx, ind.ID); err != nil {
			slog.Error("seed: failed to delete old industry", "slug", ind.Slug, "error", err)
		}
	}

	// Delete all existing bundles (they reference old slugs).
	existingBun, err := h.Store.ListBundles(ctx)
	if err != nil {
		slog.Error("seed: failed to list bundles for migration", "error", err)
		return
	}
	for _, b := range existingBun {
		if err := h.Store.DeleteBundle(ctx, b.ID); err != nil {
			slog.Error("seed: failed to delete old bundle", "slug", b.Slug, "error", err)
		}
	}

	// Delete all existing plans and addons to avoid duplicates when seedAllData re-creates them.
	existingPlans, _ := h.Store.ListPlans(ctx)
	for _, p := range existingPlans {
		_ = h.Store.DeletePlan(ctx, p.ID)
	}
	existingAddOns, _ := h.Store.ListAddOns(ctx)
	for _, a := range existingAddOns {
		_ = h.Store.DeleteAddOn(ctx, a.ID)
	}

	// Re-seed everything.
	h.seedAllData(ctx)
}

// seedMissingAddOns checks existing add-ons and inserts any that are missing
// from the expected set. Also updates pricing/included status and removes stale ones.
func (h *Handler) seedMissingAddOns(ctx context.Context) {
	existing, err := h.Store.ListAddOns(ctx)
	if err != nil {
		slog.Error("seed: failed to list addons", "error", err)
		return
	}

	slugs := make(map[string]bool)
	for _, a := range existing {
		slugs[a.Slug] = true
	}

	expected := expectedAddOns()

	added := 0
	for i := range expected {
		if !slugs[expected[i].Slug] {
			if err := h.Store.CreateAddOn(ctx, &expected[i]); err != nil {
				slog.Error("seed: failed to add missing addon", "slug", expected[i].Slug, "error", err)
			} else {
				added++
				slog.Info("seed: added missing addon", "slug", expected[i].Slug)
			}
		}
	}
	if added > 0 {
		slog.Info("seed: added missing addons", "count", added)
	}

	// Update existing addons that have changed pricing/included status.
	expectedBySlug := make(map[string]store.AddOn)
	for _, a := range expected {
		expectedBySlug[a.Slug] = a
	}
	for _, a := range existing {
		if exp, ok := expectedBySlug[a.Slug]; ok {
			if a.PriceOMR != exp.PriceOMR || a.Included != exp.Included || a.Description != exp.Description {
				a.PriceOMR = exp.PriceOMR
				a.Included = exp.Included
				a.Description = exp.Description
				if err := h.Store.UpdateAddOn(ctx, a.ID, &a); err != nil {
					slog.Error("seed: failed to update addon", "slug", a.Slug, "error", err)
				} else {
					slog.Info("seed: updated addon", "slug", a.Slug, "price", exp.PriceOMR, "included", exp.Included)
				}
			}
		}
	}

	// Remove stale addons not in expected list.
	expectedSlugs := make(map[string]bool)
	for _, a := range expected {
		expectedSlugs[a.Slug] = true
	}
	for _, a := range existing {
		if !expectedSlugs[a.Slug] {
			if err := h.Store.DeleteAddOn(ctx, a.ID); err != nil {
				slog.Error("seed: failed to remove stale addon", "slug", a.Slug, "error", err)
			} else {
				slog.Info("seed: removed stale addon", "slug", a.Slug)
			}
		}
	}
}

// migratePlans checks the current plan set and migrates from the old
// XS/S/M/L tiers to the new S/M/L/XL/Flexi tiers if needed.
func (h *Handler) migratePlans(ctx context.Context) {
	existing, err := h.Store.ListPlans(ctx)
	if err != nil {
		slog.Error("seed: failed to list plans for migration", "error", err)
		return
	}

	slugs := make(map[string]*store.Plan)
	for i := range existing {
		slugs[existing[i].Slug] = &existing[i]
	}

	// If we already have xl/flexi, just check RAM ratios and features.
	if _, ok := slugs["xl"]; ok {
		h.migrateRamRatios(ctx)
		h.migratePlanFeatures(ctx)
		return
	}

	slog.Info("seed: migrating plans from XS/S/M/L to S/M/L/XL/Flexi")

	if xs, ok := slugs["xs"]; ok {
		if err := h.Store.DeletePlan(ctx, xs.ID); err != nil {
			slog.Error("seed: failed to delete XS plan", "error", err)
		} else {
			slog.Info("seed: deleted XS plan")
		}
	}

	if s, ok := slugs["s"]; ok {
		s.Description = "For personal projects and small teams"
		s.PriceOMR = 5
		s.SortOrder = 1
		if err := h.Store.UpdatePlan(ctx, s.ID, s); err != nil {
			slog.Error("seed: failed to update S plan", "error", err)
		}
	}

	if m, ok := slugs["m"]; ok {
		m.Description = "For growing businesses up to 30 users"
		m.PriceOMR = 9
		m.Popular = true
		m.SortOrder = 2
		if err := h.Store.UpdatePlan(ctx, m.ID, m); err != nil {
			slog.Error("seed: failed to update M plan", "error", err)
		}
	}

	if l, ok := slugs["l"]; ok {
		l.Description = "For teams with 30\u2013100 users"
		l.CPU = "8 vCPU"
		l.Memory = "16 GB"
		l.Storage = "100 GB"
		l.PriceOMR = 16
		l.SortOrder = 3
		if err := h.Store.UpdatePlan(ctx, l.ID, l); err != nil {
			slog.Error("seed: failed to update L plan", "error", err)
		}
	}

	xl := store.Plan{Slug: "xl", Name: "XL", Description: "For enterprises with 100+ users", CPU: "16 vCPU", Memory: "32 GB", Storage: "200 GB", PriceOMR: 30, Popular: false, SortOrder: 4}
	if err := h.Store.CreatePlan(ctx, &xl); err != nil {
		slog.Error("seed: failed to create XL plan", "error", err)
	}

	flexi := store.Plan{Slug: "flexi", Name: "Flexi", Description: "Pay as you go \u2014 scale resources on demand", CPU: "On demand", Memory: "On demand", Storage: "On demand", PriceOMR: 0, Popular: false, SortOrder: 5}
	if err := h.Store.CreatePlan(ctx, &flexi); err != nil {
		slog.Error("seed: failed to create Flexi plan", "error", err)
	}

	slog.Info("seed: plan migration complete")
	h.migrateRamRatios(ctx)
	h.migratePlanFeatures(ctx)
}

// migrateRamRatios ensures all plans use the correct 1:2 vCPU:RAM ratio.
func (h *Handler) migrateRamRatios(ctx context.Context) {
	existing, err := h.Store.ListPlans(ctx)
	if err != nil {
		slog.Error("seed: failed to list plans for RAM migration", "error", err)
		return
	}

	expectedRam := map[string]string{
		"s": "4 GB", "m": "8 GB", "l": "16 GB", "xl": "32 GB",
	}

	updated := 0
	for i := range existing {
		want, ok := expectedRam[existing[i].Slug]
		if !ok || existing[i].Memory == want {
			continue
		}
		existing[i].Memory = want
		if err := h.Store.UpdatePlan(ctx, existing[i].ID, &existing[i]); err != nil {
			slog.Error("seed: failed to update RAM ratio", "slug", existing[i].Slug, "error", err)
		} else {
			updated++
			slog.Info("seed: fixed RAM ratio", "slug", existing[i].Slug, "memory", want)
		}
	}
	if updated > 0 {
		slog.Info("seed: RAM ratio migration complete", "updated", updated)
	}
}

// migratePlanFeatures ensures all plans have their features populated.
func (h *Handler) migratePlanFeatures(ctx context.Context) {
	existing, err := h.Store.ListPlans(ctx)
	if err != nil {
		slog.Error("seed: failed to list plans for features migration", "error", err)
		return
	}

	expectedFeatures := map[string][]string{
		"s":     {"Unlimited apps", "SSO included", "API access", "TLS certificates", "Daily snapshots"},
		"m":     {"Unlimited apps", "SSO included", "API access", "TLS certificates", "Daily backups", "Priority support", "Custom domain"},
		"l":     {"Unlimited apps", "SSO included", "API access", "TLS certificates", "Hourly backups", "Priority support", "Custom domain", "WAF/IPS", "Dedicated support"},
		"xl":    {"Unlimited apps", "SSO included", "API access", "TLS certificates", "Continuous backups", "Priority support", "Custom domain", "WAF/IPS", "Dedicated support", "SLA 99.9%", "Audit logs"},
		"flexi": {"Unlimited apps", "SSO included", "API access", "TLS certificates", "Pay per use", "Scale on demand"},
	}

	updated := 0
	for i := range existing {
		want, ok := expectedFeatures[existing[i].Slug]
		if !ok || len(existing[i].Features) > 0 {
			continue
		}
		existing[i].Features = want
		if err := h.Store.UpdatePlan(ctx, existing[i].ID, &existing[i]); err != nil {
			slog.Error("seed: failed to update plan features", "slug", existing[i].Slug, "error", err)
		} else {
			updated++
			slog.Info("seed: added features to plan", "slug", existing[i].Slug)
		}
	}
	if updated > 0 {
		slog.Info("seed: plan features migration complete", "updated", updated)
	}
}

// seedSystemApps inserts mysql, postgres, and redis as system catalog apps
// (hidden from the marketplace but selectable as dependencies in the admin UI).
// Idempotent: re-runs every startup and only creates missing entries, and
// promotes any existing entry with the same slug to System=true.
func (h *Handler) seedSystemApps(ctx context.Context) {
	now := time.Now().UTC()
	replicasField := store.ConfigField{Key: "replicas", Label: "Replicas", Type: "int", Default: 1, Min: intPtr(1), Max: intPtr(5), Description: "Number of database instances in the cluster.", Advanced: false}
	diskField := store.ConfigField{Key: "disk_gb", Label: "Storage (GB)", Type: "int", Default: 5, Min: intPtr(1), Max: intPtr(500), Description: "Persistent volume size per replica.", Advanced: false}
	backupField := store.ConfigField{Key: "backups_enabled", Label: "Daily backups", Type: "bool", Default: false, Description: "Enable daily backups to object storage.", Advanced: true}

	systemApps := []store.App{
		{Slug: "mysql", Name: "MySQL", Tagline: "Relational database engine", Description: "Managed MySQL backing store. Provisioned automatically when required by an app dependency.", Category: "database", Icon: "\U0001F5C4", IconBg: "#00758F", System: true, Kind: "service", Shareable: true, Free: true, RamMB: 256, CpuMilli: 200, DiskGB: 5, HelmChart: "mysql", HelmRepo: "https://charts.bitnami.com/bitnami", ConfigSchema: []store.ConfigField{replicasField, diskField, backupField}, CreatedAt: now, UpdatedAt: now},
		{Slug: "postgres", Name: "PostgreSQL", Tagline: "Advanced open-source relational database", Description: "Managed PostgreSQL backing store. Provisioned automatically when required by an app dependency.", Category: "database", Icon: "\U0001F418", IconBg: "#336791", System: true, Kind: "service", Shareable: true, Free: true, RamMB: 256, CpuMilli: 200, DiskGB: 5, HelmChart: "postgresql", HelmRepo: "https://charts.bitnami.com/bitnami", ConfigSchema: []store.ConfigField{replicasField, diskField, backupField}, CreatedAt: now, UpdatedAt: now},
		{Slug: "redis", Name: "Redis", Tagline: "In-memory key-value cache", Description: "Managed Redis backing cache. Provisioned automatically when required by an app dependency.", Category: "database", Icon: "R", IconBg: "#DC382D", System: true, Kind: "service", Shareable: true, Free: true, RamMB: 128, CpuMilli: 100, DiskGB: 1, HelmChart: "redis", HelmRepo: "https://charts.bitnami.com/bitnami", ConfigSchema: []store.ConfigField{{Key: "replicas", Label: "Replicas", Type: "int", Default: 1, Min: intPtr(1), Max: intPtr(3), Description: "Number of Redis instances.", Advanced: false}, {Key: "persistence", Label: "Persistence", Type: "bool", Default: true, Description: "Persist data to disk (disable for pure cache).", Advanced: true}}, CreatedAt: now, UpdatedAt: now},
	}

	created, promoted := 0, 0
	for i := range systemApps {
		existing, err := h.Store.GetApp(ctx, systemApps[i].Slug)
		if err != nil {
			slog.Error("seed: failed to check system app", "slug", systemApps[i].Slug, "error", err)
			continue
		}
		if existing == nil {
			if err := h.Store.CreateApp(ctx, &systemApps[i]); err != nil {
				slog.Error("seed: failed to create system app", "slug", systemApps[i].Slug, "error", err)
				continue
			}
			created++
			continue
		}
		changed := false
		if !existing.System {
			existing.System = true
			existing.Category = "database"
			changed = true
		}
		if existing.Kind != "service" {
			existing.Kind = "service"
			changed = true
		}
		if !existing.Shareable {
			existing.Shareable = true
			changed = true
		}
		if len(existing.ConfigSchema) == 0 && len(systemApps[i].ConfigSchema) > 0 {
			existing.ConfigSchema = systemApps[i].ConfigSchema
			changed = true
		}
		if changed {
			if err := h.Store.UpdateApp(ctx, existing.ID, existing); err != nil {
				slog.Error("seed: failed to promote app to system", "slug", existing.Slug, "error", err)
				continue
			}
			promoted++
		}
	}
	if created > 0 {
		slog.Info("seed: created system apps", "count", created)
	}
	if promoted > 0 {
		slog.Info("seed: promoted apps to system", "count", promoted)
	}
}

// migrateAppDependencies fills in the Dependencies field on existing catalog
// apps where we know the required backing service. Idempotent — re-applies on
// every startup and only updates when the current Dependencies differ.
func (h *Handler) migrateAppDependencies(ctx context.Context) {
	knownDeps := map[string][]string{
		"wordpress":    {"mysql"},
		"ghost":        {"mysql"},
		"invoiceshelf": {"mysql"},
		"bookstack":    {"mysql"},
		"umami":        {"postgres"},
		"cal-com":      {"postgres"},
		"nextcloud":    {"postgres"},
		"gitea":        {"postgres"},
		"nocodb":       {"postgres"},
		"listmonk":     {"postgres"},
		"formbricks":   {"postgres"},
		"chatwoot":     {"postgres", "redis"},
	}

	updated := 0
	for slug, deps := range knownDeps {
		app, err := h.Store.GetApp(ctx, slug)
		if err != nil || app == nil {
			continue
		}
		if sameStrings(app.Dependencies, deps) {
			continue
		}
		app.Dependencies = deps
		if err := h.Store.UpdateApp(ctx, app.ID, app); err != nil {
			slog.Error("seed: failed to update app dependencies", "slug", slug, "error", err)
			continue
		}
		updated++
	}
	if updated > 0 {
		slog.Info("seed: app dependencies migration complete", "updated", updated)
	}
}

// migrateAppDeployable marks catalog apps as deployable (real provisioning
// template + verified end-to-end install) or not. Issue #102. Before this
// flag, unknown slugs silently deployed an nginx:1-alpine placeholder that
// the UI reported as "installed OK" — a correctness bug worse than an
// outright failure because it fabricated working installs. InstallApp in the
// tenant service now refuses to queue day-2 work for non-deployable apps.
//
// The list below must agree with KnownApps in
// services/provisioning/gitops/apps.go and with what the dod harness has
// actually driven green. Apps listed in KnownApps but known to crashloop
// (chatwoot → needs Redis, issue #100; listmonk → config.toml bug,
// issue #101; rocket-chat → needs MongoDB backing service) are marked
// NOT deployable until their fixes ship.
func (h *Handler) migrateAppDeployable(ctx context.Context) {
	deployable := map[string]bool{
		"wordpress":    true,
		"ghost":        true,
		"nextcloud":    true,
		"bookstack":    true,
		"uptime-kuma":  true,
		"gitea":        true,
		"vaultwarden":  true,
		"umami":        true,
		"nocodb":       true,
		"cal-com":      true,
		"invoiceshelf": true,
		"formbricks":   true,
		"listmonk":     true, // fixed in #101 — DBEnvStyle:"listmonk" + InitCommand
		// Backing services are always deployable — they come bundled with
		// whichever business app needs them. Marking them true so the
		// catalog UI doesn't draw a 'Coming soon' overlay on them. #112.
		"postgres": true,
		"mysql":    true,
		"redis":    true,
	}
	apps, err := h.Store.ListApps(ctx)
	if err != nil {
		slog.Error("seed: list apps for deployable migration", "error", err)
		return
	}
	updated := 0
	for i := range apps {
		a := &apps[i]
		want := deployable[a.Slug]
		if a.Deployable == want {
			continue
		}
		a.Deployable = want
		if err := h.Store.UpdateApp(ctx, a.ID, a); err != nil {
			slog.Error("seed: failed to mark app deployable",
				"slug", a.Slug, "deployable", want, "error", err)
			continue
		}
		updated++
	}
	if updated > 0 {
		slog.Info("seed: app deployable flag migration complete", "updated", updated)
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// dedupBySlug removes duplicate plans and addons (keeps the first occurrence of each slug).
func (h *Handler) dedupBySlug(ctx context.Context) {
	// Dedup plans
	plans, _ := h.Store.ListPlans(ctx)
	seenPlan := make(map[string]bool)
	removed := 0
	for _, p := range plans {
		if seenPlan[p.Slug] {
			_ = h.Store.DeletePlan(ctx, p.ID)
			removed++
		} else {
			seenPlan[p.Slug] = true
		}
	}
	if removed > 0 {
		slog.Info("seed: removed duplicate plans", "count", removed)
	}

	// Dedup addons
	addons, _ := h.Store.ListAddOns(ctx)
	seenAddon := make(map[string]bool)
	removed = 0
	for _, a := range addons {
		if seenAddon[a.Slug] {
			_ = h.Store.DeleteAddOn(ctx, a.ID)
			removed++
		} else {
			seenAddon[a.Slug] = true
		}
	}
	if removed > 0 {
		slog.Info("seed: removed duplicate addons", "count", removed)
	}
}

func intPtr(i int) *int { return &i }
