package router

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/mt-sense/backend-service/internal/aiservice"
	"github.com/mt-sense/backend-service/internal/auth"
	"github.com/mt-sense/backend-service/internal/config"
	"github.com/mt-sense/backend-service/internal/database"
	"github.com/mt-sense/backend-service/internal/models"
)

func TestTopicDrilldownScope(t *testing.T) {
	dsn := os.Getenv("TOPIC_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TOPIC_TEST_DATABASE_URL to an isolated PostgreSQL database")
	}
	db, err := database.Connect(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(db); err != nil {
		t.Fatal(err)
	}
	mustCreate := func(value any) {
		t.Helper()
		if err := db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	org, foreign, it, sales, small := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	topic, otherTopic := uuid.NewString(), uuid.NewString()
	for _, id := range []string{org, foreign} {
		mustCreate(&models.Organization{ID: id, Name: "Topic test", Slug: id, JoinCode: id[:6]})
	}
	for id, name := range map[string]string{it: "IT", sales: "Sales", small: "Small"} {
		mustCreate(&models.Department{ID: id, OrgID: org, Name: name})
	}
	mustCreate(&models.Topic{ID: topic, Label: models.Localized{TH: "งาน", EN: "Work"}})
	mustCreate(&models.Topic{ID: otherTopic, Label: models.Localized{TH: "อื่น", EN: "Other"}})
	periods := map[int]string{}
	for _, month := range []int{9, 10, 11} {
		id := uuid.NewString()
		periods[month] = id
		mustCreate(&models.SurveyPeriod{ID: id, OrgID: org, Month: int16(month), Year: 2026, OpensAt: time.Now().Add(-time.Hour), ClosesAt: time.Now().Add(time.Hour)})
	}
	foreignPeriod := uuid.NewString()
	mustCreate(&models.SurveyPeriod{ID: foreignPeriod, OrgID: foreign, Month: 11, Year: 2026, OpensAt: time.Now().Add(-time.Hour), ClosesAt: time.Now().Add(time.Hour)})
	seed := func(orgID, period, department, category, sentiment, comment string, score int16, n int) {
		t.Helper()
		for i := 0; i < n; i++ {
			id := uuid.NewString()
			var dept *string
			if department != "" {
				dept = &department
			}
			mustCreate(&models.SurveyResponse{ID: id, OrgID: orgID, PeriodID: period, DepartmentID: dept, SatisfactionScore: score, CommentText: comment, AnalysisStatus: "done", SubmittedAt: time.Now()})
			mustCreate(&models.ResponseAnalysis{ID: uuid.NewString(), ResponseID: id, Categories: []string{category}, SentimentLabel: sentiment, AnalyzedAt: time.Now()})
		}
	}
	seed(org, periods[11], it, topic, "pos", "IT feedback contact test@example.com", 2, 5)
	seed(org, periods[11], sales, topic, "neg", "Sales feedback", 4, 5)
	seed(org, periods[11], sales, otherTopic, "neu", "OTHER topic must not appear", 5, 5)
	seed(org, periods[9], it, topic, "pos", "OLD round must not appear", 4, 5)
	seed(org, periods[10], it, topic, "neg", "SMALL old group must stay hidden", 1, 4)
	seed(foreign, foreignPeriod, "", topic, "neg", "FOREIGN must not appear", 1, 5)
	mustCreate(&models.SurveyResponse{ID: uuid.NewString(), OrgID: org, PeriodID: periods[11], DepartmentID: &sales, SatisfactionScore: 1, CommentText: "Pending", AnalysisStatus: "pending", SubmittedAt: time.Now()})
	// Old sub-issue rows are deliberately unscoped and must not enter this response.
	mustCreate(&models.TopicSubIssue{ID: uuid.NewString(), TopicID: topic, Label: models.Localized{TH: "Old seed", EN: "Old seed"}, Percentage: 99})
	var summaryCalls, sampled atomic.Int32
	var invalidEvidence atomic.Bool
	summaryAI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		summaryCalls.Add(1)
		if request.URL.Path != "/topics/summarize" || request.Header.Get("X-Automation-Token") != "summary-test-token" {
			t.Error("wrong AI endpoint or service authentication")
			w.WriteHeader(403)
			return
		}
		raw, _ := io.ReadAll(request.Body)
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(raw, &payload); err != nil || len(payload) != 2 || payload["topic"] == nil || payload["comments"] == nil {
			t.Error("unapproved fields sent to AI", string(raw))
		}
		for _, forbidden := range []string{org, foreign, periods[11], it, sales, "test@example.com", "OTHER", "FOREIGN", "OLD"} {
			if strings.Contains(string(raw), forbidden) {
				t.Error("scope, redaction or database-ID boundary failed", forbidden)
			}
		}
		var input aiservice.TopicSummaryRequest
		if err := json.Unmarshal(raw, &input); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		sampled.Store(int32(len(input.Comments)))
		if len(input.Comments) < 5 || len(input.Comments) > 120 {
			t.Error("wrong summary batch size")
		}
		theme := aiservice.TopicTheme{TitleTH: "การวางแผนงาน", TitleEN: "Work planning", SummaryTH: "ความคิดเห็นกล่าวถึงการวางแผนงาน", SummaryEN: "Comments mention planning"}
		for index, comment := range input.Comments {
			if comment.Ref != fmt.Sprintf("c%d", index+1) {
				t.Error("not a temporary reference", comment.Ref)
			}
			if index < 5 {
				theme.Evidence = append(theme.Evidence, aiservice.TopicCitation{Ref: comment.Ref, Excerpt: "IT feedback contact"})
			}
		}
		if invalidEvidence.Load() {
			theme.Evidence[0].Ref = "not-in-input"
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(aiservice.TopicSummaryResult{Themes: []aiservice.TopicTheme{theme}})
	}))
	defer summaryAI.Close()
	cfg := &config.Config{JWTSecret: strings.Repeat("t", 32), AccessTTL: time.Hour, RefreshTTL: time.Hour, AIServiceURL: summaryAI.URL, AutomationServiceToken: "summary-test-token"}
	app := fiber.New()
	Register(app, db, cfg)
	issuer := auth.NewIssuer(cfg.JWTSecret, time.Hour, time.Hour)
	admin, _, _ := issuer.AccessToken("admin", org, models.RoleAdmin)
	employee, _, _ := issuer.AccessToken("employee", org, models.RoleEmployee)
	executive, _, _ := issuer.AccessToken("executive", org, models.RoleExecutive)
	callMethod := func(method, path, token string, want int) []byte {
		t.Helper()
		req := httptest.NewRequest(method, "/api/dashboard/hr/topics/"+path, nil)
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
			t.Fatalf("status %d want %d: %s", res.StatusCode, want, body)
		}
		return body
	}
	call := func(path, token string, want int) []byte { t.Helper(); return callMethod("GET", path, token, want) }
	type result struct {
		TopicID, PeriodID, DepartmentID     string
		Score, CompanyAverage               float64
		RespondentCount, ScopeResponseCount int64
		PercentageTagged                    int
		Sentiment                           struct{ Positive, Neutral, Negative int }
		SampleQuotes                        []string
		SubIssues                           []any
		Trend                               []struct {
			Month string
			Score *float64
		}
	}
	get := func(department string) result {
		t.Helper()
		var detail result
		if err := json.Unmarshal(call(topic+"?period="+periods[11]+department, admin, 200), &detail); err != nil {
			t.Fatal(err)
		}
		return detail
	}
	path := topic + "?period=" + periods[11]
	call(path, "", 401)
	call(path, employee, 403)
	call(path, executive, 403)
	all := get("")
	if all.Score != 3 || math.Abs(all.CompanyAverage-3.5) > 0.01 || all.RespondentCount != 10 || all.ScopeResponseCount != 16 || all.PercentageTagged != 63 {
		t.Fatalf("wrong organization metrics: %+v", all)
	}
	if all.Sentiment.Positive != 50 || all.Sentiment.Negative != 50 || all.Sentiment.Neutral != 0 || len(all.SubIssues) != 0 {
		t.Fatalf("sentiment or sub-issues are not topic scoped: %+v", all)
	}
	team := get("&department=" + it)
	if team.Score != 2 || team.RespondentCount != 5 || team.ScopeResponseCount != 5 || team.PercentageTagged != 100 || team.Sentiment.Positive != 100 {
		t.Fatalf("wrong department metrics: %+v", team)
	}
	if team.CompanyAverage != all.CompanyAverage || team.PeriodID != periods[11] || team.DepartmentID != it {
		t.Fatal("wrong benchmark or context", team)
	}
	if len(team.Trend) != 6 || team.Trend[0].Month != "2026-06" || team.Trend[5].Month != "2026-11" || team.Trend[3].Score == nil || *team.Trend[3].Score != 4 || team.Trend[4].Score != nil || team.Trend[5].Score == nil || *team.Trend[5].Score != 2 {
		t.Fatalf("missing months or suppression gaps lost: %+v", team.Trend)
	}
	for _, comment := range all.SampleQuotes {
		if strings.Contains(comment, "OTHER") || strings.Contains(comment, "OLD") || strings.Contains(comment, "FOREIGN") || strings.Contains(comment, "test@example.com") {
			t.Fatal("quote scope/redaction failed", comment)
		}
	}
	if len(team.SampleQuotes) == 0 || !strings.Contains(team.SampleQuotes[0], "IT feedback") {
		t.Fatal("missing scoped quotes", team.SampleQuotes)
	}
	// Pending analysis outside IT must not mislabel an unavailable IT topic.
	var unavailable map[string]any
	json.Unmarshal(call(otherTopic+"?period="+periods[11]+"&department="+it, admin, 404), &unavailable)
	if unavailable["reason"] != "suppressed" || unavailable["departmentName"] != "IT" || unavailable["label"] == nil {
		t.Fatal("wrong scoped empty state", unavailable)
	}
	seed(org, periods[11], small, topic, "neg", "SECRET small group", 1, 4)
	body := call(path+"&department="+small, admin, 404)
	if strings.Contains(string(body), "SECRET") || strings.Contains(string(body), "respondentCount") {
		t.Fatal("small-group contents/count leaked", string(body))
	}
	call(topic+"?period="+foreignPeriod, admin, 404)
	call(path+"&department=foreign-department", admin, 404)
	if summaryCalls.Load() != 0 {
		t.Fatal("GET drilldown triggered an AI summary")
	}
	// Generation is explicit, HR-only, scope-checked and capped before leaving the backend.
	summaryPath := topic + "/summary?period=" + periods[11] + "&department=" + it
	callMethod("POST", summaryPath, "", 401)
	callMethod("POST", summaryPath, employee, 403)
	callMethod("POST", summaryPath, executive, 403)
	seed(org, periods[11], it, topic, "pos", "IT feedback contact test@example.com", 2, 121)
	var generated struct {
		SampledComments int
		Themes          []struct {
			SupportingCount int
			Excerpts        []string
		}
	}
	if err := json.Unmarshal(callMethod("POST", summaryPath, admin, 200), &generated); err != nil {
		t.Fatal(err)
	}
	if sampled.Load() != 120 || generated.SampledComments != 120 || len(generated.Themes) != 1 || generated.Themes[0].SupportingCount != 5 {
		t.Fatalf("invalid summary cap or evidence: %+v", generated)
	}
	callMethod("POST", topic+"/summary?period="+periods[11]+"&department="+small, admin, 422)
	if summaryCalls.Load() != 1 {
		t.Fatal("small group was sent to AI")
	}
	invalidEvidence.Store(true)
	callMethod("POST", summaryPath, admin, 502)
	callMethod("POST", summaryPath, admin, 429)
	if summaryCalls.Load() != 2 {
		t.Fatal("rate limit did not stop model call")
	}
	anotherAdmin, _, _ := issuer.AccessToken("fresh-admin", org, models.RoleAdmin)
	callMethod("POST", topic+"/summary?period="+foreignPeriod, anotherAdmin, 404)
	callMethod("POST", summaryPath+"-foreign", anotherAdmin, 404)
	if summaryCalls.Load() != 2 {
		t.Fatal("foreign scope reached AI")
	}
	// Every cleanup targets only records created by this test.
	t.Cleanup(func() {
		for _, model := range []any{&models.ResponseAnalysis{}, &models.TopicSubIssue{}} {
			var err error
			if _, ok := model.(*models.ResponseAnalysis); ok {
				err = db.Where("response_id IN (?)", db.Model(&models.SurveyResponse{}).Select("id").Where("org_id IN ?", []string{org, foreign})).Delete(model).Error
			} else {
				err = db.Where("topic_id IN ?", []string{topic, otherTopic}).Delete(model).Error
			}
			if err != nil {
				t.Error(err)
			}
		}
		for _, model := range []any{&models.SurveyResponse{}, &models.SurveyPeriod{}, &models.Department{}} {
			if err := db.Where("org_id IN ?", []string{org, foreign}).Delete(model).Error; err != nil {
				t.Error(err)
			}
		}
		db.Where("id IN ?", []string{topic, otherTopic}).Delete(&models.Topic{})
		db.Where("id IN ?", []string{org, foreign}).Delete(&models.Organization{})
	})
}
