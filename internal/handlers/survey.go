package handlers

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/mt-sense/backend-service/internal/auth"
	"github.com/mt-sense/backend-service/internal/dto"
	"github.com/mt-sense/backend-service/internal/middleware"
	"github.com/mt-sense/backend-service/internal/models"
	"github.com/mt-sense/backend-service/internal/privacy"
)

type SurveyHandler struct{ db *gorm.DB }

func NewSurveyHandler(db *gorm.DB) *SurveyHandler { return &SurveyHandler{db: db} }

// Get returns a survey with its steps and questions, ordered for rendering.
func (h *SurveyHandler) Get(c *fiber.Ctx) error {
	var survey models.Survey
	err := h.db.
		Preload("Steps", func(db *gorm.DB) *gorm.DB { return db.Order("survey_steps.sort_order") }).
		Preload("Steps.Questions", func(db *gorm.DB) *gorm.DB { return db.Order("survey_questions.sort_order") }).
		First(&survey, "id = ?", c.Params("id")).Error
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "survey not found")
	}
	return c.JSON(dto.NewSurvey(&survey))
}

// Submit stores a response with no link back to the caller.
//
// The user id is used for exactly two things, both outside the response itself: reading the
// submitter's coarse department/tenure so aggregates can be sliced, and stamping the audit
// log so reminder emails know who still owes a submission. Neither is written into
// survey_responses, and the whole thing runs in one transaction so an audit row can never
// exist for a response that failed to save (or vice versa).
func (h *SurveyHandler) Submit(c *fiber.Ctx) error {
	surveyID := c.Params("id")
	userID := middleware.UserID(c)

	var req dto.SubmitResponseRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "malformed request body")
	}
	if problems := req.Validate(); len(problems) > 0 {
		return c.Status(fiber.StatusBadRequest).JSON(dto.ValidationErrors{Errors: problems})
	}

	var survey models.Survey
	if err := h.db.First(&survey, "id = ?", surveyID).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "survey not found")
	}

	var user models.User
	if err := h.db.First(&user, "id = ?", userID).Error; err != nil {
		return fiber.NewError(fiber.StatusUnauthorized, "unknown user")
	}

	// One submission per user per survey — checked against the audit log, which is the
	// only place that association is allowed to exist.
	var already int64
	err := h.db.Model(&models.AuditSubmissionLog{}).
		Where("user_id = ? AND survey_id = ? AND status = ?", userID, surveyID, "submitted").
		Count(&already).Error
	if err != nil {
		return err
	}
	if already > 0 {
		return fiber.NewError(fiber.StatusConflict, "you have already submitted this survey")
	}

	questions, err := h.questionsByID(surveyID)
	if err != nil {
		return err
	}

	token, err := auth.AnonymousToken()
	if err != nil {
		return err
	}
	now := time.Now()
	response := models.SurveyResponse{
		ID:             uuid.NewString(),
		SurveyID:       surveyID,
		AnonymousToken: token,
		DepartmentID:   user.DepartmentID,
		TenureBucket:   user.TenureBucket,
		SubmittedAt:    now,
		PeriodMonth:    now.Format("2006-01"),
	}

	answers := make([]models.ResponseAnswer, 0, len(req.Answers))
	var feedPosts []models.FeedPost

	for _, a := range req.Answers {
		question, ok := questions[a.QuestionID]
		if !ok {
			return fiber.NewError(fiber.StatusBadRequest, "answer refers to a question that is not in this survey: "+a.QuestionID)
		}

		answer := models.ResponseAnswer{
			ResponseID:    response.ID,
			QuestionID:    question.ID,
			TopicID:       question.TopicID,
			Value:         a.Value,
			Tags:          a.Tags,
			OptedInToFeed: a.OptedInToFeed,
		}

		switch question.Type {
		case models.QuestionScale5, models.QuestionENPS:
			var n float64
			if err := json.Unmarshal(a.Value, &n); err != nil {
				return fiber.NewError(fiber.StatusBadRequest, "question "+question.ID+" expects a number")
			}
			if !validNumericAnswer(question.Type, n) {
				return fiber.NewError(fiber.StatusBadRequest, "value out of range for question "+question.ID)
			}
			answer.NumericValue = &n

		case models.QuestionOpenText:
			var text string
			if err := json.Unmarshal(a.Value, &text); err != nil {
				return fiber.NewError(fiber.StatusBadRequest, "question "+question.ID+" expects text")
			}
			// Redact before anything is persisted — the raw text never reaches a table.
			redacted := privacy.Redact(strings.TrimSpace(text))
			answer.TextRedacted = redacted
			answer.Sentiment = classifySentiment(redacted)
			answer.Value = json.RawMessage(mustJSON(redacted))

			// Gate one of the feed flow: the author opted in. Gate two (moderation) is a
			// separate HR action, so the post starts unpublished.
			if question.AllowPublish && a.OptedInToFeed && redacted != "" {
				feedPosts = append(feedPosts, models.FeedPost{
					ID:        uuid.NewString(),
					Text:      redacted,
					Hashtags:  a.Tags,
					PostedOn:  now.Format("2006-01-02"),
					OptedIn:   true,
					Published: false,
				})
			}
		}

		answers = append(answers, answer)
	}

	err = h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&response).Error; err != nil {
			return err
		}
		if err := tx.Create(&answers).Error; err != nil {
			return err
		}
		if len(feedPosts) > 0 {
			if err := tx.Create(&feedPosts).Error; err != nil {
				return err
			}
		}
		// Audit log: separate table, records only that a submission happened.
		return tx.Where(models.AuditSubmissionLog{UserID: userID, SurveyID: surveyID}).
			Assign(models.AuditSubmissionLog{Status: "submitted", SubmittedAt: &now}).
			FirstOrCreate(&models.AuditSubmissionLog{}).Error
	})
	if err != nil {
		return err
	}

	// The receipt identifies the submission, never the submitter.
	return c.Status(fiber.StatusCreated).JSON(dto.SubmitResponseReceipt{
		AnonymousToken: token,
		SubmittedAt:    now,
	})
}

