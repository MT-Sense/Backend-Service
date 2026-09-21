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
	SentimentLabel string   `json:"sentiment_label"`
	SentimentScore float32  `json:"sentiment_score"`
	Confidence     float32  `json:"confidence"`
	LowConfidence  bool     `json:"low_confidence"`
	Categories     []string `json:"categories"`
	Reason         string   `json:"reason"`
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

// AnalyzeMany is used by HR workbook imports; the AI service processes texts in batches.
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

func validCategory(category string) bool {
	switch category {
	case "work", "team", "manager", "compensation", "growth", "benefits":
		return true
	default:
		return false
	}
}
