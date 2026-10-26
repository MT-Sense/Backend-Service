package mailer

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/mt-sense/backend-service/internal/models"
)

const (
	batchSize    = 20
	maxAttempts  = 6
	pollInterval = 30 * time.Second
	staleAfter   = 10 * time.Minute
	sendTimeout  = 45 * time.Second
)

// retryDelays is the wait after attempt n (1-based); the last entry repeats.
var retryDelays = []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour}

// Service renders and enqueues emails, and runs the delivery worker.
type Service struct {
	db      *gorm.DB
	sender  Sender
	baseURL string
	wake    chan struct{}
}

func New(db *gorm.DB, sender Sender, baseURL string) *Service {
	return &Service{db: db, sender: sender, baseURL: baseURL, wake: make(chan struct{}, 1)}
}

func (s *Service) Configured() bool { return s.sender.Configured() }
func (s *Service) From() string     { return s.sender.From() }

// Start launches the delivery worker. Rows stuck in "sending" (the process died mid-send)
// are returned to the queue first; that can at worst deliver one message twice, which is
// preferable to silently dropping it.
//
// Claiming uses FOR UPDATE SKIP LOCKED, so several backend replicas can run the worker
// against one database without sending the same row twice.
func (s *Service) Start(ctx context.Context) {
	if err := s.db.Model(&models.EmailOutbox{}).
		Where("status = ? AND locked_at < ?", models.EmailSending, time.Now().Add(-staleAfter)).
		Updates(map[string]any{"status": models.EmailPending, "locked_at": nil}).Error; err != nil {
		log.Printf("mailer: recovering stale rows: %v", err)
	}

	go func() {
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		for {
			s.processDue(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			case <-s.wake:
			}
		}
	}()
}

// Notify wakes the worker after a commit instead of waiting for the next poll.
func (s *Service) Notify() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Enqueue inserts rows inside tx. Rows whose (kind, dedupe_key) already exist are skipped.
func (s *Service) Enqueue(tx *gorm.DB, rows []models.EmailOutbox) error {
	if len(rows) == 0 {
		return nil
	}
	now := time.Now()
	for i := range rows {
		if rows[i].ID == "" {
			rows[i].ID = uuid.NewString()
		}
		rows[i].Status = models.EmailPending
		if rows[i].SendAfter.IsZero() {
			rows[i].SendAfter = now
		}
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(rows, 200).Error
}

// SendNow delivers a message synchronously, bypassing the outbox. Only the SMTP test uses
// it, because the caller wants the real error back.
func (s *Service) SendNow(ctx context.Context, msg Message) error {
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	return s.sender.Send(ctx, msg)
}

func (s *Service) processDue(ctx context.Context) {
	for ctx.Err() == nil {
		rows, err := s.claim()
		if err != nil {
			log.Printf("mailer: claiming due emails: %v", err)
			return
		}
		if len(rows) == 0 {
			return
		}
		for i := range rows {
			s.deliver(ctx, &rows[i])
		}
	}
}

func (s *Service) claim() ([]models.EmailOutbox, error) {
	var rows []models.EmailOutbox
	err := s.db.Raw(`
		UPDATE email_outbox
		SET status = ?, locked_at = NOW(), attempts = attempts + 1
		WHERE id IN (
			SELECT id FROM email_outbox
			WHERE status = ? AND send_after <= NOW()
			ORDER BY send_after
			LIMIT ?
			FOR UPDATE SKIP LOCKED
		)
		RETURNING *`, models.EmailSending, models.EmailPending, batchSize).Scan(&rows).Error
	return rows, err
}

func (s *Service) deliver(ctx context.Context, row *models.EmailOutbox) {
	sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	err := s.sender.Send(sendCtx, Message{
		ToEmail: row.ToEmail, ToName: row.ToName, Subject: row.Subject, Text: row.TextBody, HTML: row.HTMLBody,
	})
	cancel()

	updates := map[string]any{"locked_at": nil}
	switch {
	case err == nil:
		now := time.Now()
		updates["status"] = models.EmailSent
		updates["sent_at"] = &now
		updates["last_error"] = ""
	case errors.As(err, new(*PermanentError)) || row.Attempts >= maxAttempts:
		updates["status"] = models.EmailFailed
		updates["last_error"] = err.Error()
		log.Printf("mailer: giving up on email %s (%s) after %d attempt(s): %v", row.ID, row.Kind, row.Attempts, err)
	default:
		updates["status"] = models.EmailPending
		updates["last_error"] = err.Error()
		updates["send_after"] = time.Now().Add(retryDelay(row.Attempts))
		log.Printf("mailer: email %s (%s) attempt %d failed, will retry: %v", row.ID, row.Kind, row.Attempts, err)
	}
	// A reset link must not sit in the outbox after it is no longer needed.
	if row.Kind == KindPasswordReset && updates["status"] != models.EmailPending {
		updates["text_body"] = scrubbedBody
		updates["html_body"] = scrubbedBody
	}
	if err := s.db.Model(&models.EmailOutbox{}).Where("id = ?", row.ID).Updates(updates).Error; err != nil {
		log.Printf("mailer: recording result for email %s: %v", row.ID, err)
	}
}

const scrubbedBody = "[removed after delivery]"

func retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > len(retryDelays) {
		return retryDelays[len(retryDelays)-1]
	}
	return retryDelays[attempt-1]
}

// QueueStats counts undelivered mail for an org, for the settings screen.
func (s *Service) QueueStats(orgID string) (pending, failed int64, err error) {
	var row struct{ Pending, Failed int64 }
	err = s.db.Model(&models.EmailOutbox{}).
		Where("org_id = ?", orgID).
		Select(`COUNT(*) FILTER (WHERE status IN ('pending','sending')) AS pending,
			COUNT(*) FILTER (WHERE status = 'failed' AND created_at > NOW() - INTERVAL '7 days') AS failed`).
		Scan(&row).Error
	return row.Pending, row.Failed, err
}

func dedupeKey(format string, args ...any) *string {
	key := fmt.Sprintf(format, args...)
	return &key
}
