package handlers

import (
	"errors"
	"log"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/mt-sense/backend-service/internal/aiservice"
	"github.com/mt-sense/backend-service/internal/analysisqueue"
	"github.com/mt-sense/backend-service/internal/analytics"
	"github.com/mt-sense/backend-service/internal/dto"
	"github.com/mt-sense/backend-service/internal/knowledgebase"
	"github.com/mt-sense/backend-service/internal/middleware"
	"github.com/mt-sense/backend-service/internal/models"
)

// PeriodsHandler replaces the old form-builder handler: under the fixed satisfaction_score +
// comment_text survey model there is no form to author, only monthly rounds to schedule.
type PeriodsHandler struct {
	db    *gorm.DB
	stats *analytics.Service
	ai    *aiservice.Client
	kb    *knowledgebase.Service
	queue *analysisqueue.Service
}

func NewPeriodsHandler(db *gorm.DB, stats *analytics.Service, ai *aiservice.Client, kb *knowledgebase.Service, queue *analysisqueue.Service) *PeriodsHandler {
	return &PeriodsHandler{db: db, stats: stats, ai: ai, kb: kb, queue: queue}
}

// List returns every survey period for the org, newest first, with response counts.
func (h *PeriodsHandler) List(c *fiber.Ctx) error {
	orgID := middleware.OrgID(c)
	stats := h.stats.WithOrg(orgID)

	var periods []models.SurveyPeriod
	if err := h.db.Where("org_id = ?", orgID).Order("year DESC, month DESC").Find(&periods).Error; err != nil {
		return err
	}

	counts := make(map[string]int64, len(periods))
	for _, p := range periods {
		n, err := stats.TotalRespondents(p.ID)
		if err != nil {
			return err
		}
		counts[p.ID] = n
	}

	return c.JSON(dto.NewSurveyPeriodList(periods, counts))
}

// Create opens a new monthly round. If opens/closes are not supplied, it opens now and
// closes one calendar month later.
func (h *PeriodsHandler) Create(c *fiber.Ctx) error {
	var req dto.CreatePeriodRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "malformed request body")
	}
	if problems := req.Validate(); len(problems) > 0 {
		return c.Status(fiber.StatusBadRequest).JSON(dto.ValidationErrors{Errors: problems})
	}

	opens := time.Now()
	if req.OpensAt != nil {
		opens = *req.OpensAt
	}
	closes := opens.AddDate(0, 1, 0)
	if req.ClosesAt != nil {
		closes = *req.ClosesAt
	}

	period := models.SurveyPeriod{
		ID:                    uuid.NewString(),
		OrgID:                 middleware.OrgID(c),
		Month:                 int16(req.Month),
		Year:                  int16(req.Year),
		OpensAt:               opens,
		ClosesAt:              closes,
		EnabledExtraQuestions: req.EnabledExtraQuestions,
	}
	if err := h.db.Create(&period).Error; err != nil {
		return err
	}
	return c.Status(fiber.StatusCreated).JSON(dto.NewSurveyPeriod(&period, false, 0))
}

// Close ends a period early (sets closes_at to now if it hasn't passed yet) and generates
// its alerts.
func (h *PeriodsHandler) Close(c *fiber.Ctx) error {
	orgID := middleware.OrgID(c)
	stats := h.stats.WithOrg(orgID)

	var period models.SurveyPeriod
	if err := h.db.Where("org_id = ? AND id = ?", orgID, c.Params("id")).First(&period).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "survey period not found")
	}

	now := time.Now()
	if period.ClosesAt.After(now) {
		period.ClosesAt = now
		if err := h.db.Model(&period).Update("closes_at", now).Error; err != nil {
			return err
		}
	}
	if err := h.queue.FlushPeriod(c.UserContext(), orgID, period.ID); err != nil {
		log.Printf("analysis queue flush failed while closing period %s: %v", period.ID, err)
		return fiber.NewError(fiber.StatusServiceUnavailable, "the period is closed, but pending comments could not be analyzed; retry closing the period")
	}

	if err := stats.GenerateAlerts(&period); err != nil {
		return err
	}

	// Knowledge compilation is best-effort here: closing a survey must not depend on an
	// external LLM being available. HR can retry explicitly via /api/knowledge-base/:id/compile.
	if h.kb != nil {
		if _, _, err := h.kb.CompilePeriod(c.UserContext(), orgID, period.ID, false); err != nil && !errors.Is(err, knowledgebase.ErrInsufficientData) {
			log.Printf("knowledge compilation after period close failed for %s: %v", period.ID, err)
		}
	}

	n, err := stats.TotalRespondents(period.ID)
	if err != nil {
		return err
	}
	return c.JSON(dto.NewSurveyPeriod(&period, false, n))
}
