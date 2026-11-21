// Package notify sends plain-text notification emails over SMTP using only
// the standard library.
package notify

import (
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// Mailer holds the outgoing SMTP settings.
type Mailer struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
}

// Configured reports whether enough is set to attempt delivery.
func (m Mailer) Configured() bool {
	return m.Host != "" && m.Port > 0 && m.From != ""
}

const timeout = 20 * time.Second

// Send delivers one plain-text message. Port 465 uses implicit TLS; any other
// port must offer STARTTLS. The message (and any credentials) is never sent in
// the clear, except to a mail server on this machine.
func (m Mailer) Send(to, subject, body string) error {
	return m.SendWithReplyTo(to, "", subject, body)
}

// SendWithReplyTo is Send with a Reply-To address, so answering the
// notification writes to the person it is about. replyTo may be empty.
func (m Mailer) SendWithReplyTo(to, replyTo, subject, body string) error {
	if !m.Configured() {
		return errors.New("smtp is not configured")
	}
	if to == "" {
		return errors.New("no recipient")
	}
	addr := net.JoinHostPort(m.Host, strconv.Itoa(m.Port))
	tlsCfg := &tls.Config{ServerName: m.Host, MinVersion: tls.VersionTLS12}

	conn, err := (&net.Dialer{Timeout: timeout}).Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("connecting to %s: %w", addr, err)
	}
	_ = conn.SetDeadline(time.Now().Add(timeout))
	encrypted := false
	if m.Port == 465 {
		conn = tls.Client(conn, tlsCfg)
		encrypted = true
	}

	c, err := smtp.NewClient(conn, m.Host)
	if err != nil {
		conn.Close()
		return fmt.Errorf("smtp greeting: %w", err)
	}
	defer c.Close()

	if !encrypted {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(tlsCfg); err != nil {
				return fmt.Errorf("starttls: %w", err)
			}
			encrypted = true
		}
	}
	if !encrypted && !isLocal(m.Host) {
		return fmt.Errorf("%s does not offer TLS; refusing to send mail in the clear", addr)
	}

	if m.Username != "" {
		// PlainAuth itself refuses to run over an unencrypted, non-local link.
		if err := c.Auth(smtp.PlainAuth("", m.Username, m.Password, m.Host)); err != nil {
			return fmt.Errorf("smtp auth: %w", err)
		}
	}
	if err := c.Mail(addrOnly(m.From)); err != nil {
		return fmt.Errorf("MAIL FROM: %w", err)
	}
	if err := c.Rcpt(addrOnly(to)); err != nil {
		return fmt.Errorf("RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("DATA: %w", err)
	}
	if _, err := w.Write(buildMessage(m.From, to, replyTo, subject, body, time.Now())); err != nil {
		return fmt.Errorf("writing message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("finishing message: %w", err)
	}
	return c.Quit()
}

// buildMessage renders an RFC 5322 message. Header values are stripped of
// line breaks so nothing user-supplied can inject extra headers.
func buildMessage(from, to, replyTo, subject, body string, now time.Time) []byte {
	var b strings.Builder
	header := func(k, v string) { b.WriteString(k + ": " + oneLine(v) + "\r\n") }
	header("From", from)
	header("To", to)
	if replyTo != "" {
		header("Reply-To", replyTo)
	}
	header("Subject", mime.QEncoding.Encode("utf-8", oneLine(subject)))
	header("Date", now.Format(time.RFC1123Z))
	header("MIME-Version", "1.0")
	header("Content-Type", "text/plain; charset=utf-8")
	header("Content-Transfer-Encoding", "8bit")
	b.WriteString("\r\n")
	body = strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\r", "\n")
	b.WriteString(strings.ReplaceAll(body, "\n", "\r\n"))
	if !strings.HasSuffix(body, "\n") {
		b.WriteString("\r\n")
	}
	return []byte(b.String())
}

func oneLine(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r == '\r' || r == '\n' {
			return ' '
		}
		return r
	}, s))
}

// addrOnly extracts the bare address from "Name <addr>" for the envelope.
func addrOnly(s string) string {
	s = oneLine(s)
	if i, j := strings.LastIndex(s, "<"), strings.LastIndex(s, ">"); i >= 0 && j > i {
		return s[i+1 : j]
	}
	return s
}

func isLocal(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
