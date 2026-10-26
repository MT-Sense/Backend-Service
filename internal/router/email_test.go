package router

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/mt-sense/backend-service/internal/auth"
	"github.com/mt-sense/backend-service/internal/config"
	"github.com/mt-sense/backend-service/internal/database"
	"github.com/mt-sense/backend-service/internal/mailer"
	"github.com/mt-sense/backend-service/internal/models"
)

type recordingSender struct {
	mu   sync.Mutex
	sent []mailer.Message
	fail map[string]error
}

func (s *recordingSender) Send(_ context.Context, msg mailer.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.fail[msg.ToEmail]; err != nil {
		return err
	}
	s.sent = append(s.sent, msg)
	return nil
}
func (s *recordingSender) Configured() bool { return true }
func (s *recordingSender) From() string     { return "noreply@example.test" }

func TestEmailWorkflow(t *testing.T) {
	dsn := os.Getenv("EMAIL_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set EMAIL_TEST_DATABASE_URL to an isolated PostgreSQL database")
	}
	db, err := database.Connect(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err = database.Migrate(db); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{JWTSecret: strings.Repeat("a", 32), AccessTTL: time.Hour, RefreshTTL: time.Hour, AppBaseURL: "https://app.example.test"}
	app := fiber.New()
	Register(app, db, cfg)
	issuer := auth.NewIssuer(cfg.JWTSecret, time.Hour, time.Hour)

	org, otherOrg := uuid.NewString(), uuid.NewString()
	for _, id := range []string{org, otherOrg} {
		if err := db.Create(&models.Organization{ID: id, Name: "Email Co " + id[:4], Slug: id, JoinCode: id[:6]}).Error; err != nil {
			t.Fatal(err)
		}
	}
	type member struct {
		org    string
		role   models.Role
		notify bool
		active bool
	}
	ids := map[string]string{}
	for name, m := range map[string]member{
		"admin":    {org, models.RoleAdmin, true, true},
		"exec":     {org, models.RoleExecutive, false, true},
		"emp":      {org, models.RoleEmployee, true, true},
		"optout":   {org, models.RoleEmployee, false, true},
		"inactive": {org, models.RoleEmployee, true, false},
		"outsider": {otherOrg, models.RoleExecutive, true, true},
	} {
		id := uuid.NewString()
		ids[name] = id
		u := models.User{ID: id, OrgID: m.org, Email: name + "-" + id[:8] + "@example.test", PasswordHash: "x", Role: m.role,
			FullName: name, NotifyNewRound: m.notify, NotifyMonthlySummary: true, LastLoginAt: time.Now()}
		if err := db.Create(&u).Error; err != nil {
			t.Fatal(err)
		}
		// Create skips zero-valued fields that have a gorm default, so set booleans explicitly.
		if err := db.Model(&u).Updates(map[string]any{"notify_new_round": m.notify, "is_active": m.active}).Error; err != nil {
			t.Fatal(err)
		}
	}
	token := func(name string, role models.Role) string {
		tok, _, _ := issuer.AccessToken(ids[name], org, role)
		return tok
	}
	adminTok, execTok, empTok := token("admin", models.RoleAdmin), token("exec", models.RoleExecutive), token("emp", models.RoleEmployee)

	call := func(method, path, tok string, body any) (int, []byte) {
		t.Helper()
		var reader io.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			reader = bytes.NewReader(raw)
		}
		req := httptest.NewRequest(method, path, reader)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := app.Test(req, -1)
		if err != nil {
			t.Fatal(err)
		}
		out, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, out
	}
	outbox := func(kind string) []models.EmailOutbox {
		var rows []models.EmailOutbox
		if err := db.Where("org_id = ? AND kind = ?", org, kind).Order("to_email").Find(&rows).Error; err != nil {
			t.Fatal(err)
		}
		return rows
	}

	// Opening a round queues one email per active, opted-in member of this org only.
	status, body := call("POST", "/api/survey-periods", adminTok, map[string]any{"month": 10, "year": 2026})
	if status != 201 {
		t.Fatalf("create period: %d %s", status, body)
	}
	rows := outbox(mailer.KindNewRound)
	if len(rows) != 2 {
		t.Fatalf("new round emails = %d, want 2 (admin + emp)", len(rows))
	}
	for _, r := range rows {
		if !strings.HasPrefix(r.ToEmail, "admin-") && !strings.HasPrefix(r.ToEmail, "emp-") {
			t.Errorf("unexpected new-round recipient %s", r.ToEmail)
		}
		if !strings.Contains(r.HTMLBody, "https://app.example.test/survey") {
			t.Error("survey link missing")
		}
	}

	// A future round is held until it opens.
	opens := time.Now().Add(72 * time.Hour)
	status, body = call("POST", "/api/survey-periods", adminTok, map[string]any{"month": 11, "year": 2026, "opensAt": opens, "closesAt": opens.Add(240 * time.Hour)})
	if status != 201 {
		t.Fatalf("create future period: %d %s", status, body)
	}
	var future int64
	db.Model(&models.EmailOutbox{}).Where("org_id = ? AND kind = ? AND send_after > ?", org, mailer.KindNewRound, time.Now().Add(48*time.Hour)).Count(&future)
	if future != 2 {
		t.Fatalf("deferred new-round emails = %d, want 2", future)
	}

	// Recipients: active leaders of the caller's org only. Employees are refused.
	status, body = call("GET", "/api/email/recipients", execTok, nil)
	var recipients []map[string]any
	_ = json.Unmarshal(body, &recipients)
	if status != 200 || len(recipients) != 2 {
		t.Fatalf("recipients: %d %s", status, body)
	}
	if status, _ = call("GET", "/api/email/recipients", empTok, nil); status != 403 {
		t.Fatalf("employee recipients status = %d", status)
	}

	// Summary: outsiders and employees cannot be targeted; a valid send queues per leader.
	for _, target := range []string{"outsider", "emp"} {
		status, body = call("POST", "/api/email/summary", execTok, map[string]any{"recipientIds": []string{ids[target]}})
		if status != 400 {
			t.Fatalf("summary to %s: %d %s", target, status, body)
		}
	}
	status, body = call("POST", "/api/email/summary", execTok, map[string]any{
		"recipientIds": []string{ids["admin"], ids["exec"], ids["admin"]}, "note": "<b>please read</b>",
	})
	if status != 202 || !strings.Contains(string(body), `"queued":2`) {
		t.Fatalf("summary: %d %s", status, body)
	}
	summaries := outbox(mailer.KindSummary)
	if len(summaries) != 2 {
		t.Fatalf("summary rows = %d", len(summaries))
	}
	for _, s := range summaries {
		if strings.Contains(s.HTMLBody, "<b>please read</b>") {
			t.Error("note not escaped")
		}
		// No responses yet, so the scores must be withheld.
		if !strings.Contains(s.TextBody, "Fewer than 5 responses") {
			t.Error("expected suppressed summary")
		}
	}

	// Status and test are admin-only; the log sender reports "not configured".
	if status, _ = call("GET", "/api/email/status", execTok, nil); status != 403 {
		t.Fatalf("exec status = %d", status)
	}
	status, body = call("GET", "/api/email/status", adminTok, nil)
	if status != 200 || !strings.Contains(string(body), `"configured":false`) || !strings.Contains(string(body), `"pending":6`) {
		t.Fatalf("status: %d %s", status, body)
	}
	if status, body = call("POST", "/api/email/test", adminTok, nil); status != 200 {
		t.Fatalf("test email: %d %s", status, body)
	}

	// Worker: due rows are delivered; a permanent rejection fails at once; a transient error
	// is rescheduled; deferred rows stay untouched.
	var empEmail string
	db.Model(&models.User{}).Where("id = ?", ids["emp"]).Pluck("email", &empEmail)
	sender := &recordingSender{fail: map[string]error{empEmail: &mailer.PermanentError{Err: errors.New("550 no such user")}}}
	var adminEmail string
	db.Model(&models.User{}).Where("id = ?", ids["admin"]).Pluck("email", &adminEmail)
	worker := mailer.New(db, sender, cfg.AppBaseURL)
	ctx, cancel := context.WithCancel(context.Background())
	worker.Start(ctx)
	deadline := time.Now().Add(10 * time.Second)
	for {
		var due int64
		db.Model(&models.EmailOutbox{}).Where("org_id = ? AND status IN ? AND send_after <= NOW()", org, []string{"pending", "sending"}).Count(&due)
		if due == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("worker left %d due emails", due)
		}
		time.Sleep(100 * time.Millisecond)
	}
	cancel()

	count := func(where string, args ...any) int64 {
		var n int64
		db.Model(&models.EmailOutbox{}).Where("org_id = ?", org).Where(where, args...).Count(&n)
		return n
	}
	if n := count("status = 'sent'"); n != 3 { // admin new round + 2 summaries
		t.Fatalf("sent = %d, want 3", n)
	}
	if n := count("status = 'failed' AND to_email = ?", empEmail); n != 1 { // emp's due round email
		t.Fatalf("failed = %d, want 1", n)
	}
	if n := count("status = 'pending' AND send_after > NOW()"); n != 2 {
		t.Fatalf("deferred = %d, want 2", n)
	}
	// The worker drains every org's due mail, so count only this test's recipients (a reused
	// test database may still hold rows from an earlier run).
	ours := 0
	for _, m := range sender.sent {
		if strings.HasSuffix(m.ToEmail, "@example.test") && (strings.Contains(m.ToEmail, ids["admin"][:8]) || strings.Contains(m.ToEmail, ids["exec"][:8])) {
			ours++
		}
	}
	if ours != 3 {
		t.Fatalf("sender saw %d of this test's messages", ours)
	}

	// Transient failure: rescheduled with an attempt counted, not failed.
	sender.fail = map[string]error{adminEmail: errors.New("421 try later")}
	worker.Enqueue(db, []models.EmailOutbox{{OrgID: org, Kind: "retry_probe", ToEmail: adminEmail, Subject: "x", TextBody: "x"}})
	ctx, cancel = context.WithCancel(context.Background())
	worker.Start(ctx)
	deadline = time.Now().Add(10 * time.Second)
	var probe models.EmailOutbox
	for {
		db.Where("org_id = ? AND kind = 'retry_probe'", org).First(&probe)
		if probe.Attempts == 1 && probe.Status == models.EmailPending {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("probe = %+v", probe)
		}
		time.Sleep(100 * time.Millisecond)
	}
	cancel()
	if !probe.SendAfter.After(time.Now()) || !strings.Contains(probe.LastError, "421") {
		t.Fatalf("retry not scheduled: %+v", probe)
	}
}
