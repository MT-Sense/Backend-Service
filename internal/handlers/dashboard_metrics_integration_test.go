package handlers

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/mt-sense/backend-service/internal/analytics"
	"github.com/mt-sense/backend-service/internal/database"
	"github.com/mt-sense/backend-service/internal/dto"
	"github.com/mt-sense/backend-service/internal/models"
	"gorm.io/gorm"
)

// Run only against an isolated PostgreSQL database; never uses the application's DSN.
func TestDashboardMetricPrivacyAndAudience(t *testing.T) {
	dsn := os.Getenv("METRICS_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set METRICS_TEST_DATABASE_URL to an isolated PostgreSQL database")
	}
	db, err := database.Connect(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err = database.Migrate(db); err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	defer sqlDB.Close()
	create := func(value any) {
		t.Helper()
		if err := db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	orgID, departmentID, topicID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	create(&models.Organization{ID: orgID, Name: "Metric test", Slug: orgID, JoinCode: orgID[:6]})
	create(&models.Department{ID: departmentID, OrgID: orgID, Name: "Team"})
	topic := models.Topic{ID: topicID, Label: models.Localized{TH: "Test", EN: "Test"}}
	create(&topic)
	for i := 0; i < 8; i++ {
		id := uuid.NewString()
		create(&models.User{ID: id, OrgID: orgID, Email: id + "@example.invalid", PasswordHash: "test", FullName: "Test", DepartmentID: &departmentID, IsActive: true})
	}
	stats := analytics.New(db).WithOrg(orgID)
	newPeriod := func(month, count, analyzed int, capture bool) models.SurveyPeriod {
		t.Helper()
		p := models.SurveyPeriod{ID: uuid.NewString(), OrgID: orgID, Year: 2090, Month: int16(month), OpensAt: time.Now().Add(-time.Hour), ClosesAt: time.Now().Add(time.Hour)}
		if err := db.Transaction(func(tx *gorm.DB) error {
			if capture {
				if err := analytics.CapturePeriodAudience(tx, &p); err != nil {
					return err
				}
			}
			return tx.Create(&p).Error
		}); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < count; i++ {
			id := uuid.NewString()
			create(&models.SurveyResponse{ID: id, OrgID: orgID, PeriodID: p.ID, DepartmentID: &departmentID, SatisfactionScore: 4, AnalysisStatus: "done", SubmittedAt: time.Now()})
			categories := []string{topicID}
			if i == 0 {
				categories = append(categories, "rare-topic")
			}
			if i < analyzed {
				create(&models.ResponseAnalysis{ID: uuid.NewString(), ResponseID: id, SentimentLabel: "pos", Categories: categories, AnalyzedAt: time.Now()})
			}
		}
		return p
	}
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error { c.Locals("orgID", orgID); return c.Next() })
	h := NewDashboardHandler(db, stats, nil)
	app.Get("/kpi", h.HRKpis)
	app.Get("/executive", h.ExecutiveSummary)
	get := func(path string, target any) {
		t.Helper()
		response, err := app.Test(httptest.NewRequest("GET", path, nil), -1)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatalf("status %d for %s", response.StatusCode, path)
		}
		if err := json.NewDecoder(response.Body).Decode(target); err != nil {
			t.Fatal(err)
		}
	}
	for n := 0; n <= 5; n++ {
		p := newPeriod(n+1, n, n, true)
		var k dto.HrKpis
		get("/kpi?period="+p.ID, &k)
		var ex dto.ExecutiveSummary
		get("/executive?period="+p.ID, &ex)
		if n < 5 {
			if k.Enps.Value != nil || k.Satisfaction.Value != nil || k.BurnoutRisk.Percentage != nil || k.Sentiment != nil || ex.Score != nil || ex.Sentiment != nil {
				t.Fatalf("small group %d leaked metrics: %+v", n, k)
			}
			if n > 0 && (k.DataState != "suppressed" || k.ResponseRate.Responded != nil || k.ResponseRate.Percentage != nil) {
				t.Fatalf("small response rate leaked: %+v", k)
			}
			if n == 0 && (k.DataState != "empty" || k.ResponseRate.Percentage == nil || *k.ResponseRate.Percentage != 0) {
				t.Fatal("empty round not distinguished")
			}
			for _, axis := range ex.Radar {
				if axis.ThisMonth != nil {
					t.Fatal("small radar group leaked")
				}
			}
		} else {
			if k.Enps.Value == nil || k.Satisfaction.Value == nil || k.Sentiment == nil || ex.Score == nil {
				t.Fatal("five-response group was hidden")
			}
			if k.Enps.DeltaVsLastMonth != nil || k.Satisfaction.Trend != nil {
				t.Fatal("compared with suppressed previous round")
			}
			if k.ResponseRate.Total == nil || *k.ResponseRate.Total != 8 || k.ResponseRate.Percentage == nil || *k.ResponseRate.Percentage != 62 {
				t.Fatalf("wrong snapshot rate: %+v", k.ResponseRate)
			}
			radar, err := stats.Radar(&p, []models.Topic{topic})
			if err != nil {
				t.Fatal(err)
			}
			if radar[0].ThisMonth == nil || radar[0].LastMonth != nil {
				t.Fatal("radar fabricated missing previous value")
			}
			insight, _, err := stats.GenerateInsight(&p)
			if err != nil || insight == nil || len(insight.IssueConfidence) != 1 {
				t.Fatalf("rare topic leaked into insight: %+v %v", insight, err)
			}
		}
		for _, point := range k.Trend {
			if point.Month != fmt.Sprintf("2090-%02d", 6) && point.ENPS != nil {
				t.Fatal("small trend group leaked")
			}
		}
	}
	pending := newPeriod(7, 5, 2, true)
	var pendingK dto.HrKpis
	get("/kpi?period="+pending.ID, &pendingK)
	var pendingEx dto.ExecutiveSummary
	get("/executive?period="+pending.ID, &pendingEx)
	if pendingK.Satisfaction.Value == nil || pendingK.Sentiment != nil || pendingEx.Score != nil {
		t.Fatal("analyzed sample threshold not enforced separately")
	}
	legacy := newPeriod(8, 5, 5, false)
	_, total, rate, err := stats.ResponseRate(legacy.ID, "")
	if err != nil || total != nil || rate != nil {
		t.Fatalf("invented legacy denominator: %v %v %v", total, rate, err)
	}
	stable := newPeriod(9, 5, 5, true)
	// Membership changes must never rewrite historic organization or department baselines.
	if err := db.Model(&models.User{}).Where("org_id = ?", orgID).Updates(map[string]any{"is_active": false, "department_id": nil}).Error; err != nil {
		t.Fatal(err)
	}
	for _, department := range []string{"", departmentID} {
		n, total, rate, err := stats.ResponseRate(stable.ID, department)
		if err != nil || n != 5 || total == nil || *total != 8 || rate == nil || *rate != 62 {
			t.Fatalf("historical rate changed: %d %v %v %v", n, total, rate, err)
		}
	}
	create(&models.SurveyImport{ID: uuid.NewString(), OrgID: orgID, PeriodID: stable.ID, FileHash: uuid.NewString(), RowCount: 5, CreatedAt: time.Now()})
	_, total, rate, err = stats.ResponseRate(stable.ID, "")
	if err != nil || total != nil || rate != nil {
		t.Fatal("import used account baseline")
	}
	tooMany := newPeriod(10, 9, 5, false)
	tooMany.ResponseAudience = map[string]int64{"__all__": 8, departmentID: 8}
	if err := db.Save(&tooMany).Error; err != nil {
		t.Fatal(err)
	}
	_, _, rate, err = stats.ResponseRate(tooMany.ID, "")
	if err != nil || rate != nil {
		t.Fatal("response rate exceeded 100 or was silently capped")
	}
	foreign := uuid.NewString()
	create(&models.Organization{ID: foreign, Name: "Foreign", Slug: foreign, JoinCode: foreign[:6]})
	if _, _, _, err := analytics.New(db).WithOrg(foreign).ResponseRate(legacy.ID, ""); err == nil {
		t.Fatal("cross-organization snapshot read allowed")
	}
}
