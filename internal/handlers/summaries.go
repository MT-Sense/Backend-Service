package handlers

import (
	"errors"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/mt-sense/backend-service/internal/dto"
	"github.com/mt-sense/backend-service/internal/middleware"
	"github.com/mt-sense/backend-service/internal/models"
)

// SummariesHandler lets HR write the summaries employees see in the feed sidebar. Drafts
// stay HR-only; the public list (FeedHandler.Summaries) returns published rows only.
type SummariesHandler struct {
	db *gorm.DB
}

func NewSummariesHandler(db *gorm.DB) *SummariesHandler {
	return &SummariesHandler{db: db}
}

// List returns every summary of the org, drafts first, then newest.
func (h *SummariesHandler) List(c *fiber.Ctx) error {
	var rows []models.PublishedSummary
	if err := h.db.Where("org_id = ?", middleware.OrgID(c)).
		Order("CASE status WHEN 'draft' THEN 0 ELSE 1 END, published_at DESC, created_at DESC").
		Find(&rows).Error; err != nil {
		return err
	}
	return c.JSON(dto.NewPublishedSummaries(rows))
}

func (h *SummariesHandler) Create(c *fiber.Ctx) error {
	req, err := h.parse(c)
	if errors.Is(err, errResponded) {
		return nil
	}
	if err != nil {
		return err
	}
	now := time.Now()
	userID := middleware.UserID(c)
	row := models.PublishedSummary{
		ID:        uuid.NewString(),
		OrgID:     middleware.OrgID(c),
		CreatedBy: &userID,
		CreatedAt: &now,
	}
	applySummary(&row, req, now)
	if err := h.db.Create(&row).Error; err != nil {
		return err
	}
	return c.Status(fiber.StatusCreated).JSON(dto.NewPublishedSummary(&row))
}

func (h *SummariesHandler) Update(c *fiber.Ctx) error {
	req, err := h.parse(c)
	if errors.Is(err, errResponded) {
		return nil
	}
	if err != nil {
		return err
	}
	row, err := h.find(c)
	if err != nil {
		return err
	}
	applySummary(row, req, time.Now())
	if err := h.db.Model(row).Select("title", "body", "status", "period_id", "published_at", "updated_at").Updates(row).Error; err != nil {
		return err
	}
	return c.JSON(dto.NewPublishedSummary(row))
}

func (h *SummariesHandler) Delete(c *fiber.Ctx) error {
	row, err := h.find(c)
	if err != nil {
		return err
	}
	if err := h.db.Delete(row).Error; err != nil {
		return err
	}
	return c.SendStatus(fiber.StatusNoContent)
}

func (h *SummariesHandler) parse(c *fiber.Ctx) (*dto.SaveSummaryRequest, error) {
	var req dto.SaveSummaryRequest
	if err := c.BodyParser(&req); err != nil {
		return nil, fiber.NewError(fiber.StatusBadRequest, "malformed request body")
	}
	req.Normalize()
	if problems := req.Validate(); len(problems) > 0 {
		if err := c.Status(fiber.StatusBadRequest).JSON(dto.ValidationErrors{Errors: problems}); err != nil {
			return nil, err
		}
		return nil, errResponded
	}
	if req.PeriodID != "" {
		var n int64
		if err := h.db.Model(&models.SurveyPeriod{}).
			Where("org_id = ? AND id = ?", middleware.OrgID(c), req.PeriodID).Count(&n).Error; err != nil {
			return nil, err
		}
		if n == 0 {
			return nil, fiber.NewError(fiber.StatusBadRequest, "survey period not found")
		}
	}
	return &req, nil
}

func (h *SummariesHandler) find(c *fiber.Ctx) (*models.PublishedSummary, error) {
	var row models.PublishedSummary
	err := h.db.Where("org_id = ? AND id = ?", middleware.OrgID(c), c.Params("id")).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fiber.NewError(fiber.StatusNotFound, "summary not found")
	}
	return &row, err
}

// applySummary copies the request onto row. PublishedAt is stamped the first time a summary
// becomes published and cleared when it goes back to draft.
func applySummary(row *models.PublishedSummary, req *dto.SaveSummaryRequest, now time.Time) {
	row.Title = req.Title
	row.Body = req.Body
	row.PeriodID = nil
	if req.PeriodID != "" {
		periodID := req.PeriodID
		row.PeriodID = &periodID
	}
	if req.Status == models.SummaryPublished && (row.Status != models.SummaryPublished || row.PublishedAt == "") {
		row.PublishedAt = now.Format("2006-01-02")
	}
	if req.Status == models.SummaryDraft {
		row.PublishedAt = ""
	}
	row.Status = req.Status
	row.UpdatedAt = &now
}

// errResponded means the helper already wrote the response (a 400 with validation errors).
var errResponded = errors.New("response already written")
