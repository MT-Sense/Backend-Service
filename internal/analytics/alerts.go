package analytics

import (
	"fmt"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/mt-sense/backend-service/internal/models"
	"github.com/mt-sense/backend-service/internal/privacy"
)

const (
	lowSatisfactionThreshold = 3.0
	lowResponseRateThreshold = 0.5 // 50%
	sentimentDropThreshold   = 10  // percentage points, negative bucket vs previous period
)

// GenerateAlerts computes and replaces the alerts for a period. It is idempotent (deletes
// whatever was there before for this org+period, then reinserts), so it is cheap to call
// on demand — from HR closing a period, or lazily when the alerts panel loads — with no
// scheduler needed.
func (s *Service) GenerateAlerts(period *models.SurveyPeriod) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("org_id = ? AND period_id = ?", s.orgID, period.ID).Delete(&models.Alert{}).Error; err != nil {
			return err
		}

		var alerts []models.Alert

		deptAlerts, err := s.lowSatisfactionDepartmentAlerts(tx, period)
		if err != nil {
			return err
		}
		alerts = append(alerts, deptAlerts...)

		posAlerts, err := s.lowSatisfactionPositionAlerts(tx, period)
		if err != nil {
			return err
		}
		alerts = append(alerts, posAlerts...)

		responseRateAlert, err := s.lowResponseRateAlert(tx, period)
		if err != nil {
			return err
		}
		if responseRateAlert != nil {
			alerts = append(alerts, *responseRateAlert)
		}

		sentimentAlert, err := s.sentimentDropAlert(period)
		if err != nil {
			return err
		}
		if sentimentAlert != nil {
			alerts = append(alerts, *sentimentAlert)
		}

		if len(alerts) == 0 {
			return nil
		}
		return tx.Create(&alerts).Error
	})
}

func (s *Service) lowSatisfactionDepartmentAlerts(tx *gorm.DB, period *models.SurveyPeriod) ([]models.Alert, error) {
	var rows []struct {
		DepartmentID string
		Score        float64
	}
	err := tx.Model(&models.SurveyResponse{}).
		Where("period_id = ? AND department_id IS NOT NULL", period.ID).
		Group("department_id").
		Having("COUNT(*) >= ? AND AVG(satisfaction_score) < ?", privacy.MinGroupSizeSQL, lowSatisfactionThreshold).
		Select("department_id, ROUND(AVG(satisfaction_score)::numeric, 1)::float8 AS score").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	now := time.Now()
	out := make([]models.Alert, 0, len(rows))
	for _, r := range rows {
		deptID := r.DepartmentID
		out = append(out, models.Alert{
			ID:                  uuid.NewString(),
			OrgID:               s.orgID,
			PeriodID:            period.ID,
			AlertType:           models.AlertLowSatisfactionDepartment,
			Severity:            severityFor(r.Score),
			Message:             fmt.Sprintf("แผนกมีคะแนนความพึงพอใจเฉลี่ย %.1f/5 ต่ำกว่าเกณฑ์", r.Score),
			RelatedDepartmentID: &deptID,
			CreatedAt:           now,
		})
	}
	return out, nil
}

func (s *Service) lowSatisfactionPositionAlerts(tx *gorm.DB, period *models.SurveyPeriod) ([]models.Alert, error) {
	var rows []struct {
		PositionID string
		Score      float64
	}
	err := tx.Model(&models.SurveyResponse{}).
		Where("period_id = ? AND position_id IS NOT NULL", period.ID).
		Group("position_id").
		Having("COUNT(*) >= ? AND AVG(satisfaction_score) < ?", privacy.MinGroupSizeSQL, lowSatisfactionThreshold).
		Select("position_id, ROUND(AVG(satisfaction_score)::numeric, 1)::float8 AS score").
		Scan(&rows).Error
	if err != nil {
		return nil, err
	}

	now := time.Now()
	out := make([]models.Alert, 0, len(rows))
	for _, r := range rows {
		posID := r.PositionID
		out = append(out, models.Alert{
			ID:                 uuid.NewString(),
			OrgID:              s.orgID,
			PeriodID:           period.ID,
			AlertType:          models.AlertLowSatisfactionPosition,
			Severity:           severityFor(r.Score),
			Message:            fmt.Sprintf("ตำแหน่งมีคะแนนความพึงพอใจเฉลี่ย %.1f/5 ต่ำกว่าเกณฑ์", r.Score),
			RelatedPositionID:  &posID,
			CreatedAt:          now,
		})
	}
	return out, nil
}

func (s *Service) lowResponseRateAlert(tx *gorm.DB, period *models.SurveyPeriod) (*models.Alert, error) {
	responded, err := s.TotalRespondents(period.ID)
	if err != nil {
		return nil, err
	}
	var headcount int64
	err = tx.Model(&models.User{}).Where("org_id = ? AND is_active = ?", s.orgID, true).Count(&headcount).Error
	if err != nil {
		return nil, err
	}
	if headcount == 0 {
		return nil, nil
	}
	rate := float64(responded) / float64(headcount)
	if rate >= lowResponseRateThreshold {
		return nil, nil
	}
	return &models.Alert{
		ID:        uuid.NewString(),
		OrgID:     s.orgID,
		PeriodID:  period.ID,
		AlertType: models.AlertLowResponseRate,
		Severity:  models.SeverityWarning,
		Message:   fmt.Sprintf("อัตราการตอบแบบสอบถามอยู่ที่ %.0f%% ต่ำกว่าเป้าหมาย", rate*100),
		CreatedAt: time.Now(),
	}, nil
}

func (s *Service) sentimentDropAlert(period *models.SurveyPeriod) (*models.Alert, error) {
	prev, err := s.PreviousPeriod(period)
	if err != nil || prev == nil {
		return nil, err
	}
	current, err := s.SentimentSplit(period.ID)
	if err != nil {
		return nil, err
	}
	previous, err := s.SentimentSplit(prev.ID)
	if err != nil {
		return nil, err
	}
	delta := current.Negative - previous.Negative
	if delta < sentimentDropThreshold {
		return nil, nil
	}
	return &models.Alert{
		ID:        uuid.NewString(),
		OrgID:     s.orgID,
		PeriodID:  period.ID,
		AlertType: models.AlertSentimentDrop,
		Severity:  models.SeverityCritical,
		Message:   fmt.Sprintf("สัดส่วนความคิดเห็นเชิงลบเพิ่มขึ้น %d จุดเทียบกับเดือนก่อน", delta),
		CreatedAt: time.Now(),
	}, nil
}

func severityFor(score float64) models.AlertSeverity {
	if score < 2.5 {
		return models.SeverityCritical
	}
	return models.SeverityWarning
}
