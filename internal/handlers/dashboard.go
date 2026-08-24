package handlers

import (
	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"github.com/mt-sense/backend-service/internal/analytics"
	"github.com/mt-sense/backend-service/internal/dto"
	"github.com/mt-sense/backend-service/internal/middleware"
	"github.com/mt-sense/backend-service/internal/models"
	"github.com/mt-sense/backend-service/internal/privacy"
)

type DashboardHandler struct {
	db    *gorm.DB
	stats *analytics.Service
}

func NewDashboardHandler(db *gorm.DB, stats *analytics.Service) *DashboardHandler {
	return &DashboardHandler{db: db, stats: stats}
}

// resolvePeriod reads ?period=<id>, defaulting to the org's latest survey period.
func (h *DashboardHandler) resolvePeriod(c *fiber.Ctx, stats *analytics.Service) (*models.SurveyPeriod, error) {
	if id := c.Query("period"); id != "" {
		return stats.PeriodByID(id)
	}
	return stats.LatestPeriod()
}

// HRKpis serves the five KPI cards plus the six-month trend.
func (h *DashboardHandler) HRKpis(c *fiber.Ctx) error {
	orgID := middleware.OrgID(c)
	stats := h.stats.WithOrg(orgID)
	period, err := h.resolvePeriod(c, stats)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "no survey period found")
	}

	enps, err := stats.ENPS(period.ID)
	if err != nil {
		return err
	}
	prevENPSValue := 0
	if prev, err := stats.PreviousPeriod(period); err != nil {
		return err
	} else if prev != nil {
		prevENPS, err := stats.ENPS(prev.ID)
		if err != nil {
			return err
		}
		prevENPSValue = prevENPS.Value
	}

	satisfaction, err := stats.SatisfactionAverage(period.ID)
	if err != nil {
		return err
	}
	prevSatisfaction := 0.0
	if prev, err := stats.PreviousPeriod(period); err != nil {
		return err
	} else if prev != nil {
		prevSatisfaction, err = stats.SatisfactionAverage(prev.ID)
		if err != nil {
			return err
		}
	}

	burnoutPct, deptsAtRisk, err := stats.BurnoutRisk(period.ID)
	if err != nil {
		return err
	}
	sentiment, err := stats.SentimentSplit(period.ID)
	if err != nil {
		return err
	}
	trend, err := stats.Trend(period, 6)
	if err != nil {
		return err
	}

	responded, err := stats.TotalRespondents(period.ID)
	if err != nil {
		return err
	}
	var headcount int64
	if err := h.db.Model(&models.User{}).Where("org_id = ? AND is_active = ?", orgID, true).Count(&headcount).Error; err != nil {
		return err
	}
	responseRate := 0
	if headcount > 0 {
		responseRate = int(float64(responded) / float64(headcount) * 100)
	}

	return c.JSON(dto.HrKpis{
		Enps: dto.EnpsKpi{
			Value:            enps.Value,
			DeltaVsLastMonth: enps.Value - prevENPSValue,
		},
		Satisfaction: dto.SatisfactionKpi{
			Value: satisfaction,
			Trend: trendDirection(satisfaction, prevSatisfaction),
		},
		BurnoutRisk: dto.BurnoutKpi{
			Percentage:        burnoutPct,
			DepartmentsAtRisk: deptsAtRisk,
		},
		ResponseRate: dto.ResponseRateKpi{
			Percentage: responseRate,
			Responded:  responded,
			Total:      headcount,
		},
		Sentiment: sentiment,
		Trend:     dto.NewTrend(trend),
	})
}

func trendDirection(current, previous float64) string {
	switch {
	case current > previous+0.05:
		return "up"
	case current < previous-0.05:
		return "down"
	default:
		return "flat"
	}
}

// Heatmap returns department rows × topic columns. Departments under the n<5 threshold
// come back with respondentCount set and every cell suppressed, so the client renders its
// "hidden for privacy" row without ever having received a score.
func (h *DashboardHandler) Heatmap(c *fiber.Ctx) error {
	orgID := middleware.OrgID(c)
	stats := h.stats.WithOrg(orgID)
	period, err := h.resolvePeriod(c, stats)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "no survey period found")
	}

	var departments []models.Department
	if err := h.db.Where("org_id = ?", orgID).Order("id").Find(&departments).Error; err != nil {
		return err
	}
	var topics []models.Topic
	if err := h.db.Order("sort_order, id").Find(&topics).Error; err != nil {
		return err
	}

	scores, err := stats.DepartmentTopicScores(period.ID, topics)
	if err != nil {
		return err
	}
	counts, err := stats.RespondentCounts(period.ID)
	if err != nil {
		return err
	}

	byDeptTopic := make(map[string]float64, len(scores))
	for _, s := range scores {
		byDeptTopic[s.DepartmentID+":"+s.TopicID] = s.Score
	}

	rows := make([]dto.HeatmapRow, 0, len(departments))
	for _, d := range departments {
		n := counts[d.ID]
		cells := make([]dto.HeatmapCell, 0, len(topics))
		for _, t := range topics {
			cell := dto.HeatmapCell{TopicID: t.ID}
			if score, ok := byDeptTopic[d.ID+":"+t.ID]; ok {
				cell.Score = privacy.Suppress(n, score)
			} else {
				cell.Score = models.Hidden[float64]()
			}
			cells = append(cells, cell)
		}
		rows = append(rows, dto.HeatmapRow{
			Department: dto.Department{ID: d.ID, Name: d.Name, RespondentCount: n},
			Cells:      cells,
		})
	}

	return c.JSON(dto.Heatmap{Topics: dto.NewTopics(topics), Rows: rows})
}

