package aiservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Result uses the same topic IDs as response_analysis and the dashboards.
type Result struct {
	SentimentLabel string          `json:"sentiment_label"`
	SentimentScore float32         `json:"sentiment_score"`
	Confidence     float32         `json:"confidence"`
	LowConfidence  bool            `json:"low_confidence"`
	Categories     []string        `json:"categories"`
	EmergingTopics []EmergingTopic `json:"emerging_topics"`
	Reason         string          `json:"reason"`
}

type EmergingTopic struct {
	LabelTH string `json:"label_th"`
	LabelEN string `json:"label_en"`
}

type KnowledgeTopic struct {
	ID                  string  `json:"id"`
	Label               string  `json:"label"`
	AverageSatisfaction float64 `json:"average_satisfaction"`
	MentionCount        int64   `json:"mention_count"`
}

type KnowledgeDepartment struct {
	ID                  string  `json:"id"`
	Name                string  `json:"name"`
	AverageSatisfaction float64 `json:"average_satisfaction"`
	ResponseCount       int64   `json:"response_count"`
}

type KnowledgeSource struct {
	PeriodID            string                `json:"period_id"`
	PeriodLabel         string                `json:"period_label"`
	TotalResponses      int64                 `json:"total_responses"`
	AverageSatisfaction float64               `json:"average_satisfaction"`
	Sentiment           *KnowledgeSentiment   `json:"sentiment,omitempty"`
	Topics              []KnowledgeTopic      `json:"topics"`
	Departments         []KnowledgeDepartment `json:"departments"`
}

type KnowledgeSentiment struct {
	Positive int `json:"positive"`
	Neutral  int `json:"neutral"`
	Negative int `json:"negative"`
	Analyzed int `json:"analyzed"`
}

type PreviousKnowledgeArticle struct {
	PeriodID    string `json:"period_id"`
	PeriodLabel string `json:"period_label"`
	TitleTH     string `json:"title_th"`
	SummaryTH   string `json:"summary_th"`
}

type KnowledgeCompileRequest struct {
	Source           KnowledgeSource            `json:"source"`
	PreviousArticles []PreviousKnowledgeArticle `json:"previous_articles"`
}

type KnowledgeCompileResult struct {
	TitleTH            string   `json:"title_th"`
	TitleEN            string   `json:"title_en"`
	SummaryTH          string   `json:"summary_th"`
	SummaryEN          string   `json:"summary_en"`
	Markdown           string   `json:"markdown"`
	Tags               []string `json:"tags"`
	RelatedPeriodIDs   []string `json:"related_period_ids"`
	SuggestedQuestions []string `json:"suggested_questions"`
}

type KnowledgeQAArticle struct {
	PeriodID       string          `json:"period_id"`
	PeriodLabel    string          `json:"period_label"`
	Title          string          `json:"title"`
	Summary        string          `json:"summary"`
	Tags           []string        `json:"tags"`
	SourceSnapshot json.RawMessage `json:"source_snapshot"`
}

type KnowledgeQARequest struct {
	Question string               `json:"question"`
	Articles []KnowledgeQAArticle `json:"articles"`
	Locale   string               `json:"locale"`
}

type KnowledgeQAResult struct {
	Answer        string   `json:"answer"`
	UsedPeriodIDs []string `json:"used_period_ids"`
}

type Client struct {
	url     string
	baseURL string
	http    *http.Client
}

func New(baseURL string, timeout time.Duration) *Client {
	return &Client{
		url:     strings.TrimRight(baseURL, "/") + "/analyze",
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: timeout},
	}
}

type TrainingReport struct {
	Accuracy     float64 `json:"accuracy"`
	MacroF1      float64 `json:"macroF1"`
	TestRows     int     `json:"testRows"`
	TrainingRows int     `json:"trainingRows"`
}

type TrainingError struct {
	StatusCode int
	Detail     string
}

func (e *TrainingError) Error() string {
	return fmt.Sprintf("AI training returned status %d: %s", e.StatusCode, e.Detail)
}

