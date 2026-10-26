package mailer

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/mail"
	"net/smtp"
	"net/textproto"
	"strconv"
	"time"

	"github.com/mt-sense/backend-service/internal/config"
)

// Sender delivers one message. Implementations must be safe for concurrent use.
type Sender interface {
	Send(ctx context.Context, msg Message) error
	// Configured is false for the log-only sender, so the UI can say mail is not set up.
	Configured() bool
	From() string
}

// NewSender returns an SMTP sender, or a log-only sender when SMTP_HOST is empty.
func NewSender(cfg config.SMTPConfig) Sender {
	if cfg.Host == "" {
		log.Println("mailer: SMTP_HOST is not set — emails will be written to the log, not sent")
		return logSender{}
	}
	if cfg.From == "" {
		log.Fatal("mailer: SMTP_FROM is required when SMTP_HOST is set")
	}
	if !ValidAddress(cfg.From) {
		log.Fatalf("mailer: SMTP_FROM=%q is not a valid email address", cfg.From)
	}
	switch cfg.Security {
	case "starttls", "tls", "none":
	default:
		log.Fatalf("mailer: SMTP_SECURITY must be starttls, tls or none, got %q", cfg.Security)
	}
	return &smtpSender{cfg: cfg}
}

type logSender struct{}

func (logSender) Send(_ context.Context, msg Message) error {
	log.Printf("mailer (log only): to=%s subject=%q\n%s", msg.ToEmail, msg.Subject, msg.Text)
	return nil
}
func (logSender) Configured() bool { return false }
func (logSender) From() string     { return "" }

type smtpSender struct {
	cfg config.SMTPConfig
}

func (s *smtpSender) Configured() bool { return true }
func (s *smtpSender) From() string     { return s.cfg.From }

func (s *smtpSender) Send(ctx context.Context, msg Message) error {
	from := mail.Address{Name: headerSafe(s.cfg.FromName), Address: s.cfg.From}
	body, err := build(from, msg, time.Now())
	if err != nil {
		return permanent(err)
	}

	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(30 * time.Second)
	}
	addr := net.JoinHostPort(s.cfg.Host, strconv.Itoa(s.cfg.Port))
	dialer := &net.Dialer{Deadline: deadline}
	tlsConfig := &tls.Config{ServerName: s.cfg.Host, MinVersion: tls.VersionTLS12}

	var conn net.Conn
	if s.cfg.Security == "tls" {
		conn, err = tls.DialWithDialer(dialer, "tcp", addr, tlsConfig)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("connecting to %s: %w", addr, err)
	}
	_ = conn.SetDeadline(deadline)

	client, err := smtp.NewClient(conn, s.cfg.Host)
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("smtp handshake: %w", err)
	}
	defer client.Close()

	if s.cfg.Security == "starttls" {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			return errors.New("server does not offer STARTTLS; set SMTP_SECURITY=tls or none")
		}
		if err := client.StartTLS(tlsConfig); err != nil {
			return fmt.Errorf("starttls: %w", err)
		}
	}
	if s.cfg.Username != "" {
		// PlainAuth refuses to send credentials over an unencrypted connection to anything
		// but localhost, which is the behaviour we want for SMTP_SECURITY=none.
		if err := client.Auth(smtp.PlainAuth("", s.cfg.Username, s.cfg.Password, s.cfg.Host)); err != nil {
			return fmt.Errorf("authentication: %w", err)
		}
	}
	if err := client.Mail(s.cfg.From); err != nil {
		return fmt.Errorf("MAIL FROM: %w", err)
	}
	if err := client.Rcpt(msg.ToEmail); err != nil {
		return classify("RCPT TO", err)
	}
	w, err := client.Data()
	if err != nil {
		return classify("DATA", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("writing message: %w", err)
	}
	if err := w.Close(); err != nil {
		return classify("DATA", err)
	}
	return client.Quit()
}

// PermanentError marks a failure that retrying cannot fix (5xx replies, bad addresses).
type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

func permanent(err error) error { return &PermanentError{Err: err} }

// classify wraps an SMTP reply to the recipient or the message itself, marking 5xx replies
// permanent. 4xx replies (greylisting, rate limits, mailbox busy) stay retryable. Failures
// before RCPT TO (connection, STARTTLS, auth, sender address) are configuration problems an
// admin can fix, so they are never classified permanent and the outbox keeps retrying.
func classify(step string, err error) error {
	wrapped := fmt.Errorf("%s: %w", step, err)
	var reply *textproto.Error
	if errors.As(err, &reply) && reply.Code >= 500 {
		return permanent(wrapped)
	}
	return wrapped
}
