package handlers

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/mt-sense/backend-service/internal/analysisqueue"
	"github.com/mt-sense/backend-service/internal/analytics"
	"github.com/mt-sense/backend-service/internal/dto"
	"github.com/mt-sense/backend-service/internal/middleware"
	"github.com/mt-sense/backend-service/internal/models"
	"github.com/mt-sense/backend-service/internal/privacy"
)

type SurveyHandler struct {
	db    *gorm.DB
	stats *analytics.Service
	queue *analysisqueue.Service
}

func NewSurveyHandler(db *gorm.DB, stats *analytics.Service, queue *analysisqueue.Service) *SurveyHandler {
	return &SurveyHandler{db: db, stats: stats, queue: queue}
}

// Current returns the currently open survey period, if any, and whether the caller already
// submitted it this period.
func (h *SurveyHandler) Current(c *fiber.Ctx) error {
	period, err := h.stats.WithOrg(middleware.OrgID(c)).CurrentOpenPeriod()
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

// Catalog returns the fixed set of optional extra questions HR can toggle on/off per round
// (see dto.ExtraQuestionCatalog) — static, no DB access, available to any authenticated role
// since both HR (choosing what to enable) and employees (rendering enabled questions) need it.
func (h *SurveyHandler) Catalog(c *fiber.Ctx) error {
	return c.JSON(dto.ExtraQuestionCatalog)
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
	orgID := middleware.OrgID(c)

	var req dto.SubmitResponseRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "malformed request body")
	}
	if problems := req.Validate(); len(problems) > 0 {
		return c.Status(fiber.StatusBadRequest).JSON(dto.ValidationErrors{Errors: problems})
	}

	period, err := h.stats.WithOrg(orgID).CurrentOpenPeriod()
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
		OrgID:             orgID,
		PeriodID:          period.ID,
		DepartmentID:      user.DepartmentID,
		PositionID:        user.PositionID,
		SatisfactionScore: int16(req.SatisfactionScore),
		SubmittedAt:       now,
		AnalysisStatus:    "skipped",
	}

	var feedPost *models.FeedPost

	comment := strings.TrimSpace(req.CommentText)
	if comment != "" {
		// Redact before anything is persisted — the raw text never reaches a table.
		redacted := privacy.Redact(comment)
		response.CommentText = redacted

		if redacted != "" {
			response.AnalysisStatus = "pending"

			// Gate one of the feed flow: the author opted in. Gate two (moderation) is a
			// separate HR action, so the post starts unpublished.
			if req.OptedInToFeed {
				feedPost = &models.FeedPost{
					ID:        uuid.NewString(),
					OrgID:     orgID,
					Text:      redacted,
					Hashtags:  req.Tags,
					PostedOn:  now.Format("2006-01-02"),
					OptedIn:   true,
					Published: false,
				}
			}
		}
	}

	// Only answers to questions this specific period actually enabled are kept — a stale
	// client sending answers for a question HR turned off (or never turned on) is silently
	// dropped rather than rejected, since the enabled set can legitimately change between
	// when the employee opened the form and when they submit it.
	enabledExtra := make(map[string]bool, len(period.EnabledExtraQuestions))
	for _, k := range period.EnabledExtraQuestions {
		enabledExtra[k] = true
	}
	var extraAnswers []models.ExtraAnswer
	for key, value := range req.ExtraAnswers {
		if !enabledExtra[key] {
			continue
		}
		extraAnswers = append(extraAnswers, models.ExtraAnswer{
			ID:          uuid.NewString(),
			ResponseID:  response.ID,
			QuestionKey: key,
			Value:       int16(value),
		})
	}

	err = h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&response).Error; err != nil {
			return err
		}
		if feedPost != nil {
			if err := tx.Create(feedPost).Error; err != nil {
				return err
			}
		}
		if len(extraAnswers) > 0 {
			if err := tx.Create(&extraAnswers).Error; err != nil {
				return err
			}
		}
		return tx.Create(&models.SurveySubmission{
			ID:          uuid.NewString(),
			OrgID:       orgID,
			UserID:      userID,
			PeriodID:    period.ID,
			SubmittedAt: now,
		}).Error
	})
	if err != nil {
		return err
	}
	if response.AnalysisStatus == "pending" {
		h.queue.Notify()
	}

	// The receipt confirms the submission, never the submitter.
	return c.Status(fiber.StatusCreated).JSON(dto.SubmitResponseReceipt{SubmittedAt: now})
}
