package handlers

import (
	"errors"
	"strings"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"github.com/mt-sense/backend-service/internal/aiservice"
	"github.com/mt-sense/backend-service/internal/middleware"
	"github.com/mt-sense/backend-service/internal/models"
)

const maxTrainingWorkbookBytes = 10 << 20
const trainerEmail = "test@kmitl.ac.th"

type ModelTrainingHandler struct {
	db    *gorm.DB
	ai    *aiservice.Client
	token string
}

func NewModelTrainingHandler(db *gorm.DB, ai *aiservice.Client, token string) *ModelTrainingHandler {
	return &ModelTrainingHandler{db: db, ai: ai, token: token}
}

func (h *ModelTrainingHandler) Train(c *fiber.Ctx) error {
	var user models.User
	if err := h.db.Where("id = ? AND org_id = ? AND is_active = ?", middleware.UserID(c), middleware.OrgID(c), true).First(&user).Error; err != nil {
		return fiber.NewError(fiber.StatusForbidden, "trainer account not found")
	}
	if !isDesignatedTrainer(&user) {
		return fiber.NewError(fiber.StatusForbidden, "only the designated HR account can train the model")
	}
	if h.token == "" {
		return fiber.NewError(fiber.StatusServiceUnavailable, "AI training token is not configured")
	}
	data := c.Body()
	if len(data) == 0 || len(data) > maxTrainingWorkbookBytes || len(data) < 4 || string(data[:2]) != "PK" {
		return fiber.NewError(fiber.StatusBadRequest, "upload an .xlsx workbook smaller than 10 MB")
	}
	report, err := h.ai.Train(c.UserContext(), data, h.token)
	if err != nil {
		var trainingErr *aiservice.TrainingError
		if errors.As(err, &trainingErr) {
			switch trainingErr.StatusCode {
			case fiber.StatusBadRequest, fiber.StatusConflict, fiber.StatusRequestEntityTooLarge:
				return fiber.NewError(trainingErr.StatusCode, trainingErr.Detail)
			case fiber.StatusServiceUnavailable:
				return fiber.NewError(fiber.StatusServiceUnavailable, "AI training is not configured")
			}
		}
		return fiber.NewError(fiber.StatusBadGateway, "AI service could not train the model")
	}
	return c.JSON(report)
}

func isDesignatedTrainer(user *models.User) bool {
	return user.Role == models.RoleAdmin && strings.EqualFold(strings.TrimSpace(user.Email), trainerEmail)
}
