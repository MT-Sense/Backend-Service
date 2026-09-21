package analytics

import (
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/mt-sense/backend-service/internal/models"
	"github.com/mt-sense/backend-service/internal/privacy"
)

// GenerateInsight rebuilds the dashboard insight from responses that have already passed
// through AI analysis. It is safe to call whenever the dashboard loads: the current result
// replaces the previous result in one transaction, so an open survey round does not show a
// stale summary as more responses arrive.
func (s *Service) GenerateInsight(period *models.SurveyPeriod) (*models.AIInsight, []models.UrgentIssue, error) {
	var aggregate struct {
		Average  *float64
		Positive int64
		Neutral  int64
		Negative int64
		Total    int64
	}
	err := s.db.Table("survey_responses").
		Joins("JOIN response_analysis ON response_analysis.response_id = survey_responses.id").
		Where("survey_responses.org_id = ? AND survey_responses.period_id = ?", s.orgID, period.ID).
		Select(`
			AVG(survey_responses.satisfaction_score) AS average,
			COUNT(*) FILTER (WHERE response_analysis.sentiment_label = 'pos') AS positive,
			COUNT(*) FILTER (WHERE response_analysis.sentiment_label = 'neu') AS neutral,
			COUNT(*) FILTER (WHERE response_analysis.sentiment_label = 'neg') AS negative,
			COUNT(*) AS total`).
		Scan(&aggregate).Error
	if err != nil {
		return nil, nil, err
	}
	if aggregate.Total < privacy.MinGroupSizeSQL || aggregate.Average == nil {
		return nil, nil, nil
	}

	topics, err := s.insightTopics(period.ID, aggregate.Total)
	if err != nil {
		return nil, nil, err
	}
	urgent, err := s.insightUrgentIssues(period.ID)
	if err != nil {
		return nil, nil, err
	}

	average := round(*aggregate.Average, 2)
	positivePct := percent(aggregate.Positive, aggregate.Total)
	topicNamesTH, topicNamesEN := localizedTopicNames(topics)
	insight := models.AIInsight{
		OrgID:           s.orgID,
		PeriodID:        period.ID,
		OverallScore:    int(round(average/5*100, 0)),
		Confidence:      insightConfidence(aggregate.Total),
		Summary:         insightSummary(average, aggregate.Total, positivePct, topicNamesTH, topicNamesEN),
		IssueConfidence: topics,
	}

	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("org_id = ? AND period_id = ?", s.orgID, period.ID).Delete(&models.AIInsight{}).Error; err != nil {
			return err
		}
		if err := tx.Create(&insight).Error; err != nil {
			return err
		}
		if err := tx.Where("org_id = ? AND period_id = ?", s.orgID, period.ID).Delete(&models.UrgentIssue{}).Error; err != nil {
			return err
		}
		if len(urgent) > 0 {
			return tx.Create(&urgent).Error
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return &insight, urgent, nil
}

func (s *Service) insightTopics(periodID string, analyzed int64) ([]models.IssueConfidence, error) {
	var rows []struct {
		TopicID string
		Count   int64
	}
	err := s.db.Raw(`
		SELECT category.value AS topic_id, COUNT(DISTINCT survey_responses.id) AS count
		FROM survey_responses
		JOIN response_analysis ON response_analysis.response_id = survey_responses.id
		CROSS JOIN LATERAL jsonb_array_elements_text(response_analysis.categories::jsonb) AS category(value)
		WHERE survey_responses.org_id = ? AND survey_responses.period_id = ?
		GROUP BY category.value
		ORDER BY count DESC, category.value
		LIMIT 3`, s.orgID, periodID).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]models.IssueConfidence, 0, len(rows))
	for _, row := range rows {
		out = append(out, models.IssueConfidence{
			Label:      insightTopicLabel(row.TopicID),
			Confidence: percent(row.Count, analyzed),
		})
	}
	return out, nil
}

