package dto

import (
	"fmt"

	"github.com/google/uuid"

	"github.com/mt-sense/backend-service/internal/models"
	"github.com/mt-sense/backend-service/internal/privacy"
)

type FormQuestionRequest struct {
	ID             string      `json:"id"`
	Type           string      `json:"type"`
	Text           Localized   `json:"text"`
	Required       bool        `json:"required"`
	MetricMapping  string      `json:"metricMapping"`
	TopicID        string      `json:"topicId"`
	SendToAI       bool        `json:"sendToAi"`
	Options        []Localized `json:"options"`
	TagSuggestions []string    `json:"tagSuggestions"`
	AllowPublish   bool        `json:"allowPublishOptIn"`
}

type FormStepRequest struct {
	ID               string                `json:"id"`
	Title            Localized             `json:"title"`
	EstimatedMinutes int                   `json:"estimatedMinutes"`
	Questions        []FormQuestionRequest `json:"questions"`
}

type SurveyFormRequest struct {
	Title         Localized         `json:"title"`
	Cadence       string            `json:"cadence"`
	NextRoundDate string            `json:"nextRoundDate"`
	Steps         []FormStepRequest `json:"steps"`
}

var validQuestionTypes = map[string]models.QuestionType{
	"scale5":       models.QuestionScale5,
	"enps":         models.QuestionENPS,
	"singleChoice": models.QuestionSingleChoice,
	"multiChoice":  models.QuestionMultiChoice,
	"openText":     models.QuestionOpenText,
}

// Validate reports every problem at once so the builder can show the full list.
//
// The identifying-question check is a hard rejection, not a warning: such a question would
// defeat anonymity before a single answer was collected, and a frontend-only check is
// bypassed by any direct call to this endpoint.
func (r *SurveyFormRequest) Validate() []string {
	var problems []string

	if r.Title.TH == "" && r.Title.EN == "" {
		problems = append(problems, "survey title is required")
	}
	if len(r.Steps) == 0 {
		problems = append(problems, "a survey needs at least one step")
	}

	for si, step := range r.Steps {
		for qi, q := range step.Questions {
			where := fmt.Sprintf("step %d question %d", si+1, qi+1)

			if _, ok := validQuestionTypes[q.Type]; !ok {
				problems = append(problems, fmt.Sprintf("%s: unknown type %q", where, q.Type))
			}
			if q.Text.TH == "" && q.Text.EN == "" {
				problems = append(problems, fmt.Sprintf("%s: text is required", where))
			}
			if privacy.LooksIdentifying(q.Text.TH) || privacy.LooksIdentifying(q.Text.EN) {
				problems = append(problems, fmt.Sprintf(
					"%s asks for identifying information, which anonymous surveys cannot collect: %q",
					where, firstNonEmpty(q.Text.TH, q.Text.EN)))
			}
			if (q.Type == "singleChoice" || q.Type == "multiChoice") && len(q.Options) < 2 {
				problems = append(problems, fmt.Sprintf("%s: choice questions need at least two options", where))
			}
		}
	}
	return problems
}

// ToModel builds the persistence tree. Call only after Validate returns no problems.
func (r *SurveyFormRequest) ToModel(id string) *models.Survey {
	survey := &models.Survey{
		ID:            id,
		Title:         r.Title,
		Cadence:       r.Cadence,
		NextRoundDate: r.NextRoundDate,
		Status:        "draft",
	}

	for si, s := range r.Steps {
		stepID := s.ID
		if stepID == "" {
			stepID = uuid.NewString()
		}
		step := models.SurveyStep{
			ID:               stepID,
			SurveyID:         id,
			Title:            s.Title,
			EstimatedMinutes: s.EstimatedMinutes,
			SortOrder:        si,
		}
		for qi, q := range s.Questions {
			questionID := q.ID
			if questionID == "" {
				questionID = uuid.NewString()
			}
			step.Questions = append(step.Questions, models.SurveyQuestion{
				ID:             questionID,
				StepID:         stepID,
				Type:           validQuestionTypes[q.Type],
				Text:           q.Text,
				Required:       q.Required,
				MetricMapping:  q.MetricMapping,
				TopicID:        q.TopicID,
				SendToAI:       q.SendToAI,
				Options:        q.Options,
				TagSuggestions: q.TagSuggestions,
				AllowPublish:   q.AllowPublish,
				SortOrder:      qi,
			})
		}
		survey.Steps = append(survey.Steps, step)
	}
	return survey
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

type ValidateQuestionRequest struct {
	Text string `json:"text"`
}

type ValidateQuestionResponse struct {
	LooksIdentifying bool `json:"looksIdentifying"`
}

type PublishResult struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Notified int    `json:"notified"`
}
