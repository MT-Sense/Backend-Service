package knowledgebase_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/mt-sense/backend-service/internal/aiservice"
	"github.com/mt-sense/backend-service/internal/database"
	"github.com/mt-sense/backend-service/internal/knowledgebase"
	"github.com/mt-sense/backend-service/internal/models"
)

type fakeCompiler struct {
	calls int
	last  aiservice.KnowledgeCompileRequest
}

func (f *fakeCompiler) CompileKnowledge(_ context.Context, request aiservice.KnowledgeCompileRequest) (*aiservice.KnowledgeCompileResult, error) {
	f.calls++
	f.last = request
	return &aiservice.KnowledgeCompileResult{
		TitleTH:            "สรุปบรรยากาศองค์กร 2026-09",
		TitleEN:            "Organization climate 2026-09",
		SummaryTH:          "คะแนนเฉลี่ย 3.5/5 จากข้อมูล aggregate",
		SummaryEN:          "Average satisfaction is 3.5/5 from aggregate evidence.",
		Markdown:           "# ภาพรวม\n\nคะแนนเฉลี่ย 3.5/5\n\n## รอบที่เกี่ยวข้อง\n[[2026-08]]",
		Tags:               []string{"work"},
		RelatedPeriodIDs:   []string{"period-aug", "not-allowed"},
		SuggestedQuestions: []string{"แนวโน้มภาระงานเปลี่ยนอย่างไร?"},
	}, nil
}

func TestCompilePeriodPersistsAndReusesUnchangedArticle(t *testing.T) {
	dsn := os.Getenv("KB_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set KB_TEST_DATABASE_URL to run PostgreSQL knowledge-base integration test")
	}

	db, err := database.Connect(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Migrate(db); err != nil {
		t.Fatal(err)
	}

	org := models.Organization{
		ID: "org-kb-test", Name: "KB Test", Slug: "kb-test", JoinCode: "KB0001",
		CollectDepartment: true, CollectTenure: false,
	}
	if err := db.Create(&org).Error; err != nil {
		t.Fatal(err)
	}
	department := models.Department{ID: "dept-eng", OrgID: org.ID, Name: "Engineering"}
	if err := db.Create(&department).Error; err != nil {
		t.Fatal(err)
	}
	topic := models.Topic{ID: "work", Label: models.Localized{TH: "การทำงานและภาระงาน", EN: "Work and workload"}, SortOrder: 1}
	if err := db.Create(&topic).Error; err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	previous := models.SurveyPeriod{
		ID: "period-aug", OrgID: org.ID, Month: 8, Year: 2026,
		OpensAt: now.AddDate(0, -2, 0), ClosesAt: now.AddDate(0, -1, -1),
	}
	current := models.SurveyPeriod{
		ID: "period-sep", OrgID: org.ID, Month: 9, Year: 2026,
		OpensAt: now.AddDate(0, -1, 0), ClosesAt: now.AddDate(0, 0, 1),
	}
	if err := db.Create(&[]models.SurveyPeriod{previous, current}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.KnowledgeBaseSummary{
		ID: "kb-aug", OrgID: org.ID, PeriodID: previous.ID,
		TitleTH: "สรุป 2026-08", TitleEN: "Summary 2026-08",
		SummaryText: "คะแนนเฉลี่ยรอบก่อน 3.4/5", SummaryEN: "Previous average 3.4/5",
		MarkdownText: "# 2026-08", SourceHash: "previous", CompiledAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 6; i++ {
		responseID := uuid.NewString()
		response := models.SurveyResponse{
			ID: responseID, OrgID: org.ID, PeriodID: current.ID, DepartmentID: &department.ID,
			SatisfactionScore: int16(3 + i%2), SubmittedAt: now,
		}
		if err := db.Create(&response).Error; err != nil {
			t.Fatal(err)
		}
		label := "pos"
		score := float32(0.6)
		if i == 0 {
			label, score = "neg", -0.6
		}
		analysis := models.ResponseAnalysis{
			ID: uuid.NewString(), ResponseID: responseID, SentimentLabel: label,
			SentimentScore: score, Confidence: 0.9, Categories: []string{"work"}, AnalyzedAt: now,
		}
		if err := db.Create(&analysis).Error; err != nil {
			t.Fatal(err)
		}
	}

	compiler := &fakeCompiler{}
	service := knowledgebase.New(db, compiler)

	first, reused, err := service.CompilePeriod(context.Background(), org.ID, current.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if reused || compiler.calls != 1 {
		t.Fatalf("first compile should call LLM once: reused=%v calls=%d", reused, compiler.calls)
	}
	if first.Entry.RelatedPeriodIDs[0] != previous.ID || len(first.Entry.RelatedPeriodIDs) != 1 {
		t.Fatalf("unexpected related periods: %v", first.Entry.RelatedPeriodIDs)
	}
	if compiler.last.Source.TotalResponses != 6 || len(compiler.last.Source.Topics) != 1 || len(compiler.last.Source.Departments) != 1 {
		t.Fatalf("unexpected privacy-safe source: %+v", compiler.last.Source)
	}
	if len(compiler.last.PreviousArticles) != 1 || compiler.last.PreviousArticles[0].PeriodID != previous.ID {
		t.Fatalf("previous article was not supplied: %+v", compiler.last.PreviousArticles)
	}

	second, reused, err := service.CompilePeriod(context.Background(), org.ID, current.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if !reused || compiler.calls != 1 || second.Entry.SourceHash != first.Entry.SourceHash {
		t.Fatalf("unchanged source should reuse article: reused=%v calls=%d", reused, compiler.calls)
	}

	index, err := service.IndexMarkdown(org.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(index, "[[2026-09]]") || !strings.Contains(index, "[[2026-08]]") {
		t.Fatalf("index is missing wiki links: %s", index)
	}
}
