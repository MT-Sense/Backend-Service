package router

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strings"
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

func TestUserManagementAndPasswords(t *testing.T) {
	dsn := os.Getenv("FEED_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set FEED_TEST_DATABASE_URL to an isolated PostgreSQL database")
	}
	db, err := database.Connect(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err = database.Migrate(db); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{JWTSecret: strings.Repeat("d", 32), AccessTTL: time.Hour, RefreshTTL: time.Hour, AppBaseURL: "https://app.example.test"}
	app := fiber.New()
	Register(app, db, cfg)
	issuer := auth.NewIssuer(cfg.JWTSecret, time.Hour, time.Hour)

	org := uuid.NewString()
	if err := db.Create(&models.Organization{ID: org, Name: "Users Co", Slug: org, JoinCode: org[:6]}).Error; err != nil {
		t.Fatal(err)
	}
	dept := uuid.NewString()
	if err := db.Create(&models.Department{ID: dept, OrgID: org, Name: "Research " + org[:4]}).Error; err != nil {
		t.Fatal(err)
	}
	otherDept := uuid.NewString()
	otherOrg := uuid.NewString()
	db.Create(&models.Organization{ID: otherOrg, Name: "Other", Slug: otherOrg, JoinCode: otherOrg[:6]})
	db.Create(&models.Department{ID: otherDept, OrgID: otherOrg, Name: "Elsewhere " + otherOrg[:4]})

	hrID := uuid.NewString()
	suffix := org[:8]
	if err := db.Create(&models.User{ID: hrID, OrgID: org, Email: "hr-" + suffix + "@example.test", PasswordHash: "x", Role: models.RoleAdmin, FullName: "HR", IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	hrTok, _, _ := issuer.AccessToken(hrID, org, models.RoleAdmin)
	empTok, _, _ := issuer.AccessToken(uuid.NewString(), org, models.RoleEmployee)

	call := func(method, path, token string, body any) (int, map[string]any, []byte) {
		t.Helper()
		var reader io.Reader
		if body != nil {
			raw, _ := json.Marshal(body)
			reader = bytes.NewReader(raw)
		}
		req := httptest.NewRequest(method, path, reader)
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := app.Test(req, -1)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(resp.Body)
		var obj map[string]any
		_ = json.Unmarshal(raw, &obj)
		return resp.StatusCode, obj, raw
	}
	login := func(email, password string) (int, map[string]any) {
		s, body, _ := call("POST", "/api/auth/login", "", map[string]string{"email": email, "password": password})
		return s, body
	}

	// --- create ---
	empEmail := "new-" + suffix + "@example.test"
	if s, _, _ := call("POST", "/api/hr/users", empTok, map[string]any{}); s != 403 {
		t.Fatalf("employee create user = %d", s)
	}
	if s, _, raw := call("POST", "/api/hr/users", hrTok, map[string]any{"fullName": "X", "email": "bad", "password": "short"}); s != 400 || !strings.Contains(string(raw), "email") || !strings.Contains(string(raw), "8 characters") {
		t.Fatalf("invalid create = %d %s", s, raw)
	}
	if s, _, _ := call("POST", "/api/hr/users", hrTok, map[string]any{"fullName": "X", "email": empEmail, "password": "temp-pass-1", "departmentId": otherDept}); s != 400 {
		t.Fatalf("other org's department accepted: %d", s)
	}
	s, created, raw := call("POST", "/api/hr/users", hrTok, map[string]any{
		"fullName": "สมหญิง ใจดี", "email": "  " + strings.ToUpper(empEmail) + " ", "role": "employee",
		"departmentId": dept, "position": "นักวิจัย", "password": "temp-pass-1",
	})
	if s != 201 || created["email"] != empEmail || created["departmentName"] != "Research "+org[:4] || created["positionName"] != "นักวิจัย" || created["lastLoginAt"] != nil {
		t.Fatalf("create = %d %s", s, raw)
	}
	if _, ok := created["passwordHash"]; ok {
		t.Fatal("password hash leaked")
	}
	if s, _, _ := call("POST", "/api/hr/users", hrTok, map[string]any{"fullName": "Dup", "email": empEmail, "password": "temp-pass-1"}); s != 409 {
		t.Fatalf("duplicate email = %d", s)
	}
	empID := created["id"].(string)
	if s, _ := login(empEmail, "temp-pass-1"); s != 200 {
		t.Fatalf("login with temporary password = %d", s)
	}

	// --- list has no survey data ---
	_, _, list := call("GET", "/api/hr/users", hrTok, nil)
	if !strings.Contains(string(list), empEmail) || strings.Contains(string(list), "submitted") || strings.Contains(string(list), "password") {
		t.Fatalf("list = %s", list)
	}

	// --- self and last-admin guards ---
	if s, _, _ := call("PATCH", "/api/hr/users/"+hrID, hrTok, map[string]any{"role": "employee"}); s != 403 {
		t.Fatalf("self demote = %d", s)
	}
	if s, _, _ := call("PATCH", "/api/hr/users/"+hrID, hrTok, map[string]any{"isActive": false}); s != 403 {
		t.Fatalf("self deactivate = %d", s)
	}
	if s, _, _ := call("PATCH", "/api/hr/users/"+empID, hrTok, map[string]any{"role": "admin"}); s != 200 {
		t.Fatalf("promote to HR = %d", s)
	}
	// Two admins now: demoting the new one is fine; then hr is the last admin again.
	if s, _, _ := call("PATCH", "/api/hr/users/"+empID, hrTok, map[string]any{"role": "executive", "departmentId": "", "position": ""}); s != 200 {
		t.Fatalf("demote = %d", s)
	}
	newTok, _, _ := issuer.AccessToken(empID, org, models.RoleAdmin) // pretend the new HR acts
	if s, _, _ := call("PATCH", "/api/hr/users/"+hrID, newTok, map[string]any{"isActive": false}); s != 409 {
		t.Fatalf("deactivating the last HR = %d", s)
	}

	// --- deactivate blocks login and refresh ---
	_, sess := login(empEmail, "temp-pass-1")
	refresh := sess["refreshToken"].(string)
	if s, _, _ := call("PATCH", "/api/hr/users/"+empID, hrTok, map[string]any{"isActive": false}); s != 200 {
		t.Fatalf("deactivate = %d", s)
	}
	if s, _ := login(empEmail, "temp-pass-1"); s != 401 {
		t.Fatalf("deactivated login = %d", s)
	}
	if s, _, _ := call("POST", "/api/auth/refresh", "", map[string]string{"refreshToken": refresh}); s != 401 {
		t.Fatalf("deactivated refresh = %d", s)
	}
	if s, _, _ := call("POST", "/api/hr/users/"+empID+"/password-reset-email", hrTok, nil); s != 409 {
		t.Fatalf("reset email to inactive account = %d", s)
	}
	call("PATCH", "/api/hr/users/"+empID, hrTok, map[string]any{"isActive": true})

	// --- HR temporary password ---
	if s, _, _ := call("POST", "/api/hr/users/"+empID+"/password", hrTok, map[string]string{"password": "temp-pass-2"}); s != 204 {
		t.Fatalf("set temp password = %d", s)
	}
	if s, _ := login(empEmail, "temp-pass-2"); s != 200 {
		t.Fatalf("login with new temp password = %d", s)
	}

	// --- change own password ---
	_, sess = login(empEmail, "temp-pass-2")
	selfTok := sess["accessToken"].(string)
	if s, _, raw := call("POST", "/api/settings/me/password", selfTok, map[string]string{"currentPassword": "wrong-pass", "newPassword": "my-own-pass"}); s != 400 || !strings.Contains(string(raw), "incorrect") {
		t.Fatalf("wrong current password = %d %s", s, raw)
	}
	if s, body, _ := call("POST", "/api/settings/me/password", selfTok, map[string]string{"currentPassword": "temp-pass-2", "newPassword": "my-own-pass"}); s != 200 || body["accessToken"] == nil {
		t.Fatalf("change password = %d", s)
	}
	if s, _ := login(empEmail, "my-own-pass"); s != 200 {
		t.Fatalf("login after change = %d", s)
	}

	// --- forgot / reset by email ---
	if s, _, _ := call("POST", "/api/auth/forgot-password", "", map[string]string{"email": "nobody-" + suffix + "@example.test"}); s != 204 {
		t.Fatalf("forgot for unknown email = %d (must not reveal)", s)
	}
	tokenFromOutbox := func() string {
		t.Helper()
		var row models.EmailOutbox
		if err := db.Where("org_id = ? AND kind = ?", org, mailer.KindPasswordReset).Order("created_at DESC").First(&row).Error; err != nil {
			t.Fatal(err)
		}
		m := regexp.MustCompile(`reset-password\?token=([A-Za-z0-9_%-]+)`).FindStringSubmatch(row.TextBody)
		if m == nil || !strings.HasPrefix(row.TextBody, "สวัสดี") {
			t.Fatalf("reset email body = %q", row.TextBody)
		}
		token, _ := url.QueryUnescape(m[1])
		return token
	}
	if s, _, _ := call("POST", "/api/auth/forgot-password", "", map[string]string{"email": strings.ToUpper(empEmail)}); s != 204 {
		t.Fatalf("forgot = %d", s)
	}
	first := tokenFromOutbox()
	// HR-triggered link supersedes the first one.
	if s, _, _ := call("POST", "/api/hr/users/"+empID+"/password-reset-email", hrTok, nil); s != 202 {
		t.Fatalf("HR reset email = %d", s)
	}
	second := tokenFromOutbox()
	if first == second {
		t.Fatal("expected a fresh token")
	}
	if s, _, _ := call("POST", "/api/auth/reset-password", "", map[string]string{"token": first, "newPassword": "reset-pass-1"}); s != 400 {
		t.Fatalf("superseded token accepted: %d", s)
	}
	if s, _, raw := call("POST", "/api/auth/reset-password", "", map[string]string{"token": second, "newPassword": "reset-pass-1"}); s != 204 {
		t.Fatalf("reset = %d %s", s, raw)
	}
	if s, _, _ := call("POST", "/api/auth/reset-password", "", map[string]string{"token": second, "newPassword": "reset-pass-2"}); s != 400 {
		t.Fatalf("token reused: %d", s)
	}
	if s, _ := login(empEmail, "reset-pass-1"); s != 200 {
		t.Fatalf("login after reset = %d", s)
	}
	if s, _ := login(empEmail, "my-own-pass"); s != 401 {
		t.Fatalf("old password still works: %d", s)
	}
	var stored int64
	db.Model(&models.PasswordResetToken{}).Where("token_hash = ?", second).Count(&stored)
	if stored != 0 {
		t.Fatal("plaintext reset token stored")
	}

	// Once delivered, the reset email body (which holds a live link) is wiped.
	worker := mailer.New(db, &recordingSender{}, cfg.AppBaseURL)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	worker.Start(ctx)
	deadline := time.Now().Add(10 * time.Second)
	for {
		var pending int64
		db.Model(&models.EmailOutbox{}).Where("org_id = ? AND kind = ? AND status IN ?", org, mailer.KindPasswordReset, []string{"pending", "sending"}).Count(&pending)
		if pending == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("reset emails were not delivered")
		}
		time.Sleep(100 * time.Millisecond)
	}
	var leaked int64
	db.Model(&models.EmailOutbox{}).Where("org_id = ? AND kind = ? AND (text_body LIKE ? OR html_body LIKE ?)", org, mailer.KindPasswordReset, "%token=%", "%token=%").Count(&leaked)
	if leaked != 0 {
		t.Fatalf("%d delivered reset emails still contain the link", leaked)
	}
}
