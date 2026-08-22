package handlers

import (
	"time"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"github.com/mt-sense/backend-service/internal/analytics"
	"github.com/mt-sense/backend-service/internal/dto"
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

// period reads ?month=YYYY-MM, defaulting to the current month.
func period(c *fiber.Ctx) string {
	if m := c.Query("month"); len(m) == 7 {
		return m
	}
	return analytics.CurrentPeriod()
}

func previousMonth(p string) string {
	t, err := time.Parse("2006-01", p)
	if err != nil {
		return p
	}
	return t.AddDate(0, -1, 0).Format("2006-01")
}

// HRKpis serves the five KPI cards plus the six-month trend.
func (h *DashboardHandler) HRKpis(c *fiber.Ctx) error {
	p := period(c)

	enps, err := h.stats.ENPS(p)
	if err != nil {
		return err
	}
	prevENPS, err := h.stats.ENPS(previousMonth(p))
	if err != nil {
		return err
	}
	satisfaction, err := h.stats.SatisfactionAverage(p)
	if err != nil {
		return err
	}
	prevSatisfaction, err := h.stats.SatisfactionAverage(previousMonth(p))
	if err != nil {
		return err
	}
	burnoutPct, deptsAtRisk, err := h.stats.BurnoutRisk(p)
	if err != nil {
		return err
	}
	sentiment, err := h.stats.SentimentSplit(p)
	if err != nil {
		return err
	}
	trend, err := h.stats.Trend(p, 6)
	if err != nil {
		return err
	}

	responded, err := h.stats.TotalRespondents(p)
	if err != nil {
		return err
	}
	var headcount int64
	if err := h.db.Model(&models.User{}).Count(&headcount).Error; err != nil {
		return err
	}
	responseRate := 0
	if headcount > 0 {
		responseRate = int(float64(responded) / float64(headcount) * 100)
	}

	return c.JSON(dto.HrKpis{
		Enps: dto.EnpsKpi{
			Value:            enps.Value,
			DeltaVsLastMonth: enps.Value - prevENPS.Value,
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
	p := period(c)

	var departments []models.Department
	if err := h.db.Order("id").Find(&departments).Error; err != nil {
		return err
	}
	var topics []models.Topic
	if err := h.db.Order("sort_order, id").Find(&topics).Error; err != nil {
		return err
	}

	scores, err := h.stats.DepartmentTopicScores(p)
	if err != nil {
		return err
	}
	counts, err := h.stats.RespondentCounts(p)
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
	var terms []models.WordCloudTerm
	err := h.db.Where("period_month = ?", period(c)).Order("frequency DESC").Find(&terms).Error
	if err != nil {
		return err
	}
	return c.JSON(dto.NewWordCloud(terms))
}

// Insight serves the AI summary panel and the urgent-issue list.
func (h *DashboardHandler) Insight(c *fiber.Ctx) error {
	p := period(c)

	var insight models.AIInsight
	if err := h.db.Where("period_month = ?", p).First(&insight).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "no insight generated for this period yet")
	}

	var urgent []models.UrgentIssue
	if err := h.db.Where("period_month = ?", p).Order("rank").Find(&urgent).Error; err != nil {
		return err
	}

	return c.JSON(dto.NewInsight(&insight, urgent))
}

// TopicDrilldown is HR-only: it is the one endpoint that returns sample text, and even
// there the text was redacted before it was stored.
func (h *DashboardHandler) TopicDrilldown(c *fiber.Ctx) error {
	p := period(c)
	topicID := c.Params("id")

	var topic models.Topic
	if err := h.db.First(&topic, "id = ?", topicID).Error; err != nil {
		return fiber.NewError(fiber.StatusNotFound, "unknown topic")
	}

	stats, err := h.stats.TopicStats(p, topicID)
	if err != nil {
		return err
	}
	trend, err := h.stats.TopicTrend(p, topicID, 6)
	if err != nil {
		return err
	}
	sentiment, err := h.stats.SentimentSplit(p)
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
	p := period(c)

	var topics []models.Topic
	if err := h.db.Order("sort_order, id").Find(&topics).Error; err != nil {
		return err
	}
	var departments []models.Department
	if err := h.db.Order("id").Find(&departments).Error; err != nil {
		return err
	}

	score, err := h.stats.OverallHealthScore(p)
	if err != nil {
		return err
	}
	sentiment, err := h.stats.SentimentSplit(p)
	if err != nil {
		return err
	}
	radar, err := h.stats.Radar(p, topics)
	if err != nil {
		return err
	}
	deptScores, err := h.stats.DepartmentAverages(p, departments)
	if err != nil {
		return err
	}
	tenure, err := h.stats.TenureAverages(p)
	if err != nil {
		return err
	}

	var decisions []models.DecisionItem
	if err := h.db.Where("period_month = ?", p).Order("rank").Find(&decisions).Error; err != nil {
		return err
	}

	return c.JSON(dto.ExecutiveSummary{
		Score:                score,
		Sentiment:            sentiment,
		Radar:                dto.NewRadar(radar),
		DepartmentComparison: dto.NewDepartmentScores(deptScores),
		TenureComparison:     dto.NewTenureScores(tenure),
		DecisionItems:        dto.NewDecisionItems(decisions),
	})
}

// Topics and Departments back the shared reference lists the frontend renders.
func (h *DashboardHandler) Topics(c *fiber.Ctx) error {
	var topics []models.Topic
	if err := h.db.Order("sort_order, id").Find(&topics).Error; err != nil {
		return err
	}
	return c.JSON(dto.NewTopics(topics))
}

func (h *DashboardHandler) Departments(c *fiber.Ctx) error {
	var departments []models.Department
	if err := h.db.Order("id").Find(&departments).Error; err != nil {
		return err
	}
	counts, err := h.stats.RespondentCounts(period(c))
	if err != nil {
		return err
	}
	return c.JSON(dto.NewDepartments(departments, counts))
}
