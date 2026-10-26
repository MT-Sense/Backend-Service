// Package mailer delivers email over SMTP through a Postgres outbox (models.EmailOutbox).
//
// Handlers render a message and insert it in the same transaction as the change that
// triggered it; the worker in outbox.go sends it later and retries transient failures. Only
// the test endpoint sends synchronously, because its whole point is to surface SMTP errors.
package mailer

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"mime"
	"net/mail"
	"strings"
	"time"
)

// Message is one rendered email to a single recipient.
type Message struct {
	ToEmail string
	ToName  string
	Subject string
	Text    string
	HTML    string
}

// headerSafe drops CR/LF so user-influenced values (names, subjects) can never inject
// extra headers.
func headerSafe(value string) string {
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(value)
}

// ValidAddress reports whether value is a single bare email address.
func ValidAddress(value string) bool {
	addr, err := mail.ParseAddress(value)
	return err == nil && addr.Address == value
}

// build serializes msg as an RFC 5322 multipart/alternative message. Bodies are base64 so
// Thai text survives any relay; headers use RFC 2047 encoded words.
func build(from mail.Address, msg Message, now time.Time) ([]byte, error) {
	if !ValidAddress(msg.ToEmail) {
		return nil, fmt.Errorf("invalid recipient address %q", msg.ToEmail)
	}
	to := mail.Address{Name: headerSafe(msg.ToName), Address: msg.ToEmail}
	boundary := randomHex(16)
	domain := "mt-sense.local"
	if at := strings.LastIndex(from.Address, "@"); at >= 0 {
		domain = from.Address[at+1:]
	}

	var b bytes.Buffer
	header := func(key, value string) { fmt.Fprintf(&b, "%s: %s\r\n", key, value) }
	header("From", from.String())
	header("To", to.String())
	header("Subject", mime.BEncoding.Encode("UTF-8", headerSafe(msg.Subject)))
	header("Date", now.Format(time.RFC1123Z))
	header("Message-ID", fmt.Sprintf("<%s@%s>", randomHex(16), domain))
	header("MIME-Version", "1.0")
	header("Content-Type", fmt.Sprintf(`multipart/alternative; boundary="%s"`, boundary))
	b.WriteString("\r\n")

	part := func(contentType, body string) {
		fmt.Fprintf(&b, "--%s\r\n", boundary)
		fmt.Fprintf(&b, "Content-Type: %s; charset=UTF-8\r\n", contentType)
		b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
		encoded := base64.StdEncoding.EncodeToString([]byte(body))
		for len(encoded) > 76 {
			b.WriteString(encoded[:76] + "\r\n")
			encoded = encoded[76:]
		}
		b.WriteString(encoded + "\r\n")
	}
	part("text/plain", msg.Text)
	if msg.HTML != "" {
		part("text/html", msg.HTML)
	}
	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return b.Bytes(), nil
}

func randomHex(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}
