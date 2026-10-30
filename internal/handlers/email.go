package handlers

import (
	"errors"
	"log"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"github.com/mt-sense/backend-service/internal/analytics"
	"github.com/mt-sense/backend-service/internal/dto"
	"github.com/mt-sense/backend-service/internal/mailer"
	"github.com/mt-sense/backend-service/internal/middleware"
	"github.com/mt-sense/backend-service/internal/models"
)

type EmailHandler struct {
	db    *gorm.DB
	stats *analytics.Service
	mail  *mailer.Service
}

func NewEmailHandler(db *gorm.DB, stats *analytics.Service, mail *mailer.Service) *EmailHandler {
	return &EmailHandler{db: db, stats: stats, mail: mail}
}

var leaderRoles = []models.Role{models.RoleAdmin, models.RoleExecutive}

// Status reports whether SMTP is configured and how many of the org's emails are waiting
// or failed recently.
func (h *EmailHandler) Status(c *fiber.Ctx) error {
	pending, failed, err := h.mail.QueueStats(middleware.OrgID(c))
	if err != nil {
		return err
	}
	return c.JSON(dto.EmailStatus{Configured: h.mail.Configured(), From: h.mail.From(), Pending: pending, Failed: failed})
}

// Recipients lists the org's active leaders, the only people a summary can be sent to.
func (h *EmailHandler) Recipients(c *fiber.Ctx) error {
	var users []models.User
	if err := h.db.Select("id", "full_name", "email", "role").
		Where("org_id = ? AND is_active = ? AND role IN ?", middleware.OrgID(c), true, leaderRoles).
		Order("full_name").Find(&users).Error; err != nil {
		return err
	}
	return c.JSON(dto.NewEmailRecipients(users))
}

// SendSummary queues the org-wide KPI summary of one period to chosen leaders.
func (h *EmailHandler) SendSummary(c *fiber.Ctx) error {
	var req dto.SendSummaryRequest
	if err := c.BodyParser(&req); err != nil {
		return fiber.NewError(fiber.StatusBadRequest, "malformed request body")
	}
	req.Normalize()
	if problems := req.Validate(); len(problems) > 0 {
		return c.Status(fiber.StatusBadRequest).JSON(dto.ValidationErrors{Errors: problems})
	}

	orgID := middleware.OrgID(c)
	stats := h.stats.WithOrg(orgID)
	var period *models.SurveyPeriod
	var err error
	if req.PeriodID != "" {
		period, err = stats.PeriodByID(req.PeriodID)
	} else {
		period, err = stats.LatestPeriod()
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fiber.NewError(fiber.StatusNotFound, "survey period not found")
	}
	if err != nil {
		return err
	}

	var recipients []models.User
	if err := h.db.Select("id", "org_id", "full_name", "email", "role").
		Where("org_id = ? AND is_active = ? AND role IN ? AND id IN ?", orgID, true, leaderRoles, req.RecipientIDs).
		Find(&recipients).Error; err != nil {
		return err
	}
	if len(recipients) != len(req.RecipientIDs) {
		return c.Status(fiber.StatusBadRequest).JSON(dto.ValidationErrors{
			Errors: []string{"recipients must be active HR or executive members of your organization"},
		})
	}

	var sender models.User
	if err := h.db.Select("id", "org_id", "full_name", "email").
		Where("org_id = ? AND id = ?", orgID, middleware.UserID(c)).First(&sender).Error; err != nil {
		return fiber.NewError(fiber.StatusUnauthorized, "account not found")
	}
	var org models.Organization
	if err := h.db.Select("id", "name").Where("id = ?", orgID).First(&org).Error; err != nil {
		return err
	}

	metrics, err := h.summaryMetrics(orgID, stats, period)
	if err != nil {
		return err
	}

	if err := h.db.Transaction(func(tx *gorm.DB) error {
		return h.mail.EnqueueSummary(tx, sender, org.Name, req.Note, metrics, recipients)
	}); err != nil {
		return err
	}
	h.mail.Notify()
	return c.Status(fiber.StatusAccepted).JSON(dto.SendSummaryReceipt{Queued: len(recipients)})
}

// summaryMetrics gathers the same org-wide numbers as the HR KPI cards. With fewer than
// models.MinGroupSize respondents only the response count is reported.
func (h *EmailHandler) summaryMetrics(orgID string, stats *analytics.Service, period *models.SurveyPeriod) (mailer.SummaryMetrics, error) {
	m := mailer.SummaryMetrics{Month: int(period.Month), Year: int(period.Year)}

	var err error
	if m.Respondents, err = stats.TotalRespondents(period.ID); err != nil {
		return m, err
	}
	_, total, rate, err := stats.ResponseRate(period.ID, "")
	if err != nil {
		return m, err
	}
	if total != nil && rate != nil {
		m.Headcount, m.ResponseRate, m.ResponseRateAvailable = *total, *rate, true
	}
	if m.Respondents < models.MinGroupSize {
		m.Suppressed = true
		return m, nil
	}

	if m.Satisfaction, err = stats.SatisfactionAverage(period.ID); err != nil {
		return m, err
	}
	enps, err := stats.ENPS(period.ID)
	if err != nil {
		return m, err
	}
	m.ENPS = enps.Value
	analyzed, err := stats.AnalyzedRespondents(period.ID)
	if err != nil {
		return m, err
	}
	if analyzed >= models.MinGroupSize {
		sentiment, err := stats.SentimentSplit(period.ID)
		if err != nil {
			return m, err
		}
		m.Positive, m.Neutral, m.Negative = sentiment.Positive, sentiment.Neutral, sentiment.Negative
		m.SentimentAvailable = true
	}
	if m.BurnoutRisk, m.DepartmentsAtRisk, err = stats.BurnoutRisk(period.ID); err != nil {
		return m, err
	}
	err = h.db.Model(&models.Alert{}).
		Where("org_id = ? AND period_id = ? AND resolved_at IS NULL", orgID, period.ID).
		Count(&m.OpenAlerts).Error
	return m, err
}

// SendTest emails the calling admin synchronously so SMTP problems surface immediately.
func (h *EmailHandler) SendTest(c *fiber.Ctx) error {
	var user models.User
	if err := h.db.Select("id", "full_name", "email").
		Where("org_id = ? AND id = ?", middleware.OrgID(c), middleware.UserID(c)).First(&user).Error; err != nil {
		return fiber.NewError(fiber.StatusUnauthorized, "account not found")
	}
	msg, err := h.mail.TestMessage(user)
	if err != nil {
		return err
	}
	if err := h.mail.SendNow(c.UserContext(), msg); err != nil {
		// The admin needs the SMTP reply to fix their settings; it never contains the
		// password, only the server's response text.
		log.Printf("mailer: test email to %s failed: %v", user.Email, err)
		return c.Status(fiber.StatusBadGateway).JSON(fiber.Map{"error": "SMTP: " + err.Error()})
	}
	return c.JSON(fiber.Map{"sentTo": user.Email, "configured": h.mail.Configured()})
}
