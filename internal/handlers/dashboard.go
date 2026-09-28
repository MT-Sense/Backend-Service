package handlers

import (
	"errors"
	"math"
	"sort"
	"strings"

	"github.com/gofiber/fiber/v2"
	"gorm.io/gorm"

	"github.com/mt-sense/backend-service/internal/aiservice"
	"github.com/mt-sense/backend-service/internal/analytics"
	"github.com/mt-sense/backend-service/internal/dto"
	"github.com/mt-sense/backend-service/internal/middleware"
	"github.com/mt-sense/backend-service/internal/models"
	"github.com/mt-sense/backend-service/internal/privacy"
)

type DashboardHandler struct {
	db    *gorm.DB
	stats *analytics.Service
	ai    *aiservice.Client
}

func NewDashboardHandler(db *gorm.DB, stats *analytics.Service, ai *aiservice.Client) *DashboardHandler {
	return &DashboardHandler{db: db, stats: stats, ai: ai}
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

// DepartmentTrend returns only privacy-safe monthly aggregates for a department.
func (h *DashboardHandler) DepartmentTrend(c *fiber.Ctx) error {
	orgID := middleware.OrgID(c)
	departmentID := strings.TrimSpace(c.Query("department"))
	if departmentID == "" {
		return fiber.NewError(fiber.StatusBadRequest, "department is required")
	}
	if departmentID != analytics.UnassignedDepartmentID {
		var count int64
		if err := h.db.Model(&models.Department{}).
			Where("org_id = ? AND id = ?", orgID, departmentID).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return fiber.NewError(fiber.StatusNotFound, "department not found")
		}
	}
	stats := h.stats.WithOrg(orgID)
	period, err := h.resolvePeriod(c, stats)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "no survey period found")
	}
	points, err := stats.DepartmentTrend(period, 6, departmentID)
	if err != nil {
		return err
	}
	return c.JSON(points)
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
	emerging, err := stats.VisibleEmergingTopics(period.ID)
	if err != nil {
		return err
	}
	emergingSortStart := len(topics)
	for index, topic := range emerging {
		topics = append(topics, models.Topic{
			ID:        topic.ID,
			Label:     topic.Label,
			SortOrder: emergingSortStart + index,
		})
	}

	scores, err := stats.DepartmentTopicScores(period.ID)
	if err != nil {
		return err
	}
	counts, err := stats.RespondentCounts(period.ID)
	if err != nil {
		return err
	}
	unassignedCount, err := stats.UnassignedRespondentCount(period.ID)
	if err != nil {
		return err
	}
	if unassignedCount >= models.MinGroupSize {
		departments = append(departments, models.Department{
			ID: analytics.UnassignedDepartmentID, Name: "Unassigned",
		})
		counts[analytics.UnassignedDepartmentID] = unassignedCount
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

// DepartmentSummary shows participation, current average, last-round change, and
// a simple next-round projection for each department. Small groups have no score,
// change, forecast, or status in the response.
func (h *DashboardHandler) DepartmentSummary(c *fiber.Ctx) error {
	orgID := middleware.OrgID(c)
	stats := h.stats.WithOrg(orgID)
	period, err := h.resolvePeriod(c, stats)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "no survey period found")
	}
	var departments []models.Department
	if err := h.db.Where("org_id = ?", orgID).Order("name, id").Find(&departments).Error; err != nil {
		return err
	}
	counts, err := stats.RespondentCounts(period.ID)
	if err != nil {
		return err
	}
	currentScores, err := stats.DepartmentPeriodScores(period.ID)
	if err != nil {
		return err
	}
	previousScores := map[string]float64{}
	previous, err := stats.PreviousPeriod(period)
	if err != nil {
		return err
	}
	if previous != nil && int(previous.Year)*12+int(previous.Month) == int(period.Year)*12+int(period.Month)-1 {
		previousScores, err = stats.DepartmentPeriodScores(previous.ID)
		if err != nil {
			return err
		}
	}
	var headcounts []struct {
		DepartmentID string
		Total        int64
	}
	if err := h.db.Model(&models.User{}).
		Select("department_id, COUNT(*) AS total").
		Where("org_id = ? AND is_active = ? AND department_id IS NOT NULL", orgID, true).
		Group("department_id").Scan(&headcounts).Error; err != nil {
		return err
	}
	totals := make(map[string]int64, len(headcounts))
	for _, item := range headcounts {
		totals[item.DepartmentID] = item.Total
	}
	rows := make([]dto.DepartmentSummaryRow, 0, len(departments)+1)
	appendRow := func(id, name string, responded int64, total *int64) {
		row := dto.DepartmentSummaryRow{
			DepartmentID: id, Name: name, Total: total,
			Responded: models.Hidden[int64](), Score: models.Hidden[float64](),
			Status: "unavailable",
		}
		if responded == 0 || responded >= models.MinGroupSize {
			row.Responded = models.Visible(responded)
		}
		if score, visible := currentScores[id]; visible {
			row.Score = models.Visible(score)
			row.Status = departmentScoreStatus(score)
			if previousScore, comparable := previousScores[id]; comparable {
				change, forecast := departmentProjection(score, previousScore)
				row.Change = &change
				row.Forecast = &forecast
			}
		}
		rows = append(rows, row)
	}
	for _, department := range departments {
		total := totals[department.ID]
		appendRow(department.ID, department.Name, counts[department.ID], &total)
	}
	unassigned, err := stats.UnassignedRespondentCount(period.ID)
	if err != nil {
		return err
	}
	if unassigned >= models.MinGroupSize {
		appendRow(analytics.UnassignedDepartmentID, "Unassigned", unassigned, nil)
	}
	return c.JSON(dto.DepartmentSummary{Rows: rows})
}

