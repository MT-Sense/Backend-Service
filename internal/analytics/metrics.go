package analytics

import (
	"github.com/mt-sense/backend-service/internal/models"
	"gorm.io/gorm"
)

// SentimentPercentages uses largest remainders: percentages stay within 0..100
// and sum to 100. Ties are resolved in positive/neutral/negative order.
func SentimentPercentages(positive, neutral, negative int64) models.SentimentSplit {
	counts := [3]int64{positive, neutral, negative}
	total := positive + neutral + negative
	if total <= 0 {
		return models.SentimentSplit{}
	}
	var values [3]int
	var remainders [3]int64
	sum := 0
	for i, n := range counts {
		values[i] = int(n * 100 / total)
		remainders[i] = n * 100 % total
		sum += values[i]
	}
	for ; sum < 100; sum++ {
		best := 0
		for i := 1; i < 3; i++ {
			if remainders[i] > remainders[best] {
				best = i
			}
		}
		values[best]++
		remainders[best] = -1
	}
	return models.SentimentSplit{Positive: values[0], Neutral: values[1], Negative: values[2]}
}

const audienceAll = "__all__"

// CapturePeriodAudience records aggregate account counts at round creation.
// It stores no identities and must run in the same transaction as period creation.
// Existing rounds are deliberately not backfilled from today's membership.
func CapturePeriodAudience(tx *gorm.DB, period *models.SurveyPeriod) error {
	var rows []struct {
		DepartmentID string
		Total        int64
	}
	if err := tx.Model(&models.User{}).
		Where("org_id = ? AND is_active = ?", period.OrgID, true).
		Select("COALESCE(department_id, '__unassigned__') AS department_id, COUNT(*) AS total").
		Group("COALESCE(department_id, '__unassigned__')").Scan(&rows).Error; err != nil {
		return err
	}
	counts := map[string]int64{audienceAll: 0}
	for _, row := range rows {
		counts[row.DepartmentID] = row.Total
		counts[audienceAll] += row.Total
	}
	period.ResponseAudience = counts
	return nil
}

// ResponseRate compares answers with the account-count snapshot, never live membership.
// Imports have no verified target population. A count exceeding the snapshot also
// makes the ratio unavailable (e.g. new accounts joined after creation), not capped.
func (s *Service) ResponseRate(periodID, departmentID string) (responded int64, total *int64, percentage *int, err error) {
	query := s.db.Model(&models.SurveyResponse{}).Where("org_id = ? AND period_id = ?", s.orgID, periodID)
	if departmentID == UnassignedDepartmentID {
		query = query.Where("department_id IS NULL")
	} else if departmentID != "" {
		query = query.Where("department_id = ?", departmentID)
	}
	if err = query.Count(&responded).Error; err != nil {
		return
	}
	var period *models.SurveyPeriod
	period, err = s.PeriodByID(periodID)
	if err != nil || period.ResponseAudience == nil {
		return
	}
	var imported int64
	err = s.db.Model(&models.SurveyImport{}).Where("org_id = ? AND period_id = ?", s.orgID, periodID).Count(&imported).Error
	if err != nil || imported > 0 {
		return
	}
	key := departmentID
	if key == "" {
		key = audienceAll
	}
	count := period.ResponseAudience[key]
	total = &count
	if count > 0 && responded <= count {
		rate := int(float64(responded) / float64(count) * 100)
		percentage = &rate
	}
	return
}

func (s *Service) AnalyzedRespondents(periodID string) (int64, error) {
	var n int64
	err := s.db.Model(&models.ResponseAnalysis{}).
		Joins("JOIN survey_responses ON survey_responses.id = response_analysis.response_id").
		Where("survey_responses.org_id = ? AND survey_responses.period_id = ?", s.orgID, periodID).
		Count(&n).Error
	return n, err
}