func validNumericAnswer(t models.QuestionType, n float64) bool {
	if t == models.QuestionScale5 {
		return n >= 1 && n <= 5
	}
	return n >= 0 && n <= 10
}

func (h *SurveyHandler) questionsByID(surveyID string) (map[string]models.SurveyQuestion, error) {
	var questions []models.SurveyQuestion
	err := h.db.
		Joins("JOIN survey_steps ON survey_steps.id = survey_questions.step_id").
		Where("survey_steps.survey_id = ?", surveyID).
		Find(&questions).Error
	if err != nil {
		return nil, err
	}
	out := make(map[string]models.SurveyQuestion, len(questions))
	for _, q := range questions {
		out[q.ID] = q
	}
	return out, nil
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		return []byte(`""`)
	}
	return b
}

// classifySentiment is a keyword heuristic standing in for the analysis pipeline.
// ponytail: lexicon lookup, no negation handling; replace with the real classifier — the
// column it writes and every aggregate reading it stay the same.
func classifySentiment(text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	lowered := strings.ToLower(text)
	positive, negative := 0, 0
	for _, w := range positiveWords {
		if strings.Contains(lowered, w) {
			positive++
		}
	}
	for _, w := range negativeWords {
		if strings.Contains(lowered, w) {
			negative++
		}
	}
	switch {
	case positive > negative:
		return "positive"
	case negative > positive:
		return "negative"
	default:
		return "neutral"
	}
}

var (
	positiveWords = []string{"ดี", "ชอบ", "ขอบคุณ", "สนุก", "ประทับใจ", "ดีขึ้น", "สนับสนุน", "good", "great", "love", "thanks", "better"}
	negativeWords = []string{"หนัก", "เหนื่อย", "ไม่พอ", "ล่าช้า", "ไม่ชัดเจน", "ปัญหา", "แย่", "เครียด", "ไม่เป็นธรรม", "bad", "tired", "unclear", "problem", "stress", "overwork"}
)