func (s *Service) insightUrgentIssues(periodID string) ([]models.UrgentIssue, error) {
	var rows []struct {
		DepartmentID   string
		DepartmentName string
		TopicID        string
		Count          int64
	}
	err := s.db.Raw(`
		SELECT survey_responses.department_id,
		       departments.name AS department_name,
		       category.value AS topic_id,
		       COUNT(DISTINCT survey_responses.id) AS count
		FROM survey_responses
		JOIN response_analysis ON response_analysis.response_id = survey_responses.id
		JOIN departments ON departments.id = survey_responses.department_id
		CROSS JOIN LATERAL jsonb_array_elements_text(response_analysis.categories::jsonb) AS category(value)
		WHERE survey_responses.org_id = ?
		  AND survey_responses.period_id = ?
		  AND response_analysis.sentiment_label = 'neg'
		GROUP BY survey_responses.department_id, departments.name, category.value
		HAVING COUNT(DISTINCT survey_responses.id) >= ?
		ORDER BY count DESC, departments.name, category.value
		LIMIT 3`, s.orgID, periodID, privacy.MinGroupSizeSQL).Scan(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]models.UrgentIssue, 0, len(rows))
	for i, row := range rows {
		out = append(out, models.UrgentIssue{
			ID:             uuid.NewString(),
			OrgID:          s.orgID,
			PeriodID:       periodID,
			Label:          insightTopicLabel(row.TopicID),
			DepartmentID:   row.DepartmentID,
			DepartmentName: models.Localized{TH: row.DepartmentName, EN: row.DepartmentName},
			Rank:           i + 1,
		})
	}
	return out, nil
}

func insightConfidence(total int64) string {
	switch {
	case total >= 20:
		return "high"
	case total >= 10:
		return "medium"
	default:
		return "low"
	}
}

func insightSummary(average float64, total int64, positivePct int, topicsTH, topicsEN []string) models.Localized {
	th := fmt.Sprintf("รอบนี้มีความพึงพอใจเฉลี่ย %.2f/5 จาก %d คำตอบ และ %d%% ของความคิดเห็นมีแนวโน้มเชิงบวก", average, total, positivePct)
	en := fmt.Sprintf("This period has an average satisfaction score of %.2f/5 from %d responses, with %d%% positive sentiment", average, total, positivePct)
	if len(topicsTH) > 0 {
		th += fmt.Sprintf(" หัวข้อที่ถูกกล่าวถึงมากที่สุดคือ %s", strings.Join(topicsTH, ", "))
		en += fmt.Sprintf(". The most frequently mentioned topics are %s", strings.Join(topicsEN, ", "))
	} else {
		th += ""
		en += "."
	}
	return models.Localized{TH: th, EN: en}
}

func localizedTopicNames(topics []models.IssueConfidence) ([]string, []string) {
	th := make([]string, 0, len(topics))
	en := make([]string, 0, len(topics))
	for _, topic := range topics {
		th = append(th, topic.Label.TH)
		en = append(en, topic.Label.EN)
	}
	return th, en
}

func insightTopicLabel(topicID string) models.Localized {
	labels := map[string]models.Localized{
		"work":         {TH: "การทำงานและภาระงาน", EN: "Work and workload"},
		"team":         {TH: "ทีมและเพื่อนร่วมงาน", EN: "Teamwork"},
		"manager":      {TH: "หัวหน้างาน", EN: "Management"},
		"compensation": {TH: "ค่าตอบแทน", EN: "Compensation"},
		"growth":       {TH: "การเติบโต", EN: "Growth"},
		"benefits":     {TH: "สวัสดิการ", EN: "Benefits"},
	}
	if label, ok := labels[topicID]; ok {
		return label
	}
	return models.Localized{TH: topicID, EN: topicID}
}

func percent(value, total int64) int {
	if total == 0 {
		return 0
	}
	return int(round(float64(value)/float64(total)*100, 0))
}
