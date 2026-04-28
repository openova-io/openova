package handlers

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/openova-io/openova/core/services/shared/events"
	"github.com/openova-io/openova/core/services/shared/respond"

	"github.com/openova-io/openova/core/services/notification/templates"
)

// MailSender is the minimal mail interface the event handlers use.
// Keeping it as an interface lets tests inject an in-memory recorder
// without mocking net/smtp. Production code passes *Mailer, which
// implements this by talking to Stalwart over SMTP.
type MailSender interface {
	Send(to, subject, htmlBody string) error
}

// Handler holds dependencies for notification HTTP handlers.
type Handler struct {
	Mailer   MailSender
	Producer *events.Producer
	// Enricher looks up tenant + owner metadata for events that carry
	// only tenant_id (e.g. day-2 provision.app_* events from
	// provisioning). Optional — when nil or unconfigured, day-2 email
	// handlers log and skip instead of erroring so the consumer never
	// blocks the partition on missing enrichment config.
	Enricher *Enricher
}

// sendRequest is the JSON body for POST /notification/send.
type sendRequest struct {
	To       string          `json:"to"`
	Subject  string          `json:"subject"`
	Template string          `json:"template"`
	Data     json.RawMessage `json:"data"`
}

// SendNotification handles POST /notification/send.
// It renders the requested template and sends it via SMTP.
func (h *Handler) SendNotification(w http.ResponseWriter, r *http.Request) {
	var req sendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respond.Error(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	if req.To == "" || req.Template == "" {
		respond.Error(w, http.StatusBadRequest, "to and template are required")
		return
	}

	htmlBody, subject, err := renderTemplate(req.Template, req.Subject, req.Data)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := h.Mailer.Send(req.To, subject, htmlBody); err != nil {
		slog.Error("failed to send email", "to", req.To, "template", req.Template, "error", err)
		respond.Error(w, http.StatusInternalServerError, "failed to send email")
		return
	}

	slog.Info("email sent", "to", req.To, "template", req.Template)
	respond.OK(w, map[string]string{"status": "sent"})
}

// renderTemplate resolves a template name and data into HTML + subject.
func renderTemplate(tmpl, subject string, data json.RawMessage) (string, string, error) {
	switch tmpl {
	case "welcome":
		var d struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(data, &d); err != nil {
			return "", "", err
		}
		if subject == "" {
			subject = "Welcome to OpenOva SME"
		}
		return templates.WelcomeEmail(d.Name), subject, nil

	case "magic-link":
		var d struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(data, &d); err != nil {
			return "", "", err
		}
		if subject == "" {
			subject = "Your login code"
		}
		return templates.MagicLinkEmail(d.Code), subject, nil

	case "provision-started":
		var d struct {
			OrgName string   `json:"org_name"`
			Apps    []string `json:"apps"`
		}
		if err := json.Unmarshal(data, &d); err != nil {
			return "", "", err
		}
		if subject == "" {
			subject = "We're setting up your tenant"
		}
		return templates.ProvisionStartedEmail(d.OrgName, d.Apps), subject, nil

	case "provision-completed":
		var d struct {
			OrgName      string   `json:"org_name"`
			WorkspaceURL string   `json:"workspace_url"`
			Apps         []string `json:"apps"`
		}
		if err := json.Unmarshal(data, &d); err != nil {
			return "", "", err
		}
		if subject == "" {
			subject = "Your tenant is ready!"
		}
		return templates.ProvisionCompletedEmail(d.OrgName, d.WorkspaceURL, d.Apps), subject, nil

	case "provision-failed":
		var d struct {
			OrgName  string `json:"org_name"`
			ErrorMsg string `json:"error_msg"`
		}
		if err := json.Unmarshal(data, &d); err != nil {
			return "", "", err
		}
		if subject == "" {
			subject = "Something went wrong with your tenant"
		}
		return templates.ProvisionFailedEmail(d.OrgName, d.ErrorMsg), subject, nil

	case "invite-member":
		var d struct {
			OrgName     string `json:"org_name"`
			InviterName string `json:"inviter_name"`
			Role        string `json:"role"`
		}
		if err := json.Unmarshal(data, &d); err != nil {
			return "", "", err
		}
		if subject == "" {
			subject = "You've been invited to " + d.OrgName
		}
		return templates.InviteMemberEmail(d.OrgName, d.InviterName, d.Role), subject, nil

	case "payment-received":
		var d struct {
			OrgName string `json:"org_name"`
			Amount  int    `json:"amount"`
		}
		if err := json.Unmarshal(data, &d); err != nil {
			return "", "", err
		}
		if subject == "" {
			subject = "Payment confirmation"
		}
		return templates.PaymentReceivedEmail(d.OrgName, d.Amount), subject, nil

	default:
		return "", "", fmt.Errorf("unknown template: %s", tmpl)
	}
}