// WordCloud serves terms from the analysis pipeline's output table.
func (h *DashboardHandler) WordCloud(c *fiber.Ctx) error {
	orgID := middleware.OrgID(c)
	stats := h.stats.WithOrg(orgID)
	period, err := h.resolvePeriod(c, stats)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "no survey period found")
	}
	var terms []models.WordCloudTerm
	err = h.db.Where("org_id = ? AND period_id = ?", orgID, period.ID).Order("frequency DESC").Find(&terms).Error
	if err != nil {
		return err
	}
	return c.JSON(dto.NewWordCloud(terms))
}

// ExtraQuestions serves HR-only results for whichever optional extra questions (see
// dto.ExtraQuestionCatalog) were enabled for the resolved period — company-wide average per
// question, only for questions that were actually turned on that round.
func (h *DashboardHandler) ExtraQuestions(c *fiber.Ctx) error {
	orgID := middleware.OrgID(c)
	stats := h.stats.WithOrg(orgID)
	period, err := h.resolvePeriod(c, stats)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "no survey period found")
	}

	results, err := stats.ExtraQuestionResults(period.ID)
	if err != nil {
		return err
	}
	byKey := make(map[string]analytics.ExtraQuestionStat, len(results))
	for _, r := range results {
		byKey[r.Key] = r
	}

	enabled := make(map[string]bool, len(period.EnabledExtraQuestions))
	for _, k := range period.EnabledExtraQuestions {
		enabled[k] = true
	}

	out := make([]dto.ExtraQuestionResult, 0, len(dto.ExtraQuestionCatalog))
	for _, def := range dto.ExtraQuestionCatalog {
		if !enabled[def.Key] {
			continue
		}
		stat := byKey[def.Key]
		out = append(out, dto.ExtraQuestionResult{
			Key:             def.Key,
			Type:            def.Type,
			Label:           def.Label,
			Average:         stat.Avg,
			RespondentCount: stat.Count,
		})
	}
	return c.JSON(out)
}

// Insight serves the AI summary panel and the urgent-issue list.
func (h *DashboardHandler) Insight(c *fiber.Ctx) error {
	orgID := middleware.OrgID(c)
	stats := h.stats.WithOrg(orgID)
	period, err := h.resolvePeriod(c, stats)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "no survey period found")
	}

	var insight models.AIInsight
	if err := h.db.Where("org_id = ? AND period_id = ?", orgID, period.ID).First(&insight).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "no insight generated for this period yet")
	}

	var urgent []models.UrgentIssue
	if err := h.db.Where("org_id = ? AND period_id = ?", orgID, period.ID).Order("rank").Find(&urgent).Error; err != nil {
		return err
	}

	return c.JSON(dto.NewInsight(&insight, urgent))
}

// Alerts serves the HR-only alerts panel, generating them on demand if they haven't been
// computed for this period yet (no scheduler — same "on the fly" philosophy as the rest of
// the dashboard).
func (h *DashboardHandler) Alerts(c *fiber.Ctx) error {
	orgID := middleware.OrgID(c)
	stats := h.stats.WithOrg(orgID)
	period, err := h.resolvePeriod(c, stats)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "no survey period found")
	}

	var count int64
	if err := h.db.Model(&models.Alert{}).Where("org_id = ? AND period_id = ?", orgID, period.ID).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		if err := stats.GenerateAlerts(period); err != nil {
			return err
		}
	}

	var alerts []models.Alert
	err = h.db.Where("org_id = ? AND period_id = ?", orgID, period.ID).Order("severity DESC, created_at DESC").Find(&alerts).Error
	if err != nil {
		return err
	}
	return c.JSON(dto.NewAlerts(alerts))
}

