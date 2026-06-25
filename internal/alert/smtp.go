// Package alert (smtp.go) — отправка писем по SMTP (раздел 15.1 ТЗ):
// none/starttls/tls, multipart/alternative (text + HTML), UTF-8.
package alert

import (
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"mime"
	"net/smtp"
	"strings"
	"time"
)

// SMTPConfig — параметры почты.
type SMTPConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	From     string
	To       []string
	Security string // none|starttls|tls
}

const mimeBoundary = "----=_BackuperBoundary_2c1f9a7e"

func b64(s string) string {
	const chunk = 76
	enc := base64.StdEncoding.EncodeToString([]byte(s))
	var sb strings.Builder
	for i := 0; i < len(enc); i += chunk {
		end := i + chunk
		if end > len(enc) {
			end = len(enc)
		}
		sb.WriteString(enc[i:end])
		sb.WriteString("\r\n")
	}
	return sb.String()
}

func buildMessage(from string, to []string, subject, textBody, htmlBody string) []byte {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + strings.Join(to, ", ") + "\r\n")
	b.WriteString("Subject: " + mime.BEncoding.Encode("UTF-8", subject) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Date: " + time.Now().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("Content-Type: multipart/alternative; boundary=\"" + mimeBoundary + "\"\r\n\r\n")

	b.WriteString("--" + mimeBoundary + "\r\n")
	b.WriteString("Content-Type: text/plain; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
	b.WriteString(b64(textBody))
	b.WriteString("\r\n")

	b.WriteString("--" + mimeBoundary + "\r\n")
	b.WriteString("Content-Type: text/html; charset=UTF-8\r\n")
	b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
	b.WriteString(b64(htmlBody))
	b.WriteString("\r\n")

	b.WriteString("--" + mimeBoundary + "--\r\n")
	return []byte(b.String())
}

func (m *Manager) sendRaw(subject, textBody, htmlBody string) error {
	cfg := m.smtp
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	msg := buildMessage(cfg.From, cfg.To, subject, textBody, htmlBody)

	var auth smtp.Auth
	if cfg.User != "" {
		auth = smtp.PlainAuth("", cfg.User, cfg.Password, cfg.Host)
	}

	deliver := func(c *smtp.Client) error {
		defer c.Close()
		if auth != nil {
			if ok, _ := c.Extension("AUTH"); ok {
				if err := c.Auth(auth); err != nil {
					return fmt.Errorf("SMTP AUTH: %w", err)
				}
			}
		}
		if err := c.Mail(cfg.From); err != nil {
			return err
		}
		for _, rcpt := range cfg.To {
			if err := c.Rcpt(rcpt); err != nil {
				return err
			}
		}
		w, err := c.Data()
		if err != nil {
			return err
		}
		if _, err := w.Write(msg); err != nil {
			return err
		}
		if err := w.Close(); err != nil {
			return err
		}
		return c.Quit()
	}

	switch cfg.Security {
	case "tls":
		conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: cfg.Host})
		if err != nil {
			return fmt.Errorf("TLS-подключение SMTP: %w", err)
		}
		c, err := smtp.NewClient(conn, cfg.Host)
		if err != nil {
			return err
		}
		return deliver(c)
	case "starttls":
		c, err := smtp.Dial(addr)
		if err != nil {
			return fmt.Errorf("подключение SMTP: %w", err)
		}
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: cfg.Host}); err != nil {
				c.Close()
				return fmt.Errorf("STARTTLS: %w", err)
			}
		}
		return deliver(c)
	default: // none
		c, err := smtp.Dial(addr)
		if err != nil {
			return fmt.Errorf("подключение SMTP: %w", err)
		}
		return deliver(c)
	}
}
