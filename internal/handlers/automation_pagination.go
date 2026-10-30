package handlers

import (
	"database/sql"
	"slices"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"github.com/mt-sense/backend-service/internal/middleware"
	"github.com/mt-sense/backend-service/internal/models"
)

type assistantPage[T any] struct {
	Items    []T   `json:"items"`
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"pageSize"`
}

type proposalStats struct {
	Total   int64 `json:"total"`
	Pending int64 `json:"pending"`
	Active  int64 `json:"active"`
}

func assistantStatus(c *fiber.Ctx, allowed []string) (string, error) {
	status := c.Query("status")
	if status == "" || status == "all" {
		return "", nil
	}
	if !slices.Contains(allowed, status) {
		return "", fiber.NewError(400, "invalid status")
	}
	return status, nil
}

// Count and rows share a snapshot. The ID tie-breaker keeps equal timestamps stable.
func readAssistantPage[T any](q *gorm.DB, status string, result *assistantPage[T]) error {
	if status != "" {
		q = q.Where("status = ?", status)
	}
	if err := q.Count(&result.Total).Error; err != nil {
		return err
	}
	pages := max(int64(1), (result.Total+int64(result.PageSize)-1)/int64(result.PageSize))
	if int64(result.Page) > pages {
		result.Page = int(pages)
	}
	return q.Order("created_at DESC, id DESC").Limit(result.PageSize).
		Offset((result.Page - 1) * result.PageSize).Find(&result.Items).Error
}

func (h *AutomationHandler) ListProposalPage(c *fiber.Ctx) error {
	page, size, err := parsePagination(c.Query("page"), c.Query("pageSize"))
	if err != nil {
		return fiber.NewError(400, err.Error())
	}
	status, err := assistantStatus(c, []string{"pending", "approved", "rejected", "executing", "needs_check", "executed", "in_progress", "completed"})
	if err != nil {
		return err
	}
	result := struct {
		assistantPage[models.AutomationProposal]
		Stats proposalStats `json:"stats"`
	}{assistantPage: assistantPage[models.AutomationProposal]{Items: []models.AutomationProposal{}, Page: page, PageSize: size}}
	err = h.db.Transaction(func(tx *gorm.DB) error {
		base := func() *gorm.DB {
			q := tx.Model(&models.AutomationProposal{}).Where("org_id = ? AND demo = false", middleware.OrgID(c))
			if period := c.Query("period"); period != "" {
				q = q.Where("period_id = ?", period)
			}
			return q
		}
		if err := base().Select("COUNT(*) AS total, COUNT(*) FILTER (WHERE status = 'pending') AS pending, COUNT(*) FILTER (WHERE status IN ('in_progress', 'executing', 'needs_check')) AS active").Scan(&result.Stats).Error; err != nil {
			return err
		}
		return readAssistantPage(base(), status, &result.assistantPage)
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return err
	}
	for i := range result.Items {
		result.Items[i].MissingData = uniqueNonEmptyStrings(result.Items[i].MissingData)
	}
	return c.JSON(result)
}

func (h *AutomationHandler) ListRoutingCasePage(c *fiber.Ctx) error {
	page, size, err := parsePagination(c.Query("page"), c.Query("pageSize"))
	if err != nil {
		return fiber.NewError(400, err.Error())
	}
	status, err := assistantStatus(c, []string{"triage", "routed", "in_progress", "completed"})
	if err != nil {
		return err
	}
	result := assistantPage[models.RoutingCase]{Items: []models.RoutingCase{}, Page: page, PageSize: size}
	err = h.db.Transaction(func(tx *gorm.DB) error {
		q := tx.Model(&models.RoutingCase{}).Where("org_id = ? AND title NOT LIKE ?", middleware.OrgID(c), "[ตัวอย่าง]%")
		return readAssistantPage(q, status, &result)
	}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return err
	}
	return c.JSON(result)
}
