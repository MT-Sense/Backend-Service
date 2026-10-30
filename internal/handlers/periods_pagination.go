package handlers

import (
	"database/sql"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"github.com/mt-sense/backend-service/internal/dto"
	"github.com/mt-sense/backend-service/internal/middleware"
	"github.com/mt-sense/backend-service/internal/models"
)

// periodResponseCounts uses one grouped query instead of one query per period.
func periodResponseCounts(db *gorm.DB, orgID string, periods []models.SurveyPeriod) (map[string]int64, error) {
	counts := make(map[string]int64, len(periods))
	if len(periods) == 0 {
		return counts, nil
	}
	ids := make([]string, 0, len(periods))
	for _, period := range periods {
		ids = append(ids, period.ID)
	}
	var rows []struct {
		PeriodID string
		Count    int64
	}
	err := db.Model(&models.SurveyResponse{}).
		Select("period_id, COUNT(*) AS count").
		Where("org_id = ? AND period_id IN ?", orgID, ids).
		Group("period_id").Scan(&rows).Error
	for _, row := range rows {
		counts[row.PeriodID] = row.Count
	}
	return counts, err
}

// ListPage preserves the legacy array endpoint when pagination parameters are absent.
// This path reads only the selected page and its response counts.
func (h *PeriodsHandler) ListPage(c *fiber.Ctx) error {
	page, size, err := parsePagination(c.Query("page"), c.Query("pageSize"))
	if err != nil {
		return fiber.NewError(fiber.StatusBadRequest, err.Error())
	}
	orgID := middleware.OrgID(c)
	result := dto.SurveyPeriodPage{Items: []dto.SurveyPeriod{}, Page: page, PageSize: size}
	err = h.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&models.SurveyPeriod{}).Where("org_id = ?", orgID).Count(&result.Total).Error; err != nil {
			return err
		}
		pageCount := (result.Total + int64(size) - 1) / int64(size)
		if pageCount < 1 {
			pageCount = 1
		}
		if int64(result.Page) > pageCount {
			result.Page = int(pageCount)
		}
		if result.Total == 0 {
			return nil
		}
		var latest models.SurveyPeriod
		if err := tx.Select("month, year").Where("org_id = ?", orgID).
			Order("year DESC, month DESC").First(&latest).Error; err != nil {
			return err
		}
		result.LatestPeriod = &dto.LatestPeriod{Month: int(latest.Month), Year: int(latest.Year)}
		var periods []models.SurveyPeriod
		if err := tx.Where("org_id = ?", orgID).Order("year DESC, month DESC, id DESC").
			Limit(size).Offset((result.Page - 1) * size).Find(&periods).Error; err != nil {
			return err
		}
		counts, err := periodResponseCounts(tx, orgID, periods)
		if err != nil {
			return err
		}
		result.Items = dto.NewSurveyPeriodList(periods, counts)
		return nil
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return err
	}
	return c.JSON(result)
}
