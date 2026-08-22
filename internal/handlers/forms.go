package handlers

import (
	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/mt-sense/backend-service/internal/dto"
	"github.com/mt-sense/backend-service/internal/middleware"
	"github.com/mt-sense/backend-service/internal/models"
	"github.com/mt-sense/backend-service/internal/privacy"
)

type FormsHandler struct{ db *gorm.DB }

func NewFormsHandler(db *gorm.DB) *FormsHandler { return &FormsHandler{db: db} }

func (h *FormsHandler) Create(c *fiber.Ctx) error {
	var req dto.SurveyFormRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "malformed request body")
	}
	if problems := req.Validate(); len(problems) > 0 {
		return c.Status(fiber.StatusUnprocessableEntity).JSON(dto.ValidationErrors{Errors: problems})
	}

	survey := req.ToModel(uuid.NewString())
	if err := h.db.Create(survey).Error; err != nil {
		return err
	}
	return c.Status(fiber.StatusCreated).JSON(dto.NewSurvey(survey))
}

func (h *FormsHandler) Update(c *fiber.Ctx) error {
	id := c.Params("id")

	var existing models.Survey
	if err := h.db.First(&existing, "id = ?", id).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "survey not found")
	}
	if existing.Status == "published" {
		return fiber.NewError(fiber.StatusConflict, "a published survey cannot be edited — create a new round instead")
	}

	var req dto.SurveyFormRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "malformed request body")
	}
	if problems := req.Validate(); len(problems) > 0 {
		return c.Status(fiber.StatusUnprocessableEntity).JSON(dto.ValidationErrors{Errors: problems})
	}

	survey := req.ToModel(id)
	survey.Status = existing.Status

	err := h.db.Transaction(func(tx *gorm.DB) error {
		// Steps and questions are replaced wholesale; cascade deletes clear the old tree.
		if err := tx.Where("survey_id = ?", id).Delete(&models.SurveyStep{}).Error; err != nil {
			return err
		}
		return tx.Session(&gorm.Session{FullSaveAssociations: true}).Save(survey).Error
	})
	if err != nil {
		return err
	}
	return c.JSON(dto.NewSurvey(survey))
}

// Publish opens the round and seeds the audit log with a "not submitted" row per employee,
// which is what later drives reminders — without ever touching response content.
func (h *FormsHandler) Publish(c *fiber.Ctx) error {
	id := c.Params("id")

	var survey models.Survey
	if err := h.db.First(&survey, "id = ?", id).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "survey not found")
	}

	var users []models.User
	if err := h.db.Find(&users).Error; err != nil {
		return err
	}

	err := h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&survey).Update("status", "published").Error; err != nil {
			return err
		}
		for _, u := range users {
			entry := models.AuditSubmissionLog{UserID: u.ID, SurveyID: id, Status: "not_submitted"}
			err := tx.Where(models.AuditSubmissionLog{UserID: u.ID, SurveyID: id}).
				FirstOrCreate(&entry).Error
			if err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}

	return c.JSON(dto.PublishResult{ID: id, Status: "published", Notified: len(users)})
}

// ValidateQuestion lets the builder check one question's text as HR types it.
func (h *FormsHandler) ValidateQuestion(c *fiber.Ctx) error {
	var req dto.ValidateQuestionRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "malformed request body")
	}
	return c.JSON(dto.ValidateQuestionResponse{LooksIdentifying: privacy.LooksIdentifying(req.Text)})
}

// List returns every survey for the HR forms screen.
func (h *FormsHandler) List(c *fiber.Ctx) error {
	var surveys []models.Survey
	err := h.db.
		Preload("Steps", func(db *gorm.DB) *gorm.DB { return db.Order("survey_steps.sort_order") }).
		Preload("Steps.Questions", func(db *gorm.DB) *gorm.DB { return db.Order("survey_questions.sort_order") }).
		Order("created_at DESC").Find(&surveys).Error
	if err != nil {
		return err
	}
	return c.JSON(dto.NewSurveyList(surveys))
}

// CreateActionItem records a follow-up. Executives may only create decision-level items;
// HR owns the operational ones.
func (h *FormsHandler) CreateActionItem(c *fiber.Ctx) error {
	var req dto.CreateActionItemRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "malformed request body")
	}
	if problems := req.Validate(); len(problems) > 0 {
		return c.Status(fiber.StatusBadRequest).JSON(dto.ValidationErrors{Errors: problems})
	}

	role := middleware.CurrentRole(c)
	level := "full"
	if role == models.RoleExecutive {
		level = "decision"
	}

	item := models.ActionItem{
		ID:         uuid.NewString(),
		Topic:      req.Topic,
		TopicID:    req.TopicID,
		Assignee:   req.Assignee,
		Status:     "in_progress",
		CreatedBy:  role,
		Level:      level,
		TargetDate: req.TargetDate,
	}
	if err := h.db.Create(&item).Error; err != nil {
		return err
	}
	return c.Status(fiber.StatusCreated).JSON(dto.NewActionItem(&item))
}
