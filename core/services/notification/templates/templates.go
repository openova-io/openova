package templates

import (
	"fmt"
	"strings"
)

const (
	brandColor = "#6366f1"
	year       = "2026"
)

// layout wraps body content in the standard email shell.
func layout(title, body string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>%s</title>
</head>
<body style="margin:0;padding:0;background-color:#f4f4f5;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;">
  <table role="presentation" width="100%%" cellpadding="0" cellspacing="0" style="background-color:#f4f4f5;padding:40px 0;">
    <tr>
      <td align="center">
        <table role="presentation" width="600" cellpadding="0" cellspacing="0" style="max-width:600px;width:100%%;background-color:#ffffff;border-radius:8px;overflow:hidden;">
          <!-- Header -->
          <tr>
            <td style="background-color:%s;padding:24px 32px;">
              <h1 style="margin:0;color:#ffffff;font-size:20px;font-weight:600;letter-spacing:-0.02em;">OpenOva SME</h1>
            </td>
          </tr>
          <!-- Body -->
          <tr>
            <td style="padding:32px;">
              %s
            </td>
          </tr>
          <!-- Footer -->
          <tr>
            <td style="padding:24px 32px;border-top:1px solid #e4e4e7;text-align:center;">
              <p style="margin:0 0 8px;color:#71717a;font-size:13px;">&copy; %s OpenOva. All rights reserved.</p>
              <a href="https://openova.io/unsubscribe" style="color:%s;font-size:13px;text-decoration:underline;">Unsubscribe</a>
            </td>
          </tr>
        </table>
      </td>
    </tr>
  </table>
</body>
</html>`, title, brandColor, body, year, brandColor)
}

// button renders a centered CTA button.
func button(text, href string) string {
	return fmt.Sprintf(`<table role="presentation" cellpadding="0" cellspacing="0" style="margin:24px auto;">
  <tr>
    <td style="background-color:%s;border-radius:6px;">
      <a href="%s" style="display:inline-block;padding:12px 28px;color:#ffffff;font-size:15px;font-weight:600;text-decoration:none;">%s</a>
    </td>
  </tr>
</table>`, brandColor, href, text)
}

// WelcomeEmail returns the "Welcome to OpenOva SME" email HTML.
func WelcomeEmail(name string) string {
	body := fmt.Sprintf(`<h2 style="margin:0 0 16px;color:#18181b;font-size:22px;font-weight:600;">Welcome, %s!</h2>
<p style="margin:0 0 16px;color:#3f3f46;font-size:15px;line-height:1.6;">
  Thanks for joining OpenOva SME. We're excited to help you run your business with our all-in-one platform.
</p>
<p style="margin:0 0 8px;color:#3f3f46;font-size:15px;line-height:1.6;">Here's how to get started:</p>
<ul style="margin:0 0 16px;padding-left:20px;color:#3f3f46;font-size:15px;line-height:1.8;">
  <li>Set up your organization profile</li>
  <li>Choose the apps you need</li>
  <li>Invite your team members</li>
</ul>
%s
<p style="margin:0;color:#71717a;font-size:13px;">If you didn't create this account, you can safely ignore this email.</p>`,
		name, button("Get Started", "https://sme.openova.io"))
	return layout("Welcome to OpenOva SME", body)
}

// MagicLinkEmail returns the login code email HTML.
func MagicLinkEmail(code string) string {
	body := fmt.Sprintf(`<h2 style="margin:0 0 16px;color:#18181b;font-size:22px;font-weight:600;">Your login code</h2>
<p style="margin:0 0 24px;color:#3f3f46;font-size:15px;line-height:1.6;">
  Use the code below to sign in to your OpenOva SME account. It expires in 10 minutes.
</p>
<table role="presentation" cellpadding="0" cellspacing="0" style="margin:0 auto 24px;">
  <tr>
    <td style="background-color:#f4f4f5;border:2px solid #e4e4e7;border-radius:8px;padding:16px 32px;">
      <span style="font-size:32px;font-weight:700;letter-spacing:0.15em;color:%s;font-family:'Courier New',monospace;">%s</span>
    </td>
  </tr>
</table>
<p style="margin:0;color:#71717a;font-size:13px;">If you didn't request this code, you can safely ignore this email.</p>`,
		brandColor, code)
	return layout("Your Login Code", body)
}

