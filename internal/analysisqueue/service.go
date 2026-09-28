package analysisqueue

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mt-sense/backend-service/internal/aiservice"
	"github.com/mt-sense/backend-service/internal/emergingtopics"
	"github.com/mt-sense/backend-service/internal/models"
	"github.com/mt-sense/backend-service/internal/privacy"
)

const (
	BatchSize    = 12
	MaximumWait  = 10 * time.Minute
	pollInterval = 30 * time.Second
	staleAfter   = 15 * time.Minute
)

type Service struct {
	db   *gorm.DB
	ai   *aiservice.Client
	wake chan struct{}
	mu   sync.Mutex
}

type scope struct {
	OrgID       string
	PeriodID    string
	Pending     int64
	OldestEntry time.Time
}

func New(db *gorm.DB, ai *aiservice.Client) *Service {
	return &Service{
		db:   db,
		ai:   ai,
		wake: make(chan struct{}, 1),
	}
}

func (s *Service) Start(ctx context.Context) {
	s.recoverStale()

	go func() {
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.processDue(ctx)
			case <-s.wake:
				s.processDue(ctx)
			}
		}
	}()
}

func (s *Service) Notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Service) FlushPeriod(ctx context.Context, orgID, periodID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for {
		batch, err := s.claimBatch(ctx, orgID, periodID, true)
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		if err := s.analyzeBatch(ctx, batch); err != nil {
			return err
		}
	}
}

func (s *Service) processDue(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recoverStale()

	for {
		candidate, found, err := s.nextDueScope(time.Now())
		if err != nil {
			log.Printf("analysis queue lookup failed: %v", err)
			return
		}
		if !found {
			return
		}

		batch, err := s.claimBatch(ctx, candidate.OrgID, candidate.PeriodID, false)
		if err != nil {
			log.Printf("analysis queue claim failed: %v", err)
			return
		}
		if len(batch) == 0 {
			return
		}
		if err := s.analyzeBatch(ctx, batch); err != nil {
			log.Printf("analysis queue batch failed: %v", err)
			return
		}
	}
}

func (s *Service) nextDueScope(now time.Time) (scope, bool, error) {
	var scopes []scope
	err := s.db.Raw(`
		SELECT org_id,
		       period_id,
		       COUNT(*) AS pending,
		       MIN(submitted_at) AS oldest_entry
		FROM survey_responses
		WHERE analysis_status IN ('pending', 'failed')
		  AND BTRIM(COALESCE(comment_text, '')) <> ''
		  AND (analysis_next_attempt_at IS NULL OR analysis_next_attempt_at <= ?)
		GROUP BY org_id, period_id
		HAVING COUNT(*) >= ? OR MIN(submitted_at) <= ?
		ORDER BY MIN(submitted_at)
		LIMIT 1
	`, now, BatchSize, now.Add(-MaximumWait)).Scan(&scopes).Error
	if err != nil {
		return scope{}, false, err
	}
	if len(scopes) == 0 {
		return scope{}, false, nil
	}

	return scopes[0], true, nil
}

func (s *Service) claimBatch(ctx context.Context, orgID, periodID string, force bool) ([]models.SurveyResponse, error) {
	claimed := []models.SurveyResponse{}
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		query := tx.Clauses(clause.Locking{Strength: "UPDATE", Options: "SKIP LOCKED"}).
			Where("org_id = ? AND period_id = ?", orgID, periodID).
			Where("analysis_status IN ?", []string{"pending", "failed"}).
			Where("BTRIM(COALESCE(comment_text, '')) <> ''")

		if !force {
			query = query.Where("analysis_next_attempt_at IS NULL OR analysis_next_attempt_at <= ?", time.Now())
		}
		if err := query.Order("submitted_at, id").Limit(BatchSize).Find(&claimed).Error; err != nil {
			return err
		}
		if len(claimed) == 0 {
			return nil
		}

		ids := responseIDs(claimed)
		now := time.Now()
		return tx.Model(&models.SurveyResponse{}).Where("id IN ?", ids).Updates(map[string]any{
			"analysis_status":          "processing",
			"analysis_started_at":      &now,
			"analysis_last_error":      "",
			"analysis_next_attempt_at": nil,
		}).Error
	})

	return claimed, err
}

func (s *Service) analyzeBatch(ctx context.Context, batch []models.SurveyResponse) error {
	texts := make([]string, 0, len(batch))
	for _, response := range batch {
		texts = append(texts, response.CommentText)
	}

	results, err := s.ai.AnalyzeMany(ctx, texts)
	if err != nil {
		s.markFailed(batch, err)
		return err
	}

	now := time.Now()
	err = s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for index, response := range batch {
			result := results[index]
			analysis := models.ResponseAnalysis{
				ID:             uuid.NewString(),
				ResponseID:     response.ID,
				SentimentLabel: result.SentimentLabel,
				SentimentScore: result.SentimentScore,
				Confidence:     result.Confidence,
				LowConfidence:  result.LowConfidence,
				Categories:     result.Categories,
				Reason:         privacy.Redact(result.Reason),
				AnalyzedAt:     now,
			}
			if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "response_id"}}, DoNothing: true}).Create(&analysis).Error; err != nil {
				return err
			}
			if err := emergingtopics.Save(tx, response.OrgID, response.ID, result.EmergingTopics); err != nil {
				return err
			}
		}

		return tx.Model(&models.SurveyResponse{}).Where("id IN ?", responseIDs(batch)).Updates(map[string]any{
			"analysis_status":          "analyzed",
			"analysis_started_at":      nil,
			"analysis_last_error":      "",
			"analysis_next_attempt_at": nil,
		}).Error
	})
	if err != nil {
		s.markFailed(batch, err)
		return err
	}
	log.Printf("analysis queue processed %d responses", len(batch))

	return nil
}

func (s *Service) markFailed(batch []models.SurveyResponse, analysisErr error) {
	next := time.Now().Add(MaximumWait)
	errorText := fmt.Sprintf("%v", analysisErr)
	if len(errorText) > 500 {
		errorText = errorText[:500]
	}

	if err := s.db.Model(&models.SurveyResponse{}).Where("id IN ?", responseIDs(batch)).Updates(map[string]any{
		"analysis_status":          "failed",
		"analysis_attempts":        gorm.Expr("analysis_attempts + 1"),
		"analysis_last_error":      errorText,
		"analysis_next_attempt_at": &next,
		"analysis_started_at":      nil,
	}).Error; err != nil {
		log.Printf("analysis queue could not record failure: %v", err)
	}
}

func (s *Service) recoverStale() {
	err := s.db.Model(&models.SurveyResponse{}).
		Where("analysis_status = ? AND analysis_started_at < ?", "processing", time.Now().Add(-staleAfter)).
		Updates(map[string]any{
			"analysis_status":          "pending",
			"analysis_started_at":      nil,
			"analysis_next_attempt_at": nil,
		}).Error
	if err != nil {
		log.Printf("analysis queue stale recovery failed: %v", err)
	}
}

func responseIDs(responses []models.SurveyResponse) []string {
	ids := make([]string, 0, len(responses))
	for _, response := range responses {
		ids = append(ids, response.ID)
	}

	return ids
}
