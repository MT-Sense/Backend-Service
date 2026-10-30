package router

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/mt-sense/backend-service/internal/auth"
	"github.com/mt-sense/backend-service/internal/config"
	"github.com/mt-sense/backend-service/internal/database"
	"github.com/mt-sense/backend-service/internal/models"
)

func TestAssistantPagination(t *testing.T) {
	dsn := os.Getenv("AUTOMATION_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set AUTOMATION_TEST_DATABASE_URL to an isolated PostgreSQL database")
	}
	db, err := database.Connect(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(db); err != nil {
		t.Fatal(err)
	}
	org, foreign, empty := uuid.NewString(), uuid.NewString(), uuid.NewString()
	mustCreate := func(value any) {
		t.Helper()
		if err := db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, model := range []any{&models.AutomationProposal{}, &models.RoutingCase{}} {
			if err := db.Where("org_id IN ?", []string{org, foreign, empty}).Delete(model).Error; err != nil {
				t.Error(err)
			}
		}
		if err := db.Where("id IN ?", []string{org, foreign, empty}).Delete(&models.Organization{}).Error; err != nil {
			t.Error(err)
		}
	})
	for _, id := range []string{org, foreign, empty} {
		mustCreate(&models.Organization{ID: id, Name: "Assistant pagination", Slug: id, JoinCode: id[:6]})
	}
	timestamp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 105; i++ {
		id := fmt.Sprintf("%s-%03d", org, i)
		proposalStatus, caseStatus := "completed", "completed"
		if i < 5 {
			proposalStatus, caseStatus = "pending", "triage"
		}
		mustCreate(&models.AutomationProposal{ID: id, OrgID: org, ScopeKey: id, Status: proposalStatus, CreatedAt: timestamp, MissingData: []string{" Check ", "Check", ""}})
		mustCreate(&models.RoutingCase{ID: id, OrgID: org, Title: id, Status: caseStatus, CreatedAt: timestamp, History: []models.RoutingHistory{}})
	}
	for _, id := range []string{org, foreign} {
		scope := uuid.NewString()
		mustCreate(&models.AutomationProposal{ID: scope, OrgID: id, ScopeKey: scope, Status: "pending", Demo: id == org})
		title := "Foreign case"
		if id == org {
			title = "[ตัวอย่าง] demo"
		}
		mustCreate(&models.RoutingCase{ID: uuid.NewString(), OrgID: id, Title: title, Status: "triage"})
	}
	cfg := &config.Config{JWTSecret: strings.Repeat("p", 32), AccessTTL: time.Hour, RefreshTTL: time.Hour}
	app := fiber.New()
	Register(app, db, cfg)
	issuer := auth.NewIssuer(cfg.JWTSecret, time.Hour, time.Hour)
	admin, _, _ := issuer.AccessToken("admin", org, models.RoleAdmin)
	employee, _, _ := issuer.AccessToken("employee", org, models.RoleEmployee)
	executive, _, _ := issuer.AccessToken("executive", org, models.RoleExecutive)
	emptyAdmin, _, _ := issuer.AccessToken("empty", empty, models.RoleAdmin)
	call := func(path, token string, want int) []byte {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/hr/automation"+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := app.Test(req, -1)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != want {
			t.Fatalf("%s: status=%d want=%d: %s", path, res.StatusCode, want, body)
		}
		return body
	}
	type item struct {
		ID          string
		MissingData []string
	}
	type pageResult struct {
		Items          []item
		Total          int64
		Page, PageSize int
		Stats          struct{ Total, Pending, Active int64 }
	}
	getPage := func(path, token string) pageResult {
		t.Helper()
		var result pageResult
		if err := json.Unmarshal(call(path, token, 200), &result); err != nil {
			t.Fatal(err)
		}
		return result
	}
	for _, endpoint := range []string{"/proposals", "/cases"} {
		t.Run(endpoint, func(t *testing.T) {
			call(endpoint+"?page=1", "", 401)
			call(endpoint+"?page=1", employee, 403)
			call(endpoint+"?page=1", executive, 403)
			for _, query := range []string{"?page=0", "?page=x", "?pageSize=0", "?pageSize=101", "?page=1&status=unknown"} {
				call(endpoint+query, admin, 400)
			}
			first := getPage(endpoint+"?page=1&pageSize=5", admin)
			second := getPage(endpoint+"?page=2&pageSize=5", admin)
			if first.Total != 105 || len(first.Items) != 5 || first.Items[0].ID != org+"-104" || second.Items[0].ID != org+"-099" {
				t.Fatalf("unstable order or wrong count: first=%+v second=%+v", first, second)
			}
			last := getPage(endpoint+"?page=999&pageSize=5", admin)
			if last.Page != 21 || len(last.Items) != 5 || last.Items[4].ID != org+"-000" {
				t.Fatalf("cannot reach records beyond 100: %+v", last)
			}
			status := "triage"
			if endpoint == "/proposals" {
				status = "pending"
			}
			filtered := getPage(endpoint+"?page=9&pageSize=5&status="+status, admin)
			if filtered.Page != 1 || filtered.Total != 5 || len(filtered.Items) != 5 || filtered.Items[0].ID != org+"-004" {
				t.Fatalf("filter must run before pagination: %+v", filtered)
			}
			if endpoint == "/proposals" {
				if filtered.Stats.Total != 105 || filtered.Stats.Pending != 5 || filtered.Stats.Active != 0 {
					t.Fatalf("stats must cover all statuses: %+v", filtered.Stats)
				}
				if len(filtered.Items[0].MissingData) != 1 || filtered.Items[0].MissingData[0] != "Check" {
					t.Fatal("missingData no longer normalized")
				}
			}
			zero := getPage(endpoint+"?page=9", emptyAdmin)
			if zero.Page != 1 || zero.Total != 0 || zero.Items == nil || len(zero.Items) != 0 {
				t.Fatalf("invalid empty page: %+v", zero)
			}
			var legacy []item
			if err := json.Unmarshal(call(endpoint, admin, 200), &legacy); err != nil {
				t.Fatal(err)
			}
			if len(legacy) != 100 {
				t.Fatal("legacy array API changed", len(legacy))
			}
		})
	}
	// A status transition can empty the final page. Refetch clamps to the remaining page.
	if err := db.Model(&models.AutomationProposal{}).Where("org_id = ? AND status = ?", org, "pending").Update("status", "approved").Error; err != nil {
		t.Fatal(err)
	}
	after := getPage("/proposals?page=2&pageSize=5&status=pending", admin)
	if after.Page != 1 || after.Total != 0 || after.Stats.Pending != 0 {
		t.Fatalf("stale totals after transition: %+v", after)
	}
}
