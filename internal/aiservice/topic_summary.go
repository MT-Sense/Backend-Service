package aiservice

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// TopicSummaryError carries an allowlisted public diagnostic, never provider text.
type TopicSummaryError struct{ Code string }

func (e *TopicSummaryError) Error() string { return e.Code }

func safeTopicSummaryCode(code string) string {
	switch code {
	case "summary_provider_request", "summary_provider_auth", "summary_model_unavailable",
		"summary_provider_quota", "summary_timeout", "summary_invalid_response",
		"summary_invalid_evidence":
		return code
	default:
		return "summary_unavailable"
	}
}

type TopicComment struct {
	Ref  string `json:"ref"`
	Text string `json:"text"`
}
type TopicSummaryRequest struct {
	Topic    string         `json:"topic"`
	Comments []TopicComment `json:"comments"`
}
type TopicCitation struct {
	Ref     string `json:"ref"`
	Excerpt string `json:"excerpt"`
}
type TopicTheme struct {
	TitleTH   string          `json:"title_th"`
	TitleEN   string          `json:"title_en"`
	SummaryTH string          `json:"summary_th"`
	SummaryEN string          `json:"summary_en"`
	Evidence  []TopicCitation `json:"evidence"`
}
type TopicSummaryResult struct {
	Themes []TopicTheme `json:"themes"`
}

func (c *Client) SummarizeTopic(ctx context.Context, input TopicSummaryRequest, token string) (*TopicSummaryResult, error) {
	body, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/topics/summarize", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Automation-Token", token)
	client := &http.Client{Timeout: 70 * time.Second}
	response, err := client.Do(req)
	if err != nil {
		var networkError net.Error
		if errors.As(err, &networkError) && networkError.Timeout() {
			return nil, &TopicSummaryError{Code: "summary_timeout"}
		}
		return nil, &TopicSummaryError{Code: "summary_unavailable"}
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		var body struct {
			Detail struct {
				Code string `json:"code"`
			} `json:"detail"`
		}
		_ = json.NewDecoder(io.LimitReader(response.Body, 1<<14)).Decode(&body)
		return nil, &TopicSummaryError{Code: safeTopicSummaryCode(body.Detail.Code)}
	}
	var result TopicSummaryResult
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&result); err != nil {
		return nil, fmt.Errorf("invalid topic summary response")
	}
	return &result, nil
}