// ProvisionStartedEmail returns the "setting up tenant" email HTML.
func ProvisionStartedEmail(orgName string, apps []string) string {
	appItems := ""
	for _, app := range apps {
		appItems += fmt.Sprintf(`<li style="padding:4px 0;">%s</li>`, app)
	}
	body := fmt.Sprintf(`<h2 style="margin:0 0 16px;color:#18181b;font-size:22px;font-weight:600;">We're setting up your tenant</h2>
<p style="margin:0 0 16px;color:#3f3f46;font-size:15px;line-height:1.6;">
  Great news! Provisioning has started for <strong>%s</strong>. We're configuring the following apps:
</p>
<ul style="margin:0 0 16px;padding-left:20px;color:#3f3f46;font-size:15px;line-height:1.8;">
  %s
</ul>
<p style="margin:0 0 8px;color:#3f3f46;font-size:15px;line-height:1.6;">
  This usually takes a few minutes. We'll send you another email once everything is ready.
</p>
<p style="margin:0;color:#71717a;font-size:13px;">No action is needed on your part right now.</p>`,
		orgName, appItems)
	return layout("Setting Up Your Tenant", body)
}

// ProvisionCompletedEmail returns the "tenant ready" email HTML.
func ProvisionCompletedEmail(orgName, tenantURL string, apps []string) string {
	appItems := ""
	for _, app := range apps {
		appItems += fmt.Sprintf(`<li style="padding:4px 0;">%s</li>`, app)
	}
	body := fmt.Sprintf(`<h2 style="margin:0 0 16px;color:#18181b;font-size:22px;font-weight:600;">Your tenant is ready!</h2>
<p style="margin:0 0 16px;color:#3f3f46;font-size:15px;line-height:1.6;">
  <strong>%s</strong> has been fully provisioned. The following apps are live and ready to use:
</p>
<ul style="margin:0 0 16px;padding-left:20px;color:#3f3f46;font-size:15px;line-height:1.8;">
  %s
</ul>
%s
<p style="margin:16px 0 0;color:#71717a;font-size:13px;">Bookmark your tenant URL for quick access: <a href="%s" style="color:%s;">%s</a></p>`,
		orgName, appItems, button("Log In to Your Tenant", tenantURL), tenantURL, brandColor, tenantURL)
	return layout("Your Tenant is Ready", body)
}

// ProvisionFailedEmail returns the "something went wrong" email HTML.
func ProvisionFailedEmail(orgName, errorMsg string) string {
	body := fmt.Sprintf(`<h2 style="margin:0 0 16px;color:#18181b;font-size:22px;font-weight:600;">Something went wrong</h2>
<p style="margin:0 0 16px;color:#3f3f46;font-size:15px;line-height:1.6;">
  We encountered an issue while setting up <strong>%s</strong>.
</p>
<table role="presentation" cellpadding="0" cellspacing="0" width="100%%" style="margin:0 0 16px;">
  <tr>
    <td style="background-color:#fef2f2;border:1px solid #fecaca;border-radius:6px;padding:12px 16px;">
      <p style="margin:0;color:#991b1b;font-size:14px;font-family:'Courier New',monospace;">%s</p>
    </td>
  </tr>
</table>
<p style="margin:0 0 16px;color:#3f3f46;font-size:15px;line-height:1.6;">
  Our team has been notified and is looking into this. If the issue persists, please contact support.
</p>
%s
<p style="margin:0;color:#71717a;font-size:13px;">You can also reach us at <a href="mailto:support@openova.io" style="color:%s;">support@openova.io</a></p>`,
		orgName, errorMsg, button("Contact Support", "mailto:support@openova.io"), brandColor)
	return layout("Provisioning Issue", body)
}

// InviteMemberEmail returns the invitation email HTML.
func InviteMemberEmail(orgName, inviterName, role string) string {
	body := fmt.Sprintf(`<h2 style="margin:0 0 16px;color:#18181b;font-size:22px;font-weight:600;">You've been invited to %s</h2>
<p style="margin:0 0 16px;color:#3f3f46;font-size:15px;line-height:1.6;">
  <strong>%s</strong> has invited you to join <strong>%s</strong> on OpenOva SME as a <strong>%s</strong>.
</p>
<p style="margin:0 0 16px;color:#3f3f46;font-size:15px;line-height:1.6;">
  OpenOva SME is an all-in-one business platform that helps teams collaborate, manage operations, and grow.
</p>
%s
<p style="margin:0;color:#71717a;font-size:13px;">If you weren't expecting this invitation, you can safely ignore this email.</p>`,
		orgName, inviterName, orgName, role, button("Accept Invitation", "https://sme.openova.io/invite"))
	return layout("You're Invited", body)
}

