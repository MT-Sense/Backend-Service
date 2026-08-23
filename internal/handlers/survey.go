package handlers

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/mt-sense/backend-service/internal/analytics"
	"github.com/mt-sense/backend-service/internal/dto"
	"github.com/mt-sense/backend-service/internal/middleware"
	"github.com/mt-sense/backend-service/internal/models"
	"github.com/mt-sense/backend-service/internal/privacy"
)

type SurveyHandler struct {
	db    *gorm.DB
	stats *analytics.Service
	orgID string
}

func NewSurveyHandler(db *gorm.DB, stats *analytics.Service, orgID string) *SurveyHandler {
	return &SurveyHandler{db: db, stats: stats, orgID: orgID}
}

// Current returns the currently open survey period, if any, and whether the caller already
// submitted it this period.
func (h *SurveyHandler) Current(c *fiber.Ctx) error {
	period, err := h.stats.CurrentOpenPeriod()
	if err != nil {
		return err
	}
	if period == nil {
		return fiber.NewError(fiber.StatusNotFound, "no survey period is currently open")
	}

	var submitted int64
	err = h.db.Model(&models.SurveySubmission{}).
		Where("user_id = ? AND period_id = ?", middleware.UserID(c), period.ID).
		Count(&submitted).Error
	if err != nil {
		return err
	}

	return c.JSON(dto.NewSurveyPeriod(period, submitted > 0, 0))
}

// Submit stores a response with no link back to the caller.
//
// The user id is used for exactly two things, both outside the response itself: reading the
// submitter's coarse department/position so aggregates can be sliced, and recording that a
// submission happened so the response-rate count and repeat-submission check work. Neither
// is written into survey_responses, and the whole thing runs in one transaction so a
// submission record can never exist for a response that failed to save (or vice versa).
func (h *SurveyHandler) Submit(c *fiber.Ctx) error {
	userID := middleware.UserID(c)

	var req dto.SubmitResponseRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "malformed request body")
	}
	if problems := req.Validate(); len(problems) > 0 {
		return c.Status(fiber.StatusBadRequest).JSON(dto.ValidationErrors{Errors: problems})
	}

	period, err := h.stats.CurrentOpenPeriod()
	if err != nil {
		return err
	}
	if period == nil {
		return fiber.NewError(fiber.StatusNotFound, "no survey period is currently open")
	}

	var user models.User
	if err := h.db.First(&user, "id = ?", userID).Error; err != nil {
		return fiber.NewError(fiber.StatusUnauthorized, "unknown user")
	}

	var already int64
	err = h.db.Model(&models.SurveySubmission{}).
		Where("user_id = ? AND period_id = ?", userID, period.ID).
		Count(&already).Error
	if err != nil {
		return err
	}
	if already > 0 {
		return fiber.NewError(fiber.StatusConflict, "you have already submitted this period")
	}

	now := time.Now()
	response := models.SurveyResponse{
		ID:                uuid.NewString(),
		OrgID:              h.orgID,
		PeriodID:           period.ID,
		DepartmentID:       user.DepartmentID,
		PositionID:         user.PositionID,
		SatisfactionScore:  int16(req.SatisfactionScore),
		SubmittedAt:        now,
	}

	var analysis *models.ResponseAnalysis
	var feedPost *models.FeedPost

	comment := strings.TrimSpace(req.CommentText)
	if comment != "" {
		// Redact before anything is persisted — the raw text never reaches a table.
		redacted := privacy.Redact(comment)
		response.CommentText = redacted

		if redacted != "" {
			sentimentLabel, sentimentScore := classifySentiment(redacted)
			categories := classifyCategories(redacted)
			confidence, lowConfidence := confidenceFor(categories, sentimentScore)

			analysis = &models.ResponseAnalysis{
				ID:             uuid.NewString(),
				ResponseID:     response.ID,
				SentimentLabel: sentimentLabel,
				SentimentScore: sentimentScore,
				Confidence:     confidence,
				LowConfidence:  lowConfidence,
				Categories:     categories,
				AnalyzedAt:     now,
			}

			// Gate one of the feed flow: the author opted in. Gate two (moderation) is a
			// separate HR action, so the post starts unpublished.
			if req.OptedInToFeed {
				tags := req.Tags
				if len(tags) == 0 {
					tags = categories
				}
				feedPost = &models.FeedPost{
					ID:        uuid.NewString(),
					OrgID:     h.orgID,
					Text:      redacted,
					Hashtags:  tags,
					PostedOn:  now.Format("2006-01-02"),
					OptedIn:   true,
					Published: false,
				}
			}
		}
	}

	err = h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&response).Error; err != nil {
			return err
		}
		if analysis != nil {
			if err := tx.Create(analysis).Error; err != nil {
				return err
			}
		}
		if feedPost != nil {
			if err := tx.Create(feedPost).Error; err != nil {
				return err
			}
		}
		return tx.Create(&models.SurveySubmission{
			ID:          uuid.NewString(),
			OrgID:       h.orgID,
			UserID:      userID,
			PeriodID:    period.ID,
			SubmittedAt: now,
		}).Error
	})
	if err != nil {
		return err
	}

	// The receipt confirms the submission, never the submitter.
	return c.Status(fiber.StatusCreated).JSON(dto.SubmitResponseReceipt{SubmittedAt: now})
}