func roundOne(value float64) float64 { return math.Round(value*10) / 10 }

func departmentProjection(current, previous float64) (change, forecast float64) {
	change = roundOne(current - previous)
	forecast = roundOne(math.Max(1, math.Min(5, current+change)))
	return change, forecast
}

func departmentScoreStatus(score float64) string {
	switch {
	case score >= 4:
		return "good"
	case score >= 3:
		return "watch"
	default:
		return "risk"
	}
}

// WordCloud counts words found in this period's redacted survey comments. It uses the
// AI service only for Thai word segmentation and noise filtering; no LLM call is needed.
// A word must occur in at least five distinct responses before it leaves the server.
func (h *DashboardHandler) WordCloud(c *fiber.Ctx) error {
	orgID := middleware.OrgID(c)
	stats := h.stats.WithOrg(orgID)
	period, err := h.resolvePeriod(c, stats)
	if err != nil {
		return fiber.NewError(fiber.StatusNotFound, "no survey period found")
	}
	var responses []struct{ CommentText string }
	if err := h.db.Model(&models.SurveyResponse{}).
		Select("comment_text").
		Where("org_id = ? AND period_id = ? AND comment_text <> ''", orgID, period.ID).
		Find(&responses).Error; err != nil {
		return err
	}
	allKeywords := make([][]string, 0, len(responses))
	for start := 0; start < len(responses); start += 200 {
		end := min(start+200, len(responses))
		texts := make([]string, 0, end-start)
		for _, response := range responses[start:end] {
			texts = append(texts, response.CommentText)
		}
		keywords, err := h.ai.Keywords(c.UserContext(), texts)
		if err != nil {
			return fiber.NewError(fiber.StatusServiceUnavailable, "keyword analysis is temporarily unavailable")
		}
		allKeywords = append(allKeywords, keywords...)
	}
	return c.JSON(wordCloudTerms(allKeywords))
}

