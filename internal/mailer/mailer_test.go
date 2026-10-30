package mailer

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/mail"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/mt-sense/backend-service/internal/config"
)

func parse(t *testing.T, raw []byte) (*mail.Message, map[string]string) {
	t.Helper()
	msg, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("message does not parse: %v", err)
	}
	_, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	parts := map[string]string{}
	reader := multipart.NewReader(msg.Body, params["boundary"])
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		var src io.Reader = part
		if strings.EqualFold(part.Header.Get("Content-Transfer-Encoding"), "base64") {
			src = base64.NewDecoder(base64.StdEncoding, part)
		}
		body, _ := io.ReadAll(src)
		mediaType, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
		parts[mediaType] = string(body)
	}
	return msg, parts
}

func TestBuildEncodesThaiAndBlocksHeaderInjection(t *testing.T) {
	raw, err := build(
		mail.Address{Name: "MT-Sense", Address: "noreply@example.com"},
		Message{
			ToEmail: "hr@example.com",
			ToName:  "สมชาย\r\nBcc: attacker@example.com",
			Subject: "แบบสำรวจเปิดแล้ว\nBcc: attacker@example.com",
			Text:    "สวัสดีครับ",
			HTML:    "<p>สวัสดีครับ</p>",
		},
		time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatal(err)
	}
	msg, parts := parse(t, raw)

	if bcc := msg.Header.Get("Bcc"); bcc != "" {
		t.Fatalf("header injection produced Bcc: %q", bcc)
	}
	subject, err := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if err != nil || !strings.HasPrefix(subject, "แบบสำรวจเปิดแล้ว") {
		t.Fatalf("subject = %q, %v", subject, err)
	}
	to, err := msg.Header.AddressList("To")
	if err != nil || len(to) != 1 || to[0].Address != "hr@example.com" {
		t.Fatalf("to = %v, %v", to, err)
	}
	if parts["text/plain"] != "สวัสดีครับ" || parts["text/html"] != "<p>สวัสดีครับ</p>" {
		t.Fatalf("parts = %v", parts)
	}
	if !strings.HasSuffix(msg.Header.Get("Message-ID"), "@example.com>") {
		t.Fatalf("message-id = %q", msg.Header.Get("Message-ID"))
	}
}

func TestBuildRejectsInvalidRecipient(t *testing.T) {
	_, err := build(mail.Address{Address: "a@example.com"}, Message{ToEmail: "x@y.com, z@w.com"}, time.Now())
	if err == nil {
		t.Fatal("expected an error for a recipient list")
	}
}

func TestClassify(t *testing.T) {
	if !errors.As(classify("RCPT TO", &textproto.Error{Code: 550, Msg: "no such user"}), new(*PermanentError)) {
		t.Error("550 should be permanent")
	}
	if errors.As(classify("RCPT TO", &textproto.Error{Code: 451, Msg: "try later"}), new(*PermanentError)) {
		t.Error("451 should be retryable")
	}
}

func TestRetryDelay(t *testing.T) {
	if retryDelay(1) != time.Minute || retryDelay(99) != retryDelays[len(retryDelays)-1] {
		t.Fatal("unexpected retry schedule")
	}
}

func TestSummaryEscapesNoteAndHidesSuppressedScores(t *testing.T) {
	r, err := renderSummary("https://app.example.com", "/dashboard", "Exec", "ACME", "HR",
		`<script>alert(1)</script>`, SummaryMetrics{Month: 9, Year: 2026, Respondents: 3, Headcount: 40, Suppressed: true, Satisfaction: 4.5})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(r.HTML, "<script>") {
		t.Fatal("note was not HTML-escaped")
	}
	if strings.Contains(r.HTML, "4.50") || strings.Contains(r.Text, "4.50") {
		t.Fatal("suppressed summary leaked a score")
	}
	if !strings.Contains(r.HTML, "https://app.example.com/dashboard") {
		t.Fatal("dashboard link missing")
	}
}

func TestNewRoundRender(t *testing.T) {
	r, err := renderNewRound("https://app.example.com", "Jane", "ACME", 10, 2026, time.Date(2026, 10, 31, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Subject, "10/2026") || !strings.Contains(r.Text, "31/10/2026 17:00") ||
		!strings.Contains(r.HTML, "https://app.example.com/survey") {
		t.Fatalf("unexpected render: %q\n%s", r.Subject, r.Text)
	}
}

func TestSummaryUnavailableMetricsAreExplicit(t *testing.T) {
	r, err := renderSummary("https://app.example.com", "/dashboard", "Exec", "ACME", "HR", "",
		SummaryMetrics{Month: 10, Year: 2026, Respondents: 5, Satisfaction: 4})
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{r.HTML, r.Text} {
		if !strings.Contains(body, "Response rate unavailable") || !strings.Contains(body, "Not enough data to display") {
			t.Fatal("missing metric explanation")
		}
		if strings.Contains(body, "5 / 0") || strings.Contains(body, "positive 0%") {
			t.Fatal("unavailable metric rendered as a measured zero")
		}
	}
}

// fakeSMTP is a minimal plaintext SMTP server that records one message.
func fakeSMTP(t *testing.T, rcptCode int) (port int, received <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	out := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		tp := textproto.NewConn(conn)
		_ = tp.PrintfLine("220 fake ESMTP")
		for {
			line, err := tp.ReadLine()
			if err != nil {
				return
			}
			cmd := strings.ToUpper(strings.SplitN(line, " ", 2)[0])
			switch cmd {
			case "EHLO", "HELO":
				_ = tp.PrintfLine("250 fake")
			case "MAIL":
				_ = tp.PrintfLine("250 ok")
			case "RCPT":
				_ = tp.PrintfLine("%d rcpt", rcptCode)
			case "DATA":
				_ = tp.PrintfLine("354 go")
				data, _ := tp.ReadDotBytes()
				out <- string(data)
				_ = tp.PrintfLine("250 queued")
			case "QUIT":
				_ = tp.PrintfLine("221 bye")
				return
			default:
				_ = tp.PrintfLine("250 ok")
			}
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port, out
}

func TestSMTPSenderDelivers(t *testing.T) {
	port, received := fakeSMTP(t, 250)
	s := NewSender(config.SMTPConfig{Host: "127.0.0.1", Port: port, From: "noreply@example.com", FromName: "MT-Sense", Security: "none"})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Send(ctx, Message{ToEmail: "hr@example.com", Subject: "ทดสอบ", Text: "สวัสดี"}); err != nil {
		t.Fatal(err)
	}
	select {
	case raw := <-received:
		_, parts := parse(t, []byte(raw))
		if parts["text/plain"] != "สวัสดี" {
			t.Fatalf("server received %q", parts)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server received nothing")
	}
}

func TestSMTPSenderRejectedRecipientIsPermanent(t *testing.T) {
	port, _ := fakeSMTP(t, 550)
	s := NewSender(config.SMTPConfig{Host: "127.0.0.1", Port: port, From: "noreply@example.com", Security: "none"})
	err := s.Send(context.Background(), Message{ToEmail: "gone@example.com", Subject: "x", Text: "x"})
	if !errors.As(err, new(*PermanentError)) {
		t.Fatalf("err = %v, want permanent", err)
	}
}