func (c *Client) Train(ctx context.Context, data []byte, token string) (*TrainingReport, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/train", bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	req.Header.Set("X-Training-Token", token)
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("AI training request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var payload struct {
			Detail string `json:"detail"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&payload)
		return nil, &TrainingError{StatusCode: resp.StatusCode, Detail: payload.Detail}
	}
	var report TrainingReport
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&report); err != nil {
		return nil, fmt.Errorf("invalid AI training response: %w", err)
	}
	return &report, nil
}

func (c *Client) Analyze(ctx context.Context, text string) (*Result, error) {
	results, err := c.analyze(ctx, []string{text}, c.http)
	if err != nil {
		return nil, err
	}
	return &results[0], nil
}

// AnalyzeMany is used by the anonymous response queue and HR workbook imports.
func (c *Client) AnalyzeMany(ctx context.Context, texts []string) ([]Result, error) {
	if len(texts) == 0 {
		return []Result{}, nil
	}
	return c.analyze(ctx, texts, &http.Client{Timeout: 5 * time.Minute})
}

func (c *Client) analyze(ctx context.Context, texts []string, client *http.Client) ([]Result, error) {
	body, err := json.Marshal(struct {
		Texts []string `json:"texts"`
	}{Texts: texts})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("AI request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("AI service returned status %d", resp.StatusCode)
	}
	var payload struct {
		Results []Result `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("invalid AI response: %w", err)
	}
	if len(payload.Results) != len(texts) {
		return nil, errors.New("AI service returned an unexpected number of results")
	}
	for _, result := range payload.Results {
		if result.SentimentLabel != "pos" && result.SentimentLabel != "neg" && result.SentimentLabel != "neu" {
			return nil, errors.New("AI service returned an unknown sentiment label")
		}
		if result.SentimentScore < -1 || result.SentimentScore > 1 || result.Confidence < 0 || result.Confidence > 1 {
			return nil, errors.New("AI service returned a score outside the expected range")
		}
		for _, category := range result.Categories {
			if !validCategory(category) {
				return nil, fmt.Errorf("AI service returned an unknown category %q", category)
			}
		}
		for _, topic := range result.EmergingTopics {
			label := strings.TrimSpace(topic.LabelTH)
			if len([]rune(label)) < 2 || len([]rune(label)) > 40 {
				return nil, errors.New("AI service returned an invalid emerging topic label")
			}
		}
	}
	return payload.Results, nil
}

// Keywords segments redacted survey comments into useful words. Each inner slice is
// de-duplicated by the AI service, so the caller can count distinct responses per word.
func (c *Client) Keywords(ctx context.Context, texts []string) ([][]string, error) {
	body, err := json.Marshal(struct {
		Texts []string `json:"texts"`
	}{Texts: texts})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/keywords", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("AI keyword request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("AI keyword service returned status %d", resp.StatusCode)
	}
	var payload struct {
		Keywords [][]string `json:"keywords"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(&payload); err != nil {
		return nil, fmt.Errorf("invalid AI keyword response: %w", err)
	}
	if len(payload.Keywords) != len(texts) {
		return nil, errors.New("AI keyword service returned an unexpected number of results")
	}
	return payload.Keywords, nil
}

func (c *Client) CompileKnowledge(ctx context.Context, request KnowledgeCompileRequest) (*KnowledgeCompileResult, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/knowledge/compile", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("AI knowledge compile request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var payload struct {
			Detail string `json:"detail"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&payload)
		if payload.Detail == "" {
			payload.Detail = http.StatusText(resp.StatusCode)
		}
		return nil, fmt.Errorf("AI knowledge compiler returned status %d: %s", resp.StatusCode, payload.Detail)
	}
	var result KnowledgeCompileResult
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return nil, fmt.Errorf("invalid AI knowledge response: %w", err)
	}
	if strings.TrimSpace(result.TitleTH) == "" || strings.TrimSpace(result.TitleEN) == "" ||
		strings.TrimSpace(result.SummaryTH) == "" || strings.TrimSpace(result.SummaryEN) == "" ||
		strings.TrimSpace(result.Markdown) == "" {
		return nil, errors.New("AI knowledge compiler returned an incomplete article")
	}
	return &result, nil
}

func (c *Client) AskKnowledge(ctx context.Context, request KnowledgeQARequest) (*KnowledgeQAResult, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/knowledge/ask", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 2 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("AI knowledge Q&A request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var payload struct {
			Detail string `json:"detail"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&payload)
		return nil, fmt.Errorf("AI knowledge Q&A returned status %d: %s", resp.StatusCode, payload.Detail)
	}
	var result KnowledgeQAResult
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return nil, fmt.Errorf("invalid AI knowledge Q&A response: %w", err)
	}
	if strings.TrimSpace(result.Answer) == "" {
		return nil, errors.New("AI knowledge Q&A returned an empty answer")
	}
	return &result, nil
}

func validCategory(category string) bool {
	switch category {
	case "work", "team", "manager", "compensation", "growth", "benefits":
		return true
	default:
		return false
	}
}