func wordCloudTerms(responses [][]string) []dto.WordCloudTerm {
	counts := make(map[string]int)
	for _, keywords := range responses {
		seen := make(map[string]bool, len(keywords))
		for _, keyword := range keywords {
			word := strings.TrimSpace(keyword)
			if word != "" && !seen[word] {
				counts[word]++
				seen[word] = true
			}
		}
	}
	terms := make([]dto.WordCloudTerm, 0, len(counts))
	for word, n := range counts {
		if n >= models.MinGroupSize {
			terms = append(terms, dto.WordCloudTerm{
				Term: models.Localized{TH: word, EN: word}, Frequency: n,
			})
		}
	}
	sort.Slice(terms, func(i, j int) bool {
		if terms[i].Frequency == terms[j].Frequency {
			return terms[i].Term.TH < terms[j].Term.TH
		}
		return terms[i].Frequency > terms[j].Frequency
	})
	if len(terms) > 30 {
		terms = terms[:30]
	}
	return terms
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

	insight, urgent, err := stats.GenerateInsight(period)
	if err != nil {
		return err
	}
	if insight == nil {
		return fiber.NewError(fiber.StatusNotFound, "at least 5 analyzed responses are required to generate insight")
	}

	// Prefer the persistent LLM-compiled period summary when available. The remaining
	// dashboard fields stay deterministic aggregates generated by analytics.GenerateInsight.
	var knowledge models.KnowledgeBaseSummary
	if err := h.db.Where("org_id = ? AND period_id = ?", orgID, period.ID).First(&knowledge).Error; err == nil {
		if knowledge.SummaryText != "" {
			insight.Summary.TH = knowledge.SummaryText
		}
		if knowledge.SummaryEN != "" {
			insight.Summary.EN = knowledge.SummaryEN
		}
	}

	return c.JSON(dto.NewInsight(insight, urgent))
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
	err = h.db.Where("org_id = ? AND period_id = ?", orgID, period.ID).
		Order("CASE severity WHEN 'critical' THEN 0 WHEN 'warning' THEN 1 ELSE 2 END, created_at DESC").
		Find(&alerts).Error
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

	var topicLabel models.Localized
	var topic models.Topic
	if err := h.db.First(&topic, "id = ?", topicID).Error; err == nil {
		topicLabel = topic.Label
	} else {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var emerging models.EmergingTopic
		if err := h.db.Where("org_id = ? AND id = ?", orgID, topicID).First(&emerging).Error; err != nil {
			return fiber.NewError(fiber.StatusNotFound, "unknown topic")
		}
		topicLabel = emerging.Label
	}
	if departmentID := c.Query("department"); departmentID != "" {
		departmentName := "Unassigned"
		if departmentID != analytics.UnassignedDepartmentID {
			var department models.Department
			if err := h.db.Where("org_id = ? AND id = ?", orgID, departmentID).First(&department).Error; err != nil {
				return fiber.NewError(fiber.StatusNotFound, "unknown department")
			}
			departmentName = department.Name
		}
		detail, err := statsSvc.DepartmentTopicDetail(period, topicID, departmentID)
		if err != nil {
			return err
		}
		if detail == nil {
			return fiber.NewError(fiber.StatusNotFound, "department topic data is hidden or unavailable")
		}
		return c.JSON(dto.TopicDrilldown{
			TopicID: topicID, Label: topicLabel,
			DepartmentID: departmentID, DepartmentName: departmentName,
			Score: detail.Score, CompanyAverage: detail.CompanyAverage,
			RespondentCount: detail.RespondentCount, PercentageTagged: detail.PercentageTagged,
			Trend: dto.NewTopicTrend(detail.Trend), SubIssues: []dto.TopicSubIssue{},
			Sentiment: detail.Sentiment, SampleQuotes: detail.SampleQuotes,
		})
	}

	stats, err := statsSvc.TopicStats(period.ID, topicID)
	if err != nil {
		return err
	}
	if stats.RespondentCount < models.MinGroupSize {
		return fiber.NewError(fiber.StatusNotFound, "topic data is hidden or unavailable")
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

	quoteTexts, err := statsSvc.TopicSampleQuotes(period.ID, topicID, "")
	if err != nil {
		return err
	}

	return c.JSON(dto.TopicDrilldown{
		TopicID:          topicID,
		Label:            topicLabel,
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
