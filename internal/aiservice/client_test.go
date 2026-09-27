package aiservice

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAnalyzeUsesAIResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/analyze" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var request struct {
			Texts []string `json:"texts"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if len(request.Texts) != 1 || request.Texts[0] != "งานหนัก" {
			t.Errorf("unexpected texts: %v", request.Texts)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"sentiment_label":"neg","sentiment_score":-0.8,"confidence":0.91,"low_confidence":false,"categories":["work"],"reason":"ภาระงานสูง"}]}`))
	}))
	defer server.Close()

	result, err := New(server.URL, time.Second).Analyze(context.Background(), "งานหนัก")
	if err != nil {
		t.Fatal(err)
	}
	if result.SentimentLabel != "neg" || result.Categories[0] != "work" || result.Reason != "ภาระงานสูง" {
		t.Fatalf("unexpected analysis: %+v", result)
	}
}

func TestAnalyzeRejectsUnknownDashboardCategory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"sentiment_label":"neu","sentiment_score":0,"confidence":0.8,"categories":["สวัสดิการ"]}]}`))
	}))
	defer server.Close()

	if _, err := New(server.URL, time.Second).Analyze(context.Background(), "ข้อความ"); err == nil {
		t.Fatal("expected a contract error for an unmapped category")
	}
}

func TestAnalyzeManyPreservesOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Texts []string `json:"texts"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if len(request.Texts) != 2 || request.Texts[0] != "ดี" || request.Texts[1] != "แย่" {
			t.Errorf("unexpected request order: %v", request.Texts)
		}
		_, _ = w.Write([]byte(`{"results":[{"sentiment_label":"pos","sentiment_score":0.8,"confidence":0.9,"categories":["team"]},{"sentiment_label":"neg","sentiment_score":-0.7,"confidence":0.8,"categories":["work"]}]}`))
	}))
	defer server.Close()
	results, err := New(server.URL, time.Second).AnalyzeMany(context.Background(), []string{"ดี", "แย่"})
	if err != nil || len(results) != 2 || results[0].SentimentLabel != "pos" || results[1].SentimentLabel != "neg" {
		t.Fatalf("unexpected results: %+v, %v", results, err)
	}
}

func TestKeywordsPreservesResponseOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/keywords" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var request struct {
			Texts []string `json:"texts"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if len(request.Texts) != 2 || request.Texts[0] != "ระบบช้า" {
			t.Errorf("unexpected texts: %v", request.Texts)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"keywords":[["ระบบ"],["ทีม","งาน"]]}`))
	}))
	defer server.Close()

	keywords, err := New(server.URL, time.Second).Keywords(context.Background(), []string{"ระบบช้า", "ทีมงาน"})
	if err != nil {
		t.Fatal(err)
	}
	if len(keywords) != 2 || keywords[0][0] != "ระบบ" || keywords[1][0] != "ทีม" {
		t.Fatalf("unexpected keywords: %v", keywords)
	}
}

func TestCompileKnowledgeUsesAggregateContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/knowledge/compile" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var request KnowledgeCompileRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Source.PeriodLabel != "2026-09" || request.Source.TotalResponses != 12 {
			t.Fatalf("unexpected source: %+v", request.Source)
		}
		if len(request.PreviousArticles) != 1 || request.PreviousArticles[0].PeriodID != "aug" {
			t.Fatalf("unexpected previous articles: %+v", request.PreviousArticles)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"title_th":"สรุป 2026-09",
			"title_en":"Summary 2026-09",
			"summary_th":"คะแนนเฉลี่ย 3.8/5",
			"summary_en":"Average satisfaction is 3.8/5",
			"markdown":"# สรุป 2026-09",
			"tags":["work"],
			"related_period_ids":["aug"],
			"suggested_questions":["อะไรเปลี่ยนไป?"]
		}`))
	}))
	defer server.Close()

	request := KnowledgeCompileRequest{
		Source: KnowledgeSource{
			PeriodID:            "sep",
			PeriodLabel:         "2026-09",
			TotalResponses:      12,
			AverageSatisfaction: 3.8,
		},
		PreviousArticles: []PreviousKnowledgeArticle{{PeriodID: "aug", PeriodLabel: "2026-08"}},
	}
	result, err := New(server.URL, time.Second).CompileKnowledge(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.TitleTH != "สรุป 2026-09" || len(result.RelatedPeriodIDs) != 1 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestCompileKnowledgeRejectsIncompleteArticle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"title_th":"only a title"}`))
	}))
	defer server.Close()

	_, err := New(server.URL, time.Second).CompileKnowledge(context.Background(), KnowledgeCompileRequest{})
	if err == nil {
		t.Fatal("expected incomplete knowledge article to be rejected")
	}
}

func TestAskKnowledgeForwardsPrivacySafeEvidence(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/knowledge/ask" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var request KnowledgeQARequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		if request.Question != "แนวโน้ม workload เป็นอย่างไร" || request.Locale != "th" {
			t.Fatalf("unexpected request: %+v", request)
		}
		if len(request.Articles) != 1 || request.Articles[0].PeriodLabel != "2026-09" {
			t.Fatalf("unexpected articles: %+v", request.Articles)
		}
		if !strings.Contains(string(request.Articles[0].SourceSnapshot), "average_satisfaction") {
			t.Fatalf("source snapshot missing: %s", request.Articles[0].SourceSnapshot)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"answer":"คะแนนเฉลี่ยอยู่ที่ 3.8/5","used_period_ids":["sep"]}`))
	}))
	defer server.Close()

	result, err := New(server.URL, time.Second).AskKnowledge(context.Background(), KnowledgeQARequest{
		Question: "แนวโน้ม workload เป็นอย่างไร",
		Locale:   "th",
		Articles: []KnowledgeQAArticle{{
			PeriodID:       "sep",
			PeriodLabel:    "2026-09",
			Title:          "สรุป",
			Summary:        "ภาพรวม",
			SourceSnapshot: json.RawMessage(`{"average_satisfaction":3.8}`),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Answer == "" || len(result.UsedPeriodIDs) != 1 || result.UsedPeriodIDs[0] != "sep" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestTrainForwardsWorkbookAndToken(t *testing.T) {
	workbook := []byte("PK\x03\x04workbook")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/train" {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("X-Training-Token") != "shared-secret" {
			t.Error("training token missing")
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != string(workbook) {
			t.Errorf("workbook changed in transit")
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"accuracy":0.69,"macroF1":0.64,"testRows":72,"trainingRows":358}`))
	}))
	defer server.Close()

	report, err := New(server.URL, time.Second).Train(context.Background(), workbook, "shared-secret")
	if err != nil {
		t.Fatal(err)
	}
	if report.Accuracy != 0.69 || report.TestRows != 72 || report.TrainingRows != 358 {
		t.Fatalf("unexpected report: %+v", report)
	}
}
