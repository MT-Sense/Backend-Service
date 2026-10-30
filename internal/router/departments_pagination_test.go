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

func TestDepartmentPagination(t *testing.T) {
	dsn := os.Getenv("DEPARTMENTS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set DEPARTMENTS_TEST_DATABASE_URL to an isolated PostgreSQL database")
	}
	db, err := database.Connect(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(db); err != nil {
		t.Fatal(err)
	}
	org, foreign, empty := uuid.NewString(), uuid.NewString(), uuid.NewString()
	t.Cleanup(func() {
		if err := db.Where("org_id IN ?", []string{org, foreign, empty}).Delete(&models.Department{}).Error; err != nil {
			t.Error(err)
		}
		if err := db.Where("id IN ?", []string{org, foreign, empty}).Delete(&models.Organization{}).Error; err != nil {
			t.Error(err)
		}
	})
	mustCreate := func(value any) {
		t.Helper()
		if err := db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{org, foreign, empty} {
		mustCreate(&models.Organization{ID: id, Name: "Department pagination", Slug: id, JoinCode: id[:6]})
	}
	ids := make([]string, 11)
	timestamp := time.Date(2024, 2, 10, 9, 0, 0, 0, time.UTC)
	for index := range ids {
		ids[index] = uuid.NewString()
		mustCreate(&models.Department{ID: ids[index], OrgID: org, Name: fmt.Sprintf("Department %02d", index), CreatedAt: timestamp})
	}
	mustCreate(&models.Department{ID: uuid.NewString(), OrgID: foreign, Name: "AAA foreign"})
	cfg := &config.Config{JWTSecret: strings.Repeat("d", 32), AccessTTL: time.Hour, RefreshTTL: time.Hour}
	app := fiber.New()
	Register(app, db, cfg)
	issuer := auth.NewIssuer(cfg.JWTSecret, time.Hour, time.Hour)
	admin, _, _ := issuer.AccessToken("admin", org, models.RoleAdmin)
	employee, _, _ := issuer.AccessToken("employee", org, models.RoleEmployee)
	emptyAdmin, _, _ := issuer.AccessToken("empty", empty, models.RoleAdmin)
	call := func(method, path, token string, want int) []byte {
		t.Helper()
		req := httptest.NewRequest(method, "/api/hr/departments"+path, nil)
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
			t.Fatalf("status=%d want=%d: %s", res.StatusCode, want, body)
		}
		return body
	}
	type departmentItem struct {
		ID        string
		CreatedAt *time.Time
	}
	type pageResult struct {
		Items          []departmentItem
		Total          int64
		Page, PageSize int
	}
	getPage := func(query, token string) pageResult {
		t.Helper()
		var page pageResult
		if err := json.Unmarshal(call("GET", query, token, 200), &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	call("GET", "?page=1", "", 401)
	call("GET", "?page=1", employee, 403)
	for _, query := range []string{"?page=0", "?page=x", "?pageSize=0", "?pageSize=101"} {
		call("GET", query, admin, 400)
	}
	first := getPage("?page=1&pageSize=5", admin)
	second := getPage("?page=2&pageSize=5", admin)
	if first.Total != 11 || len(first.Items) != 5 || first.Items[0].ID != ids[0] ||
		first.Items[0].CreatedAt == nil || !first.Items[0].CreatedAt.Equal(timestamp) {
		t.Fatalf("wrong first page or timestamp: %+v", first)
	}
	if second.Page != 2 || second.PageSize != 5 || len(second.Items) != 5 || second.Items[0].ID != ids[5] {
		t.Fatalf("wrong second page: %+v", second)
	}
	last := getPage("?page=99&pageSize=5", admin)
	if last.Page != 3 || len(last.Items) != 1 || last.Items[0].ID != ids[10] {
		t.Fatalf("wrong last page: %+v", last)
	}
	call("DELETE", "/"+ids[10], admin, 204)
	afterDelete := getPage("?page=3&pageSize=5", admin)
	if afterDelete.Page != 2 || afterDelete.Total != 10 || len(afterDelete.Items) != 5 {
		t.Fatalf("page not clamped: %+v", afterDelete)
	}
	zero := getPage("?page=99&pageSize=5", emptyAdmin)
	if zero.Page != 1 || zero.Total != 0 || zero.Items == nil || len(zero.Items) != 0 {
		t.Fatalf("wrong empty page: %+v", zero)
	}
	var legacy []departmentItem
	if err := json.Unmarshal(call("GET", "", admin, 200), &legacy); err != nil {
		t.Fatal(err)
	}
	if len(legacy) != 10 {
		t.Fatalf("legacy API changed: %d", len(legacy))
	}
}
