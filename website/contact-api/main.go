package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/smtp"
	"os"
	"strings"
)

type ContactRequest struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Phone    string `json:"phone"`
	Company  string `json:"company"`
	Interest string `json:"interest"`
	Message  string `json:"message"`
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	smtpHost := getEnv("SMTP_HOST", "stalwart-mail.stalwart.svc.cluster.local")
	smtpPort := getEnv("SMTP_PORT", "25")
	toEmail := getEnv("TO_EMAIL", "sales@openova.io")
	fromEmail := getEnv("FROM_EMAIL", "website@openova.io")
	allowOrigin := getEnv("CORS_ORIGIN", "https://openova.io")

	http.HandleFunc("/api/contact", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Access-Control-Allow-Origin", allowOrigin)
		w.Header().Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
			return
		}

		var req ContactRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
			return
		}

		req.Name = strings.TrimSpace(req.Name)
		req.Email = strings.TrimSpace(req.Email)

		if req.Name == "" || req.Email == "" {
			http.Error(w, `{"error":"name and email are required"}`, http.StatusBadRequest)
			return
		}

		interest := req.Interest
		if interest == "" {
			interest = "General"
		}

		subject := fmt.Sprintf("OpenOva website inquiry: %s", interest)
		body := fmt.Sprintf("Name: %s\nEmail: %s\nPhone: %s\nCompany: %s\nInterest: %s\n\n%s",
			req.Name, req.Email, req.Phone, req.Company, interest, req.Message)

		msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nReply-To: %s\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s",
			fromEmail, toEmail, subject, req.Email, body)

		addr := smtpHost + ":" + smtpPort
		if err := smtp.SendMail(addr, nil, fromEmail, []string{toEmail}, []byte(msg)); err != nil {
			log.Printf("SMTP error: %v", err)
			http.Error(w, `{"error":"failed to send message"}`, http.StatusInternalServerError)
			return
		}

		log.Printf("Contact form sent: name=%s email=%s interest=%s", req.Name, req.Email, interest)
		json.NewEncoder(w).Encode(map[string]bool{"success": true})
	})

	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	log.Println("contact-api listening on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
