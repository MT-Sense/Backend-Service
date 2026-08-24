// Package analytics turns raw anonymous responses into the aggregates the dashboards show.
//
// Every query that groups by department or position carries `HAVING COUNT(*) >= 5`. That is
// the primary n<5 gate and it runs in the database, so a small group's numbers are never even
// loaded into memory. privacy.Suppress is applied on top as a second gate for values
// assembled in Go.
//
// Under the fixed satisfaction_score(1-5)+comment_text survey model there is no per-topic
// question anymore, so per-topic figures (the HR heatmap columns, the Executive radar axes,
// topic drill-down) are derived from response_analysis.categories — the LLM-assigned topic
// tags on each response's comment — rather than from dedicated per-topic questions. A cell's
// "score" is the average satisfaction_score of respondents whose comment touched that theme,
// in that department/period; this is documented as an approximation, same convention as the
// other ponytail: heuristics in this codebase.
package analytics

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"github.com/mt-sense/backend-service/internal/models"
	"github.com/mt-sense/backend-service/internal/privacy"
)

type Service struct {
	db    *gorm.DB
	orgID string
}

func New(db *gorm.DB) *Service { return &Service{db: db} }

// WithOrg returns a copy of the service scoped to a specific org — the per-request
// replacement for the old boot-time constant. Cheap value-copy, safe to call once per
// request from a shared *Service without any locking.
func (s *Service) WithOrg(orgID string) *Service {
	clone := *s
	clone.orgID = orgID
	return &clone
}

func periodLabel(p models.SurveyPeriod) string {
	return fmt.Sprintf("%04d-%02d", p.Year, p.Month)
}

