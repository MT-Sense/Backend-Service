package router

import (
	"encoding/json"
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
	"github.com/mt-sense/backend-service/internal/dto"
	"github.com/mt-sense/backend-service/internal/models"
)

func TestSurveyPeriodPagination(t *testing.T) {
	dsn := os.Getenv("PERIODS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set PERIODS_TEST_DATABASE_URL to an isolated PostgreSQL database")
	}
	db, err := database.Connect(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(db); err != nil {
		t.Fatal(err)
	}
	tx := db
	mustCreate := func(value any) {
		t.Helper()
		if err := tx.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	org, foreign, empty := uuid.NewString(), uuid.NewString(), uuid.NewString()
	t.Cleanup(func() {
		for _, model := range []any{&models.SurveyResponse{}, &models.SurveyPeriod{}} {
			if err := db.Where("org_id IN ?", []string{org, foreign, empty}).Delete(model).Error; err != nil {
				t.Error(err)
			}
		}
		if err := db.Where("id IN ?", []string{org, foreign, empty}).Delete(&models.Organization{}).Error; err != nil {
			t.Error(err)
		}
	})
	for _, id := range []string{org, foreign, empty} {
		mustCreate(&models.Organization{ID: id, Name: "Pagination test", Slug: id, JoinCode: id[:6]})
	}
	ids := make([]string, 13)
	counts := map[string]int64{}
	for index := 0; index < 13; index++ {
		id := uuid.NewString()
		ids[index] = id
		mustCreate(&models.SurveyPeriod{ID: id, OrgID: org, Year: int16(2025 + index/12), Month: int16(index%12 + 1),
			OpensAt: time.Now().Add(-time.Hour), ClosesAt: time.Now().Add(time.Hour)})
		for n := 0; n < index%3; n++ {
			mustCreate(&models.SurveyResponse{ID: uuid.NewString(), OrgID: org, PeriodID: id, SatisfactionScore: 3, SubmittedAt: time.Now()})
			counts[id]++
		}
	}
	mustCreate(&models.SurveyPeriod{ID: uuid.NewString(), OrgID: foreign, Year: 2099, Month: 12,
		OpensAt: time.Now().Add(-time.Hour), ClosesAt: time.Now().Add(time.Hour)})
	cfg := &config.Config{JWTSecret: strings.Repeat("p", 32), AccessTTL: time.Hour, RefreshTTL: time.Hour}
	app := fiber.New()
	Register(app, tx, cfg)
	issuer := auth.NewIssuer(cfg.JWTSecret, time.Hour, time.Hour)
	admin, _, _ := issuer.AccessToken("periods-admin", org, models.RoleAdmin)
	employee, _, _ := issuer.AccessToken("periods-employee", org, models.RoleEmployee)
	emptyAdmin, _, _ := issuer.AccessToken("empty-admin", empty, models.RoleAdmin)
	call := func(query, token string, want int) []byte {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/survey-periods"+query, nil)
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
	call("?page=1&pageSize=5", "", 401)
	call("?page=1&pageSize=5", employee, 403)
	for _, query := range []string{"?page=0", "?page=-1", "?page=x", "?pageSize=101", "?pageSize=0"} {
		call(query, admin, 400)
	}
	decodePage := func(query, token string) dto.SurveyPeriodPage {
		t.Helper()
		var page dto.SurveyPeriodPage
		if err := json.Unmarshal(call(query, token, 200), &page); err != nil {
			t.Fatal(err)
		}
		return page
	}
	first := decodePage("?page=1&pageSize=5", admin)
	second := decodePage("?page=2&pageSize=5", admin)
	if first.Total != 13 || first.Page != 1 || first.PageSize != 5 || len(first.Items) != 5 || first.Items[0].ID != ids[12] {
		t.Fatalf("wrong first page: %+v", first)
	}
	if second.Page != 2 || len(second.Items) != 5 || second.Items[0].ID != ids[7] ||
		second.LatestPeriod == nil || second.LatestPeriod.Year != 2026 || second.LatestPeriod.Month != 1 {
		t.Fatalf("wrong second page or latest month: %+v", second)
	}
	seen := map[string]bool{}
	for _, item := range first.Items {
		seen[item.ID] = true
	}
	for _, item := range second.Items {
		if seen[item.ID] || item.ResponseCount != counts[item.ID] {
			t.Fatalf("overlap or wrong count: %+v", item)
		}
	}
	last := decodePage("?page=999&pageSize=5", admin)
	if last.Page != 3 || len(last.Items) != 3 || last.Total != 13 {
		t.Fatalf("wrong clamped last page: %+v", last)
	}
	zero := decodePage("?page=99&pageSize=5", emptyAdmin)
	if zero.Total != 0 || zero.Page != 1 || zero.Items == nil || len(zero.Items) != 0 || zero.LatestPeriod != nil {
		t.Fatalf("wrong empty page: %+v", zero)
	}
	var legacy []dto.SurveyPeriod
	if err := json.Unmarshal(call("", admin, 200), &legacy); err != nil {
		t.Fatal(err)
	}
	if len(legacy) != 13 {
		t.Fatalf("legacy array changed: %d", len(legacy))
	}
	for _, item := range legacy {
		if item.ResponseCount != counts[item.ID] {
			t.Fatalf("legacy response count changed: %+v", item)
		}
	}
}
