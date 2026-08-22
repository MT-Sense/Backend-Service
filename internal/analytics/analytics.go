// Package analytics turns raw anonymous responses into the aggregates the dashboards show.
//
// Every query that groups by department, team or tenure carries `HAVING COUNT(*) >= 5`.
// That is the primary n<5 gate and it runs in the database, so a small group's numbers are
// never even loaded into memory. privacy.Suppress is applied on top as a second gate for
// values assembled in Go.
package analytics

import (
	"time"

	"gorm.io/gorm"

	"github.com/mt-sense/backend-service/internal/models"
	"github.com/mt-sense/backend-service/internal/privacy"
)

type Service struct{ db *gorm.DB }

func New(db *gorm.DB) *Service { return &Service{db: db} }

// CurrentPeriod is the YYYY-MM the dashboards default to.
func CurrentPeriod() string { return time.Now().Format("2006-01") }

func previousPeriod(period string) string {
	t, err := time.Parse("2006-01", period)
	if err != nil {
		return period
	}
	return t.AddDate(0, -1, 0).Format("2006-01")
}

// ---------------------------------------------------------------------------
// Shared building blocks
// ---------------------------------------------------------------------------

// RespondentCounts returns respondents per department for a period. Departments below the
// threshold are still counted here (the caller needs the count to decide suppression) but
// their *scores* are never selected — see DepartmentTopicScores.
func (s *Service) RespondentCounts(period string) (map[string]int64, error) {
	var rows []struct {
		DepartmentID string
		N            int64
	}
	err := s.db.Model(&models.SurveyResponse{}).
		Select("department_id, COUNT(*) AS n").
		Where("period_month = ?", period).
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
func (s *Service) TotalRespondents(period string) (int64, error) {
	var n int64
	err := s.db.Model(&models.SurveyResponse{}).Where("period_month = ?", period).Count(&n).Error
	return n, err
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

// ENPS = %promoters (9–10) − %detractors (0–6), the standard formula.
func (s *Service) ENPS(period string) (ENPSResult, error) {
	var row struct {
		Promoters  int64
		Detractors int64
		Total      int64
	}
	err := s.db.Model(&models.ResponseAnswer{}).
		Joins("JOIN survey_responses ON survey_responses.id = response_answers.response_id").
		Joins("JOIN survey_questions ON survey_questions.id = response_answers.question_id").
		Where("survey_questions.type = ? AND survey_responses.period_month = ?", models.QuestionENPS, period).
		Select(`
			COUNT(*) FILTER (WHERE response_answers.numeric_value >= 9) AS promoters,
			COUNT(*) FILTER (WHERE response_answers.numeric_value <= 6) AS detractors,
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

// SatisfactionAverage is the mean of every scale5 answer in the period.
func (s *Service) SatisfactionAverage(period string) (float64, error) {
	var avg *float64
	err := s.db.Model(&models.ResponseAnswer{}).
		Joins("JOIN survey_responses ON survey_responses.id = response_answers.response_id").
		Joins("JOIN survey_questions ON survey_questions.id = response_answers.question_id").
		Where("survey_questions.type = ? AND survey_responses.period_month = ?", models.QuestionScale5, period).
		Select("AVG(response_answers.numeric_value)").
		Scan(&avg).Error
	if err != nil || avg == nil {
		return 0, err
	}
	return round(*avg, 2), nil
}

// SentimentSplit is the positive/neutral/negative percentage over classified open text.
func (s *Service) SentimentSplit(period string) (models.SentimentSplit, error) {
	var row struct {
		Positive int64
		Neutral  int64
		Negative int64
		Total    int64
	}
	err := s.db.Model(&models.ResponseAnswer{}).
		Joins("JOIN survey_responses ON survey_responses.id = response_answers.response_id").
		Where("survey_responses.period_month = ? AND response_answers.sentiment <> ''", period).
		Select(`
			COUNT(*) FILTER (WHERE response_answers.sentiment = 'positive') AS positive,
			COUNT(*) FILTER (WHERE response_answers.sentiment = 'neutral')  AS neutral,
			COUNT(*) FILTER (WHERE response_answers.sentiment = 'negative') AS negative,
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

// BurnoutRisk is the share of respondents whose workload-topic scores sit at 2 or below,
// plus how many departments are above the alert line.
// ponytail: threshold heuristic, not a validated model — the formula was still open in the
// spec. Swap this one function when the real definition lands.
func (s *Service) BurnoutRisk(period string) (percentage int, departmentsAtRisk int, err error) {
	var row struct {
		AtRisk int64
		Total  int64
	}
	err = s.db.Model(&models.SurveyResponse{}).
		Joins("JOIN response_answers ON response_answers.response_id = survey_responses.id").
		Where("survey_responses.period_month = ? AND response_answers.topic_id = ? AND response_answers.numeric_value IS NOT NULL", period, "work").
		Select(`
			COUNT(DISTINCT survey_responses.id) FILTER (WHERE response_answers.numeric_value <= 2) AS at_risk,
			COUNT(DISTINCT survey_responses.id) AS total`).
		Scan(&row).Error
	if err != nil || row.Total == 0 {
		return 0, 0, err
	}

	var deptRows []struct{ DepartmentID string }
	err = s.db.Model(&models.SurveyResponse{}).
		Joins("JOIN response_answers ON response_answers.response_id = survey_responses.id").
		Where("survey_responses.period_month = ? AND response_answers.topic_id = ? AND response_answers.numeric_value IS NOT NULL", period, "work").
		Group("survey_responses.department_id").
		// n<5: a department too small to report is also too small to name as at-risk.
		Having("COUNT(DISTINCT survey_responses.id) >= ? AND AVG(response_answers.numeric_value) < ?", privacy.MinGroupSizeSQL, 3.0).
		Select("survey_responses.department_id AS department_id").
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

// Trend returns the last `months` periods ending at `period`, oldest first.
func (s *Service) Trend(period string, months int) ([]TrendPoint, error) {
	end, err := time.Parse("2006-01", period)
	if err != nil {
		return nil, err
	}
	points := make([]TrendPoint, 0, months)
	for i := months - 1; i >= 0; i-- {
		p := end.AddDate(0, -i, 0).Format("2006-01")
		enps, err := s.ENPS(p)
		if err != nil {
			return nil, err
		}
		sat, err := s.SatisfactionAverage(p)
		if err != nil {
			return nil, err
		}
		points = append(points, TrendPoint{Month: p, ENPS: enps.Value, Satisfaction: sat})
	}
	return points, nil
}

// ---------------------------------------------------------------------------
// Heatmap — department × topic
// ---------------------------------------------------------------------------

type DeptTopicScore struct {
	DepartmentID string
	TopicID      string
	Score        float64
	Respondents  int64
}

// DepartmentTopicScores returns only cells whose department cleared the n<5 threshold.
// The HAVING clause is the load-bearing part: scores for a small department are never
// selected, so there is no in-memory copy to accidentally serialize later.
func (s *Service) DepartmentTopicScores(period string) ([]DeptTopicScore, error) {
	var rows []DeptTopicScore
	err := s.db.Model(&models.SurveyResponse{}).
		Joins("JOIN response_answers ON response_answers.response_id = survey_responses.id").
		Where("survey_responses.period_month = ? AND response_answers.numeric_value IS NOT NULL AND response_answers.topic_id <> ''", period).
		Group("survey_responses.department_id, response_answers.topic_id").
		Having("COUNT(DISTINCT survey_responses.id) >= ?", privacy.MinGroupSizeSQL).
		Select(`
			survey_responses.department_id AS department_id,
			response_answers.topic_id AS topic_id,
			ROUND(AVG(response_answers.numeric_value)::numeric, 1)::float8 AS score,
			COUNT(DISTINCT survey_responses.id) AS respondents`).
		Scan(&rows).Error
	return rows, err
}

// TopicAverages returns the company-wide average per topic for a period.
func (s *Service) TopicAverages(period string) (map[string]float64, error) {
	var rows []struct {
		TopicID string
		Score   float64
	}
	err := s.db.Model(&models.ResponseAnswer{}).
		Joins("JOIN survey_responses ON survey_responses.id = response_answers.response_id").
		Where("survey_responses.period_month = ? AND response_answers.numeric_value IS NOT NULL AND response_answers.topic_id <> ''", period).
		Group("response_answers.topic_id").
		Select("response_answers.topic_id AS topic_id, ROUND(AVG(response_answers.numeric_value)::numeric, 2)::float8 AS score").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make(map[string]float64, len(rows))
	for _, r := range rows {
		out[r.TopicID] = r.Score
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

func (s *Service) Radar(period string, topics []models.Topic) ([]RadarAxis, error) {
	current, err := s.TopicAverages(period)
	if err != nil {
		return nil, err
	}
	previous, err := s.TopicAverages(previousPeriod(period))
	if err != nil {
		return nil, err
	}
	axes := make([]RadarAxis, 0, len(topics))
	for _, t := range topics {
		axes = append(axes, RadarAxis{TopicID: t.ID, ThisMonth: current[t.ID], LastMonth: previous[t.ID]})
	}
	return axes, nil
}

// DepartmentAverages returns each department's overall score, suppressed below n<5.
// Executives receive these as visual bars only; the suppressed shape keeps a small
// department out of the payload entirely rather than sending a number the UI must hide.
func (s *Service) DepartmentAverages(period string, departments []models.Department) ([]DepartmentScore, error) {
	var rows []struct {
		DepartmentID string
		Score        float64
	}
	err := s.db.Model(&models.SurveyResponse{}).
		Joins("JOIN response_answers ON response_answers.response_id = survey_responses.id").
		Where("survey_responses.period_month = ? AND response_answers.numeric_value IS NOT NULL AND response_answers.topic_id <> ''", period).
		Group("survey_responses.department_id").
		Having("COUNT(DISTINCT survey_responses.id) >= ?", privacy.MinGroupSizeSQL).
		Select(`
			survey_responses.department_id AS department_id,
			ROUND(AVG(response_answers.numeric_value)::numeric, 1)::float8 AS score`).
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	byDept := make(map[string]float64, len(rows))
	for _, r := range rows {
		byDept[r.DepartmentID] = r.Score
	}

	counts, err := s.RespondentCounts(period)
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

type DepartmentScore struct {
	DepartmentID string                      `json:"departmentId"`
	Score        models.Suppressible[float64] `json:"score"`
}

type TenureScore struct {
	Bucket string  `json:"bucket"`
	Score  float64 `json:"score"`
}

// TenureAverages groups by tenure band, again gated by the n<5 threshold in SQL.
func (s *Service) TenureAverages(period string) ([]TenureScore, error) {
	var rows []TenureScore
	err := s.db.Model(&models.SurveyResponse{}).
		Joins("JOIN response_answers ON response_answers.response_id = survey_responses.id").
		Where("survey_responses.period_month = ? AND response_answers.numeric_value IS NOT NULL AND response_answers.topic_id <> ''", period).
		Group("survey_responses.tenure_bucket").
		Having("COUNT(DISTINCT survey_responses.id) >= ?", privacy.MinGroupSizeSQL).
		Select(`
			survey_responses.tenure_bucket AS bucket,
			ROUND(AVG(response_answers.numeric_value)::numeric, 1)::float8 AS score`).
		Order("bucket").
		Scan(&rows).Error
	return rows, err
}

// OverallHealthScore condenses satisfaction and sentiment into the 0–100 figure the
// executive card shows.
// ponytail: weighted blend (70% satisfaction, 30% net sentiment); replace when the real
// scoring model is agreed.
func (s *Service) OverallHealthScore(period string) (int, error) {
	sat, err := s.SatisfactionAverage(period)
	if err != nil {
		return 0, err
	}
	sentiment, err := s.SentimentSplit(period)
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
	Score           float64
	CompanyAverage  float64
	RespondentCount int64
	PercentageTagged int
}

func (s *Service) TopicStats(period, topicID string) (TopicStats, error) {
	var row struct {
		Score       *float64
		Respondents int64
	}
	err := s.db.Model(&models.ResponseAnswer{}).
		Joins("JOIN survey_responses ON survey_responses.id = response_answers.response_id").
		Where("survey_responses.period_month = ? AND response_answers.topic_id = ? AND response_answers.numeric_value IS NOT NULL", period, topicID).
		Select("AVG(response_answers.numeric_value) AS score, COUNT(DISTINCT survey_responses.id) AS respondents").
		Scan(&row).Error
	if err != nil {
		return TopicStats{}, err
	}

	var companyAvg *float64
	err = s.db.Model(&models.ResponseAnswer{}).
		Joins("JOIN survey_responses ON survey_responses.id = response_answers.response_id").
		Where("survey_responses.period_month = ? AND response_answers.numeric_value IS NOT NULL AND response_answers.topic_id <> ''", period).
		Select("AVG(response_answers.numeric_value)").
		Scan(&companyAvg).Error
	if err != nil {
		return TopicStats{}, err
	}

	total, err := s.TotalRespondents(period)
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

func (s *Service) TopicTrend(period, topicID string, months int) ([]TopicTrendPoint, error) {
	end, err := time.Parse("2006-01", period)
	if err != nil {
		return nil, err
	}
	points := make([]TopicTrendPoint, 0, months)
	for i := months - 1; i >= 0; i-- {
		p := end.AddDate(0, -i, 0).Format("2006-01")
		var avg *float64
		err := s.db.Model(&models.ResponseAnswer{}).
			Joins("JOIN survey_responses ON survey_responses.id = response_answers.response_id").
			Where("survey_responses.period_month = ? AND response_answers.topic_id = ? AND response_answers.numeric_value IS NOT NULL", p, topicID).
			Select("AVG(response_answers.numeric_value)").
			Scan(&avg).Error
		if err != nil {
			return nil, err
		}
		point := TopicTrendPoint{Month: p}
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
