package handlers

import (
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/mt-sense/backend-service/internal/auth"
	"github.com/mt-sense/backend-service/internal/middleware"
	"github.com/mt-sense/backend-service/internal/models"
)

type DepartmentsHandler struct{ db *gorm.DB }

func NewDepartmentsHandler(db *gorm.DB) *DepartmentsHandler { return &DepartmentsHandler{db: db} }

func validDepartmentName(name string) bool {
	n := utf8.RuneCountInString(name)
	return n >= 2 && n <= 100
}

type departmentWithCode struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	JoinCode string `json:"joinCode"`
}

func (h *DepartmentsHandler) List(c *fiber.Ctx) error {
	var departments []models.Department
	if err := h.db.Where("org_id = ?", middleware.OrgID(c)).Order("name").Find(&departments).Error; err != nil {
		return err
	}
	out := make([]departmentWithCode, 0, len(departments))
	for _, d := range departments {
		code := ""
		if d.JoinCode != nil {
			code = *d.JoinCode
		}
		out = append(out, departmentWithCode{ID: d.ID, Name: d.Name, JoinCode: code})
	}
	return c.JSON(out)
}

func (h *DepartmentsHandler) Create(c *fiber.Ctx) error {
	var req struct {
		Name string `json:"name"`
	}
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "malformed request body")
	}
	req.Name = strings.TrimSpace(req.Name)
	if !validDepartmentName(req.Name) {
		return fiber.NewError(fiber.StatusBadRequest, "department name must be 2-100 characters")
	}
	orgID := middleware.OrgID(c)
	for attempt := 0; attempt < 5; attempt++ {
		code, err := auth.DepartmentCode()
		if err != nil {
			return err
		}
		department := models.Department{ID: uuid.NewString(), OrgID: orgID, Name: req.Name, JoinCode: &code}
		err = h.db.Transaction(func(tx *gorm.DB) error {
			if err := tx.Create(&department).Error; err != nil {
				return err
			}
			return tx.Model(&models.Organization{}).Where("id = ?", orgID).Update("collect_department", true).Error
		})
		if err == nil {
			return c.Status(fiber.StatusCreated).JSON(departmentWithCode{ID: department.ID, Name: department.Name, JoinCode: code})
		}
		if !isUniqueViolation(err) {
			return err
		}
		var sameName int64
		if err := h.db.Model(&models.Department{}).Where("org_id = ? AND name = ?", orgID, req.Name).Count(&sameName).Error; err != nil {
			return err
		}
		if sameName > 0 {
			return fiber.NewError(fiber.StatusConflict, "department name already exists")
		}
	}
	return fiber.NewError(fiber.StatusInternalServerError, "could not allocate department code")
}

func (h *DepartmentsHandler) Update(c *fiber.Ctx) error {
	var req struct {
		Name string `json:"name"`
	}
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "malformed request body")
	}
	req.Name = strings.TrimSpace(req.Name)
	if !validDepartmentName(req.Name) {
		return fiber.NewError(fiber.StatusBadRequest, "department name must be 2-100 characters")
	}

	orgID := middleware.OrgID(c)
	var department models.Department
	if err := h.db.Where("id = ? AND org_id = ?", c.Params("id"), orgID).First(&department).Error; errors.Is(err, gorm.ErrRecordNotFound) {
		return fiber.NewError(fiber.StatusNotFound, "department not found")
	} else if err != nil {
		return err
	}
	if department.Name == req.Name {
		return c.JSON(departmentWithCode{ID: department.ID, Name: department.Name, JoinCode: departmentCode(department)})
	}
	if err := h.db.Model(&department).Where("org_id = ?", orgID).Update("name", req.Name).Error; err != nil {
		if isUniqueViolation(err) {
			return fiber.NewError(fiber.StatusConflict, "department name already exists")
		}
		return err
	}
	department.Name = req.Name
	return c.JSON(departmentWithCode{ID: department.ID, Name: department.Name, JoinCode: departmentCode(department)})
}

func departmentCode(department models.Department) string {
	if department.JoinCode != nil {
		return *department.JoinCode
	}
	return ""
}

func (h *DepartmentsHandler) Delete(c *fiber.Ctx) error {
	orgID := middleware.OrgID(c)
	err := h.db.Transaction(func(tx *gorm.DB) error {
		var department models.Department
		if err := tx.Where("id = ? AND org_id = ?", c.Params("id"), orgID).First(&department).Error; errors.Is(err, gorm.ErrRecordNotFound) {
			return fiber.NewError(fiber.StatusNotFound, "department not found")
		} else if err != nil {
			return err
		}

		// Keep names and historical groupings intact while any records reference this department.
		for _, reference := range []struct {
			model any
			field string
		}{
			{&models.User{}, "department_id"},
			{&models.SurveyResponse{}, "department_id"},
			{&models.Alert{}, "related_department_id"},
			{&models.UrgentIssue{}, "department_id"},
		} {
			var count int64
			if err := tx.Model(reference.model).Where("org_id = ? AND "+reference.field+" = ?", orgID, department.ID).Count(&count).Error; err != nil {
				return err
			}
			if count > 0 {
				return fiber.NewError(fiber.StatusConflict, "department has employees or historical records")
			}
		}
		return tx.Where("id = ? AND org_id = ?", department.ID, orgID).Delete(&models.Department{}).Error
	})
	if err != nil {
		return err
	}
	return c.SendStatus(fiber.StatusNoContent)
}
