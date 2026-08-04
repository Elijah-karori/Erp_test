package email

import (
	"fmt"
	"log"
	"net/smtp"
	"os"
)

type EmailService struct {
	smtpHost string
	smtpPort string
	smtpUser string
	smtpPass string
	sender   string
}

func NewEmailService() *EmailService {
	host := os.Getenv("SMTP_HOST")
	port := os.Getenv("SMTP_PORT")
	user := os.Getenv("SMTP_USER")
	pass := os.Getenv("SMTP_PASS")
	sender := os.Getenv("SMTP_SENDER")

	if host == "" {
		host = "smtp.gmail.com"
	}
	if port == "" {
		port = "587"
	}
	if sender == "" {
		sender = user
	}
	if sender == "" {
		sender = "no-reply@sme-kenya-erp.test"
	}

	return &EmailService{
		smtpHost: host,
		smtpPort: port,
		smtpUser: user,
		smtpPass: pass,
		sender:   sender,
	}
}

func (s *EmailService) SendEmail(to, subject, body string, isHTML bool) error {
	if s.smtpUser == "" || s.smtpPass == "" {
		log.Printf("[SMTP MOCK] Sending email to: %s\nSubject: %s\nBody: %s\n", to, subject, body)
		return nil
	}

	contentType := "text/plain; charset=UTF-8"
	if isHTML {
		contentType = "text/html; charset=UTF-8"
	}

	message := fmt.Sprintf("From: %s\r\n"+
		"To: %s\r\n"+
		"Subject: %s\r\n"+
		"MIME-version: 1.0;\r\n"+
		"Content-Type: %s\r\n\r\n"+
		"%s", s.sender, to, subject, contentType, body)

	auth := smtp.PlainAuth("", s.smtpUser, s.smtpPass, s.smtpHost)

	addr := fmt.Sprintf("%s:%s", s.smtpHost, s.smtpPort)
	err := smtp.SendMail(addr, auth, s.sender, []string{to}, []byte(message))
	if err != nil {
		log.Printf("Failed to send real SMTP email: %v. Falling back to log print.", err)
		log.Printf("[SMTP MOCK FALLBACK] Sending email to: %s\nSubject: %s\nBody: %s\n", to, subject, body)
		return nil // Avoid crashing/blocking the application
	}

	return nil
}

func (s *EmailService) SendApprovalNotification(to, approverName, itemType, itemID, status string) error {
	subject := fmt.Sprintf("ERP Notification: Approval Status Update for %s %s", itemType, itemID)
	body := fmt.Sprintf(`<h2>Approval Action Logged</h2>
<p>Hello,</p>
<p>An approval action has been processed by <strong>%s</strong>:</p>
<ul>
  <li><strong>Item:</strong> %s %s</li>
  <li><strong>Action/Status:</strong> %s</li>
</ul>
<p>Please check your workspace dashboard for further details.</p>
<p>Best regards,<br>ERP System Team</p>`, approverName, itemType, itemID, status)

	return s.SendEmail(to, subject, body, true)
}

func (s *EmailService) SendInviteEmail(to, name, inviteUrl, tenantID, roleAssignmentID string) error {
	subject := "Invitation to join SME Kenya ERP Workspace"
	body := fmt.Sprintf(`<h2>Workspace Invitation</h2>
<p>Hello %s,</p>
<p>You have been invited to join an enterprise workspace on SME Kenya ERP Platform.</p>
<p>Click the link below to activate your account and set up your password:</p>
<p><a href="%s" style="display:inline-block;padding:10px 20px;background-color:#4F46E5;color:white;text-decoration:none;border-radius:6px;font-weight:bold;">Activate Workspace Account</a></p>
<hr>
<p><strong>Tenant ID:</strong> %s</p>
<p><strong>Role Assignment ID:</strong> %s</p>
<p>This invitation link is valid for 7 days.</p>
<p>Best regards,<br>ERP System Team</p>`, name, inviteUrl, tenantID, roleAssignmentID)

	return s.SendEmail(to, subject, body, true)
}

func (s *EmailService) SendLoginConfirmation(to, name, timeStr, location string) error {
	subject := "Security Alert: Successful Login to SME Kenya ERP"
	body := fmt.Sprintf(`<h2>Successful Login Confirmation</h2>
<p>Hello %s,</p>
<p>Your workspace account has been successfully accessed.</p>
<ul>
  <li><strong>Time:</strong> %s</li>
  <li><strong>Region/Location:</strong> %s</li>
</ul>
<p>If this was not you, please immediately notify your workspace administrator and reset your password.</p>
<p>Best regards,<br>ERP System Security Team</p>`, name, timeStr, location)

	return s.SendEmail(to, subject, body, true)
}
