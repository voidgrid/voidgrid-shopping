// Package mailer sends the app's few emails (invites, password resets, a
// test message) through the SMTP server set on the Settings tab.
package mailer

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/mail"
	"net/smtp"
	"strconv"
	"strings"
	"time"
)

// Security modes.
const (
	StartTLS = "starttls"
	TLS      = "tls"
	None     = "none"
)

// Config is one SMTP account.
type Config struct {
	Host     string
	Port     int
	Security string
	Username string
	Password string
	From     string
}

// Configured reports whether enough is set to send.
func (c Config) Configured() bool { return c.Host != "" && c.Port > 0 && c.From != "" }

func noBreaks(s string) bool { return !strings.ContainsAny(s, "\r\n") }

// Send delivers one plain-text message.
func Send(ctx context.Context, c Config, to, subject, body string) error {
	if !c.Configured() {
		return errors.New("mailer: SMTP is not set up")
	}
	from, err := mail.ParseAddress(c.From)
	if err != nil {
		return errors.New("mailer: the From address is not valid")
	}
	rcpt, err := mail.ParseAddress(to)
	if err != nil || !noBreaks(subject) {
		return errors.New("mailer: bad recipient or subject")
	}
	addr := net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	d := &net.Dialer{Timeout: 15 * time.Second}
	tcfg := &tls.Config{ServerName: c.Host, MinVersion: tls.VersionTLS12}

	var conn net.Conn
	if c.Security == TLS {
		conn, err = tls.DialWithDialer(d, "tcp", addr, tcfg)
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("mailer: cannot connect to %s: %w", addr, err)
	}
	_ = conn.SetDeadline(time.Now().Add(45 * time.Second))
	cl, err := smtp.NewClient(conn, c.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("mailer: %w", err)
	}
	defer func() { _ = cl.Close() }()
	if c.Security == StartTLS {
		if ok, _ := cl.Extension("STARTTLS"); !ok {
			return errors.New("mailer: the server does not offer STARTTLS")
		}
		if err := cl.StartTLS(tcfg); err != nil {
			return fmt.Errorf("mailer: STARTTLS: %w", err)
		}
	}
	if c.Username != "" {
		if err := cl.Auth(smtp.PlainAuth("", c.Username, c.Password, c.Host)); err != nil {
			return fmt.Errorf("mailer: sign-in to the mail server failed: %w", err)
		}
	}
	if err := cl.Mail(from.Address); err != nil {
		return fmt.Errorf("mailer: %w", err)
	}
	if err := cl.Rcpt(rcpt.Address); err != nil {
		return fmt.Errorf("mailer: recipient refused: %w", err)
	}
	w, err := cl.Data()
	if err != nil {
		return fmt.Errorf("mailer: %w", err)
	}
	msg := "From: " + from.String() + "\r\nTo: " + rcpt.String() + "\r\nSubject: " + subject +
		"\r\nDate: " + time.Now().UTC().Format(time.RFC1123Z) +
		"\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: 8bit\r\n\r\n" +
		strings.ReplaceAll(strings.ReplaceAll(body, "\r\n", "\n"), "\n", "\r\n") + "\r\n"
	if _, err := w.Write([]byte(msg)); err != nil {
		return fmt.Errorf("mailer: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("mailer: %w", err)
	}
	return cl.Quit()
}