// AppReadyEmail is sent on a successful day-2 install so the customer
// knows the app is live. tenantURL opens the tenant's SME console;
// the customer signs in there to start using the app.
func AppReadyEmail(orgName, appSlug, tenantURL string) string {
	body := fmt.Sprintf(`<h2 style="margin:0 0 16px;color:#18181b;font-size:22px;font-weight:600;">%s is ready on %s</h2>
<p style="margin:0 0 16px;color:#3f3f46;font-size:15px;line-height:1.6;">
  Your new app <strong>%s</strong> has been installed on <strong>%s</strong> and is ready to use.
</p>
%s
<p style="margin:16px 0 0;color:#71717a;font-size:13px;">Open your tenant at <a href="%s" style="color:%s;">%s</a></p>`,
		appSlug, orgName, appSlug, orgName, button("Open "+appSlug, tenantURL), tenantURL, brandColor, tenantURL)
	return layout("App Installed", body)
}

// AppRemovedEmail confirms a day-2 uninstall. Keeping the email short —
// the removal itself is the action the customer wanted; we just confirm.
func AppRemovedEmail(orgName, appSlug string) string {
	body := fmt.Sprintf(`<h2 style="margin:0 0 16px;color:#18181b;font-size:22px;font-weight:600;">%s has been removed</h2>
<p style="margin:0 0 16px;color:#3f3f46;font-size:15px;line-height:1.6;">
  <strong>%s</strong> has been uninstalled from <strong>%s</strong>. You won't be billed for it going forward.
</p>
<p style="margin:0 0 16px;color:#3f3f46;font-size:15px;line-height:1.6;">
  You can reinstall it at any time from the Apps page.
</p>
<p style="margin:0;color:#71717a;font-size:13px;">If this wasn't you, please contact <a href="mailto:support@openova.io" style="color:%s;">support@openova.io</a> right away.</p>`,
		appSlug, appSlug, orgName, brandColor)
	return layout("App Removed", body)
}

// AppFailedEmail tells the customer a day-2 install/uninstall failed
// and surfaces the error so they (or support) can act on it.
func AppFailedEmail(orgName, appSlug, action, errorMsg string) string {
	if errorMsg == "" {
		errorMsg = "An unexpected error occurred."
	}
	body := fmt.Sprintf(`<h2 style="margin:0 0 16px;color:#18181b;font-size:22px;font-weight:600;">%s on %s didn't complete</h2>
<p style="margin:0 0 16px;color:#3f3f46;font-size:15px;line-height:1.6;">
  We hit an issue while processing the <strong>%s</strong> of <strong>%s</strong> for <strong>%s</strong>.
</p>
<table role="presentation" cellpadding="0" cellspacing="0" width="100%%" style="margin:0 0 16px;">
  <tr>
    <td style="background-color:#fef2f2;border:1px solid #fecaca;border-radius:6px;padding:12px 16px;">
      <p style="margin:0;color:#991b1b;font-size:14px;font-family:'Courier New',monospace;">%s</p>
    </td>
  </tr>
</table>
<p style="margin:0 0 16px;color:#3f3f46;font-size:15px;line-height:1.6;">
  Our team has been notified. You can retry from the Apps page, or reach us for help.
</p>
%s
<p style="margin:0;color:#71717a;font-size:13px;">Support: <a href="mailto:support@openova.io" style="color:%s;">support@openova.io</a></p>`,
		appSlug, orgName, action, appSlug, orgName, errorMsg, button("Contact Support", "mailto:support@openova.io"), brandColor)
	return layout("App "+action+" Failed", body)
}

// DomainRegisteredEmail acknowledges that a BYOD (bring-your-own-domain)
// record has been added to the tenant. Verification is still pending at
// this stage — the customer has to point DNS before the domain goes
// live, so the copy nudges them toward that next step.
func DomainRegisteredEmail(orgName, domain string) string {
	body := fmt.Sprintf(`<h2 style="margin:0 0 16px;color:#18181b;font-size:22px;font-weight:600;">Domain added</h2>
<p style="margin:0 0 16px;color:#3f3f46;font-size:15px;line-height:1.6;">
  We've added <strong>%s</strong> to <strong>%s</strong>. The next step is to verify it by pointing DNS to our platform.
</p>
<p style="margin:0 0 16px;color:#3f3f46;font-size:15px;line-height:1.6;">
  Check the Domains page in your console for the exact CNAME / TXT records you need to create.
</p>
%s
<p style="margin:0;color:#71717a;font-size:13px;">We'll email you again once the domain is verified.</p>`,
		domain, orgName, button("Open Domains", "https://sme.openova.io/settings/domains"))
	return layout("Domain Added", body)
}