// classifySentiment and classifyCategories are keyword heuristics standing in for the
// analysis pipeline.
// ponytail: lexicon lookup, no negation handling; replace with the real classifier — the
// columns they write and every aggregate reading them stay the same.
func classifySentiment(text string) (label string, score float32) {
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
	total := positive + negative
	switch {
	case positive > negative:
		if total > 0 {
			score = float32(positive-negative) / float32(total)
		}
		return "pos", score
	case negative > positive:
		if total > 0 {
			score = float32(positive-negative) / float32(total)
		}
		return "neg", score
	default:
		return "neu", 0
	}
}

// classifyCategories tags a comment with topics from the fixed taxonomy by keyword match.
func classifyCategories(text string) []string {
	lowered := strings.ToLower(text)
	var categories []string
	for _, topicID := range topicOrder {
		for _, w := range topicKeywords[topicID] {
			if strings.Contains(lowered, w) {
				categories = append(categories, topicID)
				break
			}
		}
	}
	return categories
}

func confidenceFor(categories []string, sentimentScore float32) (confidence float32, low bool) {
	confidence = 0.5 + float32(len(categories))*0.1
	if sentimentScore < 0 {
		sentimentScore = -sentimentScore
	}
	confidence += sentimentScore * 0.1
	if confidence > 0.95 {
		confidence = 0.95
	}
	return confidence, confidence < 0.6
}

var (
	positiveWords = []string{"ดี", "ชอบ", "ขอบคุณ", "สนุก", "ประทับใจ", "ดีขึ้น", "สนับสนุน", "good", "great", "love", "thanks", "better"}
	negativeWords = []string{"หนัก", "เหนื่อย", "ไม่พอ", "ล่าช้า", "ไม่ชัดเจน", "ปัญหา", "แย่", "เครียด", "ไม่เป็นธรรม", "bad", "tired", "unclear", "problem", "stress", "overwork"}

	// topicOrder keeps classification output deterministic (map iteration order is not).
	topicOrder = []string{"work", "team", "manager", "compensation", "growth", "benefits"}

	topicKeywords = map[string][]string{
		"work":         {"งาน", "ภาระงาน", "กะดึก", "ot", "โอที", "workload", "overtime", "shift"},
		"team":         {"ทีม", "เพื่อนร่วมงาน", "team", "colleague"},
		"manager":      {"หัวหน้า", "ผู้จัดการ", "manager", "supervisor", "feedback", "ฟีดแบ็ก"},
		"compensation": {"เงินเดือน", "ค่าตอบแทน", "โบนัส", "salary", "compensation", "bonus", "pay"},
		"growth":       {"เติบโต", "โอกาส", "เส้นทางอาชีพ", "growth", "career", "promotion", "training", "ฝึกอบรม"},
		"benefits":     {"สวัสดิการ", "ประกัน", "ลาพัก", "benefits", "insurance", "leave", "wellness"},
	}
)
