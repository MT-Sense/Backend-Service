package aiservice

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