// DomainVerifiedEmail fires when DNS has been confirmed and the domain
// is serving traffic. This is the happy-path companion to
// DomainRegisteredEmail.
func DomainVerifiedEmail(orgName, domain string) string {
	body := fmt.Sprintf(`<h2 style="margin:0 0 16px;color:#18181b;font-size:22px;font-weight:600;">Domain verified</h2>
<p style="margin:0 0 16px;color:#3f3f46;font-size:15px;line-height:1.6;">
  <strong>%s</strong> is now verified and live for <strong>%s</strong>. Your customers can reach your apps at this domain.
</p>
%s
<p style="margin:0;color:#71717a;font-size:13px;">Need to route apps to this domain? Configure mappings on the Domains page.</p>`,
		domain, orgName, button("Open "+domain, "https://"+domain))
	return layout("Domain Verified", body)
}

// DomainRemovedEmail confirms a domain deletion. Included as part of
// #70 so every domain lifecycle transition has an audit email on the
// owner's side.
func DomainRemovedEmail(orgName, domain string) string {
	body := fmt.Sprintf(`<h2 style="margin:0 0 16px;color:#18181b;font-size:22px;font-weight:600;">Domain removed</h2>
<p style="margin:0 0 16px;color:#3f3f46;font-size:15px;line-height:1.6;">
  <strong>%s</strong> has been removed from <strong>%s</strong>. It will no longer serve traffic to your tenant.
</p>
<p style="margin:0;color:#71717a;font-size:13px;">If this wasn't you, please contact <a href="mailto:support@openova.io" style="color:%s;">support@openova.io</a> right away.</p>`,
		domain, orgName, brandColor)
	return layout("Domain Removed", body)
}

// PaymentReceivedEmail returns the payment confirmation email HTML.
// amount is in cents.
func PaymentReceivedEmail(orgName string, amount int) string {
	dollars := float64(amount) / 100
	formatted := fmt.Sprintf("$%.2f", dollars)
	// Format with comma separators for amounts >= 1000
	if dollars >= 1000 {
		parts := strings.SplitN(formatted, ".", 2)
		intPart := parts[0][1:] // remove $
		result := ""
		for i, c := range intPart {
			if i > 0 && (len(intPart)-i)%3 == 0 {
				result += ","
			}
			result += string(c)
		}
		formatted = "$" + result + "." + parts[1]
	}

	body := fmt.Sprintf(`<h2 style="margin:0 0 16px;color:#18181b;font-size:22px;font-weight:600;">Payment confirmed</h2>
<p style="margin:0 0 16px;color:#3f3f46;font-size:15px;line-height:1.6;">
  We've received your payment for <strong>%s</strong>.
</p>
<table role="presentation" cellpadding="0" cellspacing="0" width="100%%" style="margin:0 0 24px;">
  <tr>
    <td style="background-color:#f4f4f5;border-radius:6px;padding:16px 20px;">
      <table role="presentation" width="100%%" cellpadding="0" cellspacing="0">
        <tr>
          <td style="color:#71717a;font-size:14px;">Amount paid</td>
          <td align="right" style="color:#18181b;font-size:20px;font-weight:700;">%s</td>
        </tr>
        <tr>
          <td style="color:#71717a;font-size:14px;padding-top:8px;">Organization</td>
          <td align="right" style="color:#18181b;font-size:14px;padding-top:8px;">%s</td>
        </tr>
      </table>
    </td>
  </tr>
</table>
<p style="margin:0 0 8px;color:#3f3f46;font-size:15px;line-height:1.6;">
  Thank you for your continued trust in OpenOva SME. Your subscription is active and up to date.
</p>
<p style="margin:0;color:#71717a;font-size:13px;">You can view your billing history in <a href="https://sme.openova.io/billing" style="color:%s;">Account Settings</a>.</p>`,
		orgName, formatted, orgName, brandColor)
	return layout("Payment Confirmation", body)
}
