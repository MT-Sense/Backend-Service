package handlers

import (
	"database/sql"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"github.com/mt-sense/backend-service/internal/middleware"
	"github.com/mt-sense/backend-service/internal/models"
)

type departmentPage struct {
	Items    []departmentWithCode `json:"items"`
	Total    int64                `json:"total"`
	Page     int                  `json:"page"`
	PageSize int                  `json:"pageSize"`
}

func (h *DepartmentsHandler) ListPage(c *fiber.Ctx) error {
	page, size, err := parsePagination(c.Query("page"), c.Query("pageSize"))
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	}
	orgID := middleware.OrgID(c)
	result := departmentPage{Items: []departmentWithCode{}, Page: page, PageSize: size}
	err = h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.Department{}).Where("org_id = ?", orgID).Count(&result.Total).Error; err != nil {
			return err
		}
		pages := (result.Total + int64(size) - 1) / int64(size)
		if pages < 1 {
			pages = 1
		}
		if int64(result.Page) > pages {
			result.Page = int(pages)
		}
		if result.Total == 0 {
			return nil
		}
		var departments []models.Department
		if err := tx.Where("org_id = ?", orgID).Order("name, id").
			Limit(size).Offset((result.Page - 1) * size).Find(&departments).Error; err != nil {
			return err
		}
		for _, department := range departments {
			result.Items = append(result.Items, newDepartmentWithCode(department))
		}
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return err
	}
	return c.JSON(result)
}