// LatestPeriod is the most recent survey period (open or already closed) for the org —
// dashboards default to it.
func (s *Service) LatestPeriod() (*models.SurveyPeriod, error) {
	var p models.SurveyPeriod
	err := s.db.Where("org_id = ?", s.orgID).Order("year DESC, month DESC").First(&p).Error
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// CurrentOpenPeriod returns the period currently accepting submissions
// (opens_at <= now < closes_at), or nil if none is open.
func (s *Service) CurrentOpenPeriod() (*models.SurveyPeriod, error) {
	var p models.SurveyPeriod
	now := time.Now()
	err := s.db.Where("org_id = ? AND opens_at <= ? AND closes_at > ?", s.orgID, now, now).
		Order("year DESC, month DESC").First(&p).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// PeriodByID loads a specific period, scoped to the org.
func (s *Service) PeriodByID(id string) (*models.SurveyPeriod, error) {
	var p models.SurveyPeriod
	err := s.db.Where("org_id = ? AND id = ?", s.orgID, id).First(&p).Error
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// PreviousPeriod returns the period chronologically before the given one, or nil if there
// isn't one yet.
func (s *Service) PreviousPeriod(p *models.SurveyPeriod) (*models.SurveyPeriod, error) {
	var prev models.SurveyPeriod
	err := s.db.Where("org_id = ? AND (year < ? OR (year = ? AND month < ?))", s.orgID, p.Year, p.Year, p.Month).
		Order("year DESC, month DESC").First(&prev).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &prev, nil
}

// RecentPeriods returns up to n periods ending at (and including) upTo, oldest first.
func (s *Service) RecentPeriods(upTo *models.SurveyPeriod, n int) ([]models.SurveyPeriod, error) {
	var periods []models.SurveyPeriod
	err := s.db.Where("org_id = ? AND (year < ? OR (year = ? AND month <= ?))", s.orgID, upTo.Year, upTo.Year, upTo.Month).
		Order("year DESC, month DESC").Limit(n).Find(&periods).Error
	if err != nil {
		return nil, err
	}
	for i, j := 0, len(periods)-1; i < j; i, j = i+1, j-1 {
		periods[i], periods[j] = periods[j], periods[i]
	}
	return periods, nil
}

// ---------------------------------------------------------------------------
// Shared building blocks
// ---------------------------------------------------------------------------

// RespondentCounts returns respondents per department for a period. Departments below the
// threshold are still counted here (the caller needs the count to decide suppression) but
// their *scores* are never selected — see DepartmentTopicScores.
func (s *Service) RespondentCounts(periodID string) (map[string]int64, error) {
	var rows []struct {
		DepartmentID string
		N            int64
	}
	err := s.db.Model(&models.SurveyResponse{}).
		Select("department_id, COUNT(*) AS n").
		Where("period_id = ? AND department_id IS NOT NULL", periodID).
		Group("department_id").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(rows))
	for _, r := range rows {
		out[r.DepartmentID] = r.N
	}
	return out, nil
}

// TotalRespondents counts submissions for the period across the whole company.
func (s *Service) TotalRespondents(periodID string) (int64, error) {
	var n int64
	err := s.db.Model(&models.SurveyResponse{}).Where("period_id = ?", periodID).Count(&n).Error
	return n, err
}

// ExtraQuestionStat is one fixed-catalog extra question's company-wide average for a period.
type ExtraQuestionStat struct {
	Key   string
	Avg   float64
	Count int64
}

// ExtraQuestionResults aggregates answers to the optional extra questions (see
// dto.ExtraQuestionCatalog) HR enabled for a period. extra_answers has no period_id column
// of its own — it joins through survey_responses, the same table that owns period scoping.
func (s *Service) ExtraQuestionResults(periodID string) ([]ExtraQuestionStat, error) {
	var rows []struct {
		Key   string
		Avg   float64
		Count int64
	}
	err := s.db.Table("extra_answers").
		Joins("JOIN survey_responses ON survey_responses.id = extra_answers.response_id").
		Where("survey_responses.period_id = ?", periodID).
		Group("extra_answers.question_key").
		Select("extra_answers.question_key AS key, AVG(extra_answers.value) AS avg, COUNT(*) AS count").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]ExtraQuestionStat, 0, len(rows))
	for _, r := range rows {
		out = append(out, ExtraQuestionStat{Key: r.Key, Avg: round(r.Avg, 2), Count: r.Count})
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// HR KPIs
// ---------------------------------------------------------------------------

type ENPSResult struct {
	Value      int
	Promoters  int64
	Detractors int64
	Total      int64
}

// ENPS is an approximation derived from the satisfaction_score distribution (there is no
// dedicated 0-10 recommend question in the fixed survey model): score 5 counts as a
// promoter, score <=3 as a detractor, using the standard %promoters-%detractors formula.
func (s *Service) ENPS(periodID string) (ENPSResult, error) {
	var row struct {
		Promoters  int64
		Detractors int64
		Total      int64
	}
	err := s.db.Model(&models.SurveyResponse{}).
		Where("period_id = ?", periodID).
		Select(`
			COUNT(*) FILTER (WHERE satisfaction_score = 5) AS promoters,
			COUNT(*) FILTER (WHERE satisfaction_score <= 3) AS detractors,
			COUNT(*) AS total`).
		Scan(&row).Error
	if err != nil || row.Total == 0 {
		return ENPSResult{}, err
	}
	value := float64(row.Promoters-row.Detractors) / float64(row.Total) * 100
	return ENPSResult{
		Value:      int(round(value, 0)),
		Promoters:  row.Promoters,
		Detractors: row.Detractors,
		Total:      row.Total,
	}, nil
}

// SatisfactionAverage is the mean satisfaction_score across all responses in the period.
func (s *Service) SatisfactionAverage(periodID string) (float64, error) {
	var avg *float64
	err := s.db.Model(&models.SurveyResponse{}).
		Where("period_id = ?", periodID).
		Select("AVG(satisfaction_score)").
		Scan(&avg).Error
	if err != nil || avg == nil {
		return 0, err
	}
	return round(*avg, 2), nil
}

// SentimentSplit is the positive/neutral/negative percentage over responses that have a
// comment (and therefore a response_analysis row). Enum values are the schema's
// pos/neu/neg — translated to the frontend's positive/neutral/negative wording in dto.
func (s *Service) SentimentSplit(periodID string) (models.SentimentSplit, error) {
	var row struct {
		Positive int64
		Neutral  int64
		Negative int64
		Total    int64
	}
	err := s.db.Model(&models.ResponseAnalysis{}).
		Joins("JOIN survey_responses ON survey_responses.id = response_analysis.response_id").
		Where("survey_responses.period_id = ?", periodID).
		Select(`
			COUNT(*) FILTER (WHERE response_analysis.sentiment_label = 'pos') AS positive,
			COUNT(*) FILTER (WHERE response_analysis.sentiment_label = 'neu') AS neutral,
			COUNT(*) FILTER (WHERE response_analysis.sentiment_label = 'neg') AS negative,
			COUNT(*) AS total`).
		Scan(&row).Error
	if err != nil || row.Total == 0 {
		return models.SentimentSplit{}, err
	}
	pct := func(n int64) int { return int(round(float64(n)/float64(row.Total)*100, 0)) }
	split := models.SentimentSplit{
		Positive: pct(row.Positive),
		Neutral:  pct(row.Neutral),
		Negative: pct(row.Negative),
	}
	// Absorb rounding drift into the largest bucket so the three always total 100.
	if drift := 100 - (split.Positive + split.Neutral + split.Negative); drift != 0 {
		split.Positive += drift
	}
	return split, nil
}

// BurnoutRisk is the share of respondents scoring 2 or below, plus how many departments are
// above the alert line.
// ponytail: threshold heuristic, not a validated model — the formula was still open in the
// spec. Swap this one function when the real definition lands.
func (s *Service) BurnoutRisk(periodID string) (percentage int, departmentsAtRisk int, err error) {
	var row struct {
		AtRisk int64
		Total  int64
	}
	err = s.db.Model(&models.SurveyResponse{}).
		Where("period_id = ?", periodID).
		Select(`
			COUNT(*) FILTER (WHERE satisfaction_score <= 2) AS at_risk,
			COUNT(*) AS total`).
		Scan(&row).Error
	if err != nil || row.Total == 0 {
		return 0, 0, err
	}

	var deptRows []struct{ DepartmentID string }
	err = s.db.Model(&models.SurveyResponse{}).
		Where("period_id = ? AND department_id IS NOT NULL", periodID).
		Group("department_id").
		// n<5: a department too small to report is also too small to name as at-risk.
		Having("COUNT(*) >= ? AND AVG(satisfaction_score) < ?", privacy.MinGroupSizeSQL, 3.0).
		Select("department_id").
		Scan(&deptRows).Error
	if err != nil {
		return 0, 0, err
	}

	return int(round(float64(row.AtRisk)/float64(row.Total)*100, 0)), len(deptRows), nil
}

type TrendPoint struct {
	Month        string  `json:"month"`
	ENPS         int     `json:"enps"`
	Satisfaction float64 `json:"satisfaction"`
}

// Trend returns up to `months` periods ending at `upTo`, oldest first.
func (s *Service) Trend(upTo *models.SurveyPeriod, months int) ([]TrendPoint, error) {
	periods, err := s.RecentPeriods(upTo, months)
	if err != nil {
		return nil, err
	}
	points := make([]TrendPoint, 0, len(periods))
	for _, p := range periods {
		enps, err := s.ENPS(p.ID)
		if err != nil {
			return nil, err
		}
		sat, err := s.SatisfactionAverage(p.ID)
		if err != nil {
			return nil, err
		}
		points = append(points, TrendPoint{Month: periodLabel(p), ENPS: enps.Value, Satisfaction: sat})
	}
	return points, nil
}

// ---------------------------------------------------------------------------
// Heatmap — department × topic (topic membership from response_analysis.categories)
// ---------------------------------------------------------------------------

type DeptTopicScore struct {
	DepartmentID string
	TopicID      string
	Score        float64
	Respondents  int64
}

// DepartmentTopicScores returns only cells whose department cleared the n<5 threshold. Runs
// one query per topic (small, fixed-size taxonomy) using jsonb containment on
// response_analysis.categories, the same cast-at-query-time pattern as FeedPost.Hashtags.
func (s *Service) DepartmentTopicScores(periodID string, topics []models.Topic) ([]DeptTopicScore, error) {
	out := make([]DeptTopicScore, 0, len(topics)*4)
	for _, topic := range topics {
		containsTopic, err := json.Marshal([]string{topic.ID})
		if err != nil {
			return nil, err
		}
		var rows []struct {
			DepartmentID string
			Score        float64
			Respondents  int64
		}
		err = s.db.Model(&models.SurveyResponse{}).
			Joins("JOIN response_analysis ON response_analysis.response_id = survey_responses.id").
			Where("survey_responses.period_id = ? AND survey_responses.department_id IS NOT NULL AND response_analysis.categories::jsonb @> ?::jsonb", periodID, string(containsTopic)).
			Group("survey_responses.department_id").
			Having("COUNT(DISTINCT survey_responses.id) >= ?", privacy.MinGroupSizeSQL).
			Select(`
				survey_responses.department_id AS department_id,
				ROUND(AVG(survey_responses.satisfaction_score)::numeric, 1)::float8 AS score,
				COUNT(DISTINCT survey_responses.id) AS respondents`).
			Scan(&rows).Error
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			out = append(out, DeptTopicScore{DepartmentID: r.DepartmentID, TopicID: topic.ID, Score: r.Score, Respondents: r.Respondents})
		}
	}
	return out, nil
}

// TopicAverages returns the company-wide average per topic for a period (no suppression —
// this is an aggregate over the whole company, not a small group).
func (s *Service) TopicAverages(periodID string, topics []models.Topic) (map[string]float64, error) {
	out := make(map[string]float64, len(topics))
	for _, topic := range topics {
		containsTopic, err := json.Marshal([]string{topic.ID})
		if err != nil {
			return nil, err
		}
		var avg *float64
		err = s.db.Model(&models.SurveyResponse{}).
			Joins("JOIN response_analysis ON response_analysis.response_id = survey_responses.id").
			Where("survey_responses.period_id = ? AND response_analysis.categories::jsonb @> ?::jsonb", periodID, string(containsTopic)).
			Select("ROUND(AVG(survey_responses.satisfaction_score)::numeric, 2)::float8").
			Scan(&avg).Error
		if err != nil {
			return nil, err
		}
		if avg != nil {
			out[topic.ID] = *avg
		}
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Executive aggregates
// ---------------------------------------------------------------------------

type RadarAxis struct {
	TopicID   string  `json:"topicId"`
	ThisMonth float64 `json:"thisMonth"`
	LastMonth float64 `json:"lastMonth"`
}

func (s *Service) Radar(period *models.SurveyPeriod, topics []models.Topic) ([]RadarAxis, error) {
	current, err := s.TopicAverages(period.ID, topics)
	if err != nil {
		return nil, err
	}
	prevPeriod, err := s.PreviousPeriod(period)
	if err != nil {
		return nil, err
	}
	previous := map[string]float64{}
	if prevPeriod != nil {
		previous, err = s.TopicAverages(prevPeriod.ID, topics)
		if err != nil {
			return nil, err
		}
	}
	axes := make([]RadarAxis, 0, len(topics))
	for _, t := range topics {
		axes = append(axes, RadarAxis{TopicID: t.ID, ThisMonth: current[t.ID], LastMonth: previous[t.ID]})
	}
	return axes, nil
}

type DepartmentScore struct {
	DepartmentID string                       `json:"departmentId"`
	Score        models.Suppressible[float64] `json:"score"`
}

// DepartmentAverages returns each department's overall satisfaction score, suppressed below
// n<5. Executives receive these as visual bars only; the suppressed shape keeps a small
// department out of the payload entirely rather than sending a number the UI must hide.
func (s *Service) DepartmentAverages(periodID string, departments []models.Department) ([]DepartmentScore, error) {
	var rows []struct {
		DepartmentID string
		Score        float64
	}
	err := s.db.Model(&models.SurveyResponse{}).
		Where("period_id = ? AND department_id IS NOT NULL", periodID).
		Group("department_id").
		Having("COUNT(*) >= ?", privacy.MinGroupSizeSQL).
		Select("department_id, ROUND(AVG(satisfaction_score)::numeric, 1)::float8 AS score").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	byDept := make(map[string]float64, len(rows))
	for _, r := range rows {
		byDept[r.DepartmentID] = r.Score
	}

	counts, err := s.RespondentCounts(periodID)
	if err != nil {
		return nil, err
	}

	out := make([]DepartmentScore, 0, len(departments))
	for _, d := range departments {
		score, reported := byDept[d.ID]
		if !reported {
			out = append(out, DepartmentScore{DepartmentID: d.ID, Score: models.Hidden[float64]()})
			continue
		}
		out = append(out, DepartmentScore{
			DepartmentID: d.ID,
			Score:        privacy.Suppress(counts[d.ID], score),
		})
	}
	return out, nil
}

type PositionScore struct {
	PositionID string
	Score      float64
}

// PositionAverages groups by position (this app's replacement for the old tenure-bucket
// breakdown, since the fixed schema tracks position instead of tenure), gated by n<5.
// Positions below the threshold are simply omitted from the result — the executive chart
// renders fewer bars rather than a suppressed placeholder, matching how this chart behaved
// for tenure before.
func (s *Service) PositionAverages(periodID string, positions []models.Position) ([]PositionScore, error) {
	var rows []struct {
		PositionID string
		Score      float64
	}
	err := s.db.Model(&models.SurveyResponse{}).
		Where("period_id = ? AND position_id IS NOT NULL", periodID).
		Group("position_id").
		Having("COUNT(*) >= ?", privacy.MinGroupSizeSQL).
		Select("position_id, ROUND(AVG(satisfaction_score)::numeric, 1)::float8 AS score").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	byPosition := make(map[string]float64, len(rows))
	for _, r := range rows {
		byPosition[r.PositionID] = r.Score
	}

	out := make([]PositionScore, 0, len(positions))
	for _, p := range positions {
		if score, ok := byPosition[p.ID]; ok {
			out = append(out, PositionScore{PositionID: p.ID, Score: score})
		}
	}
	return out, nil
}

// OverallHealthScore condenses satisfaction and sentiment into the 0-100 figure the
// executive card shows.
// ponytail: weighted blend (70% satisfaction, 30% net sentiment); replace when the real
// scoring model is agreed.
func (s *Service) OverallHealthScore(periodID string) (int, error) {
	sat, err := s.SatisfactionAverage(periodID)
	if err != nil {
		return 0, err
	}
	sentiment, err := s.SentimentSplit(periodID)
	if err != nil {
		return 0, err
	}
	satComponent := sat / 5 * 100
	sentimentComponent := float64(50 + (sentiment.Positive-sentiment.Negative)/2)
	return int(round(satComponent*0.7+sentimentComponent*0.3, 0)), nil
}

// ---------------------------------------------------------------------------
// Topic drill-down (HR only)
// ---------------------------------------------------------------------------

type TopicStats struct {
	Score            float64
	CompanyAverage   float64
	RespondentCount  int64
	PercentageTagged int
}

func (s *Service) TopicStats(periodID, topicID string) (TopicStats, error) {
	containsTopic, err := json.Marshal([]string{topicID})
	if err != nil {
		return TopicStats{}, err
	}

	var row struct {
		Score       *float64
		Respondents int64
	}
	err = s.db.Model(&models.SurveyResponse{}).
		Joins("JOIN response_analysis ON response_analysis.response_id = survey_responses.id").
		Where("survey_responses.period_id = ? AND response_analysis.categories::jsonb @> ?::jsonb", periodID, string(containsTopic)).
		Select("AVG(survey_responses.satisfaction_score) AS score, COUNT(DISTINCT survey_responses.id) AS respondents").
		Scan(&row).Error
	if err != nil {
		return TopicStats{}, err
	}

	var companyAvg *float64
	err = s.db.Model(&models.SurveyResponse{}).
		Where("period_id = ?", periodID).
		Select("AVG(satisfaction_score)").
		Scan(&companyAvg).Error
	if err != nil {
		return TopicStats{}, err
	}

	total, err := s.TotalRespondents(periodID)
	if err != nil {
		return TopicStats{}, err
	}

	stats := TopicStats{RespondentCount: row.Respondents}
	if row.Score != nil {
		stats.Score = round(*row.Score, 1)
	}
	if companyAvg != nil {
		stats.CompanyAverage = round(*companyAvg, 1)
	}
	if total > 0 {
		stats.PercentageTagged = int(round(float64(row.Respondents)/float64(total)*100, 0))
	}
	return stats, nil
}

type TopicTrendPoint struct {
	Month string  `json:"month"`
	Score float64 `json:"score"`
}

func (s *Service) TopicTrend(upTo *models.SurveyPeriod, topicID string, months int) ([]TopicTrendPoint, error) {
	periods, err := s.RecentPeriods(upTo, months)
	if err != nil {
		return nil, err
	}
	containsTopic, err := json.Marshal([]string{topicID})
	if err != nil {
		return nil, err
	}

	points := make([]TopicTrendPoint, 0, len(periods))
	for _, p := range periods {
		var avg *float64
		err := s.db.Model(&models.SurveyResponse{}).
			Joins("JOIN response_analysis ON response_analysis.response_id = survey_responses.id").
			Where("survey_responses.period_id = ? AND response_analysis.categories::jsonb @> ?::jsonb", p.ID, string(containsTopic)).
			Select("AVG(survey_responses.satisfaction_score)").
			Scan(&avg).Error
		if err != nil {
			return nil, err
		}
		point := TopicTrendPoint{Month: periodLabel(p)}
		if avg != nil {
			point.Score = round(*avg, 2)
		}
		points = append(points, point)
	}
	return points, nil
}

func round(v float64, places int) float64 {
	shift := 1.0
	for range places {
		shift *= 10
	}
	return float64(int64(v*shift+copysign(0.5, v))) / shift
}

func copysign(magnitude, sign float64) float64 {
	if sign < 0 {
		return -magnitude
	}
	return magnitude
}
