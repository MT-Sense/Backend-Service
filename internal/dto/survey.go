package dto

import (
	"encoding/json"
	"time"

	"github.com/mt-sense/backend-service/internal/models"
)

// --- responses ---

type SurveyQuestion struct {
	ID             string      `json:"id"`
	Type           string      `json:"type"`
	Text           Localized   `json:"text"`
	Required       bool        `json:"required"`
	MetricMapping  string      `json:"metricMapping,omitempty"`
	SendToAI       bool        `json:"sendToAi,omitempty"`
	Options        []Localized `json:"options,omitempty"`
	TagSuggestions []string    `json:"tagSuggestions,omitempty"`
	AllowPublish   bool        `json:"allowPublishOptIn,omitempty"`
}

type SurveyStep struct {
	ID               string           `json:"id"`
	Title            Localized        `json:"title"`
	EstimatedMinutes int              `json:"estimatedMinutes"`
	Questions        []SurveyQuestion `json:"questions"`
}

type Survey struct {
	ID            string       `json:"id"`
	Title         Localized    `json:"title"`
	Cadence       string       `json:"cadence"`
	NextRoundDate string       `json:"nextRoundDate"`
	Status        string       `json:"status"`
	Steps         []SurveyStep `json:"steps"`
}

// NewSurvey maps the loaded tree. TopicID stays out of the payload deliberately — it is an
// internal analysis mapping, not something the survey form needs.
func NewSurvey(s *models.Survey) Survey {
	out := Survey{
		ID:            s.ID,
		Title:         s.Title,
		Cadence:       s.Cadence,
		NextRoundDate: s.NextRoundDate,
		Status:        s.Status,
		Steps:         make([]SurveyStep, 0, len(s.Steps)),
	}
	for _, step := range s.Steps {
		dtoStep := SurveyStep{
			ID:               step.ID,
			Title:            step.Title,
			EstimatedMinutes: step.EstimatedMinutes,
			Questions:        make([]SurveyQuestion, 0, len(step.Questions)),
		}
		for _, q := range step.Questions {
			dtoStep.Questions = append(dtoStep.Questions, SurveyQuestion{
				ID:             q.ID,
				Type:           string(q.Type),
				Text:           q.Text,
				Required:       q.Required,
				MetricMapping:  q.MetricMapping,
				SendToAI:       q.SendToAI,
				Options:        q.Options,
				TagSuggestions: q.TagSuggestions,
				AllowPublish:   q.AllowPublish,
			})
		}
		out.Steps = append(out.Steps, dtoStep)
	}
	return out
}

func NewSurveyList(surveys []models.Survey) []Survey {
	out := make([]Survey, 0, len(surveys))
	for i := range surveys {
		out = append(out, NewSurvey(&surveys[i]))
	}
	return out
}

// --- submission ---

type SubmittedAnswer struct {
	QuestionID    string          `json:"questionId"`
	Value         json.RawMessage `json:"value"`
	Tags          []string        `json:"tags"`
	OptedInToFeed bool            `json:"optedInToFeed"`
}

type SubmitResponseRequest struct {
	Answers []SubmittedAnswer `json:"answers"`
}

func (r *SubmitResponseRequest) Validate() []string {
	if len(r.Answers) == 0 {
		return []string{"a submission must contain at least one answer"}
	}
	return nil
}

// SubmitResponseReceipt returns the per-submission token, which identifies the submission
// and never the submitter — there is intentionally no user field on this struct.
type SubmitResponseReceipt struct {
	AnonymousToken string    `json:"anonymousToken"`
	SubmittedAt    time.Time `json:"submittedAt"`
}