// TopicDrilldown is HR-only: it is the one endpoint that returns sample text, and even
// there the text was redacted before it was stored.
func (h *DashboardHandler) TopicDrilldown(c *fiber.Ctx) error {
	orgID := middleware.OrgID(c)
	statsSvc := h.stats.WithOrg(orgID)
	period, err := h.resolvePeriod(c, statsSvc)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "no survey period found")
	}
	topicID := c.Params("id")

	var topic models.Topic
	if err := h.db.First(&topic, "id = ?", topicID).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "unknown topic")
	}

	stats, err := statsSvc.TopicStats(period.ID, topicID)
	if err != nil {
		return err
	}
	trend, err := statsSvc.TopicTrend(period, topicID, 6)
	if err != nil {
		return err
	}
	sentiment, err := statsSvc.SentimentSplit(period.ID)
	if err != nil {
		return err
	}

	var subIssues []models.TopicSubIssue
	if err := h.db.Where("topic_id = ?", topicID).Order("percentage DESC").Find(&subIssues).Error; err != nil {
		return err
	}

	var quotes []models.TopicSampleQuote
	if err := h.db.Where("topic_id = ?", topicID).Limit(3).Find(&quotes).Error; err != nil {
		return err
	}
	quoteTexts := make([]string, 0, len(quotes))
	for _, q := range quotes {
		quoteTexts = append(quoteTexts, q.Text)
	}

	return c.JSON(dto.TopicDrilldown{
		TopicID:          topicID,
		Label:            topic.Label,
		Score:            stats.Score,
		CompanyAverage:   stats.CompanyAverage,
		RespondentCount:  stats.RespondentCount,
		PercentageTagged: stats.PercentageTagged,
		Trend:            dto.NewTopicTrend(trend),
		SubIssues:        dto.NewSubIssues(subIssues),
		Sentiment:        sentiment,
		SampleQuotes:     quoteTexts,
	})
}

// ExecutiveSummary is aggregate-only by construction: dto.ExecutiveSummary has no field
// that could carry an individual's words, so the restriction holds at the type level.
func (h *DashboardHandler) ExecutiveSummary(c *fiber.Ctx) error {
	orgID := middleware.OrgID(c)
	stats := h.stats.WithOrg(orgID)
	period, err := h.resolvePeriod(c, stats)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "no survey period found")
	}

	var topics []models.Topic
	if err := h.db.Order("sort_order, id").Find(&topics).Error; err != nil {
		return err
	}
	var departments []models.Department
	if err := h.db.Where("org_id = ?", orgID).Order("id").Find(&departments).Error; err != nil {
		return err
	}
	var positions []models.Position
	if err := h.db.Where("org_id = ?", orgID).Order("id").Find(&positions).Error; err != nil {
		return err
	}

	score, err := stats.OverallHealthScore(period.ID)
	if err != nil {
		return err
	}
	sentiment, err := stats.SentimentSplit(period.ID)
	if err != nil {
		return err
	}
	radar, err := stats.Radar(period, topics)
	if err != nil {
		return err
	}
	deptScores, err := stats.DepartmentAverages(period.ID, departments)
	if err != nil {
		return err
	}
	positionScores, err := stats.PositionAverages(period.ID, positions)
	if err != nil {
		return err
	}

	var decisions []models.DecisionItem
	if err := h.db.Where("org_id = ? AND period_id = ?", orgID, period.ID).Order("rank").Find(&decisions).Error; err != nil {
		return err
	}

	return c.JSON(dto.ExecutiveSummary{
		Score:                score,
		Sentiment:            sentiment,
		Radar:                dto.NewRadar(radar),
		DepartmentComparison: dto.NewDepartmentScores(deptScores),
		PositionComparison:   dto.NewPositionScores(positionScores, positions),
		DecisionItems:        dto.NewDecisionItems(decisions),
	})
}

// Topics, Departments, Positions back the shared reference lists the frontend renders.
func (h *DashboardHandler) Topics(c *fiber.Ctx) error {
	var topics []models.Topic
	if err := h.db.Order("sort_order, id").Find(&topics).Error; err != nil {
		return err
	}
	return c.JSON(dto.NewTopics(topics))
}

func (h *DashboardHandler) Departments(c *fiber.Ctx) error {
	orgID := middleware.OrgID(c)
	stats := h.stats.WithOrg(orgID)
	period, err := h.resolvePeriod(c, stats)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "no survey period found")
	}
	var departments []models.Department
	if err := h.db.Where("org_id = ?", orgID).Order("id").Find(&departments).Error; err != nil {
		return err
	}
	counts, err := stats.RespondentCounts(period.ID)
	if err != nil {
		return err
	}
	return c.JSON(dto.NewDepartments(departments, counts))
}

func (h *DashboardHandler) Positions(c *fiber.Ctx) error {
	var positions []models.Position
	if err := h.db.Where("org_id = ?", middleware.OrgID(c)).Order("id").Find(&positions).Error; err != nil {
		return err
	}
	return c.JSON(dto.NewPositions(positions))
}
