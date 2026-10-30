package analytics

import (
	"fmt"
	"strings"

	"github.com/mt-sense/backend-service/internal/aiservice"
	"github.com/mt-sense/backend-service/internal/privacy"
)

// TopicSummaryEvidence gates scope inside the same SQL statement that selects
// comments. Only redacted text and temporary references leave the backend.
func (s *Service) TopicSummaryEvidence(periodID, topicID, departmentID string) ([]aiservice.TopicComment, error) {
	query, err := s.departmentTopicResponses(periodID, topicID, departmentID)
	if err != nil {
		return nil, err
	}
	gate, err := s.departmentTopicResponses(periodID, topicID, departmentID)
	if err != nil {
		return nil, err
	}
	gate = gate.Select("survey_responses.period_id").Group("survey_responses.period_id").
		Having("COUNT(DISTINCT survey_responses.id) >= ?", privacy.MinGroupSizeSQL)
	var rows []struct{ CommentText string }
	if err := query.Where("survey_responses.period_id IN (?)", gate).
		Where("length(btrim(survey_responses.comment_text)) >= 12").
		Select("survey_responses.comment_text").
		Order("survey_responses.submitted_at DESC, survey_responses.id DESC").Limit(120).Scan(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]aiservice.TopicComment, 0, len(rows))
	for _, row := range rows {
		text := strings.TrimSpace(privacy.Redact(row.CommentText))
		runes := []rune(text)
		if len(runes) < 12 || text == "[ถูกปกปิด]" {
			continue
		}
		if len(runes) > 1200 {
			text = string(runes[:1200])
		}
		out = append(out, aiservice.TopicComment{Ref: fmt.Sprintf("c%d", len(out)+1), Text: text})
	}
	return out, nil
}
