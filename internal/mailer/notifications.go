package mailer

import (
	"net/url"
	"time"

	"gorm.io/gorm"

	"github.com/mt-sense/backend-service/internal/models"
)

const (
	KindNewRound = "new_round"
	KindSummary  = "dashboard_summary"
)

// EnqueueNewRound queues a "survey is open" email for every active member of the period's
// org who left notify_new_round on. A round scheduled for later is delivered at opens_at.
func (s *Service) EnqueueNewRound(tx *gorm.DB, period *models.SurveyPeriod) error {
	var org models.Organization
	if err := tx.Select("id", "name").Where("id = ?", period.OrgID).First(&org).Error; err != nil {
		return err
	}
	var users []models.User
	if err := tx.Select("id", "email", "full_name").
		Where("org_id = ? AND is_active = ? AND notify_new_round = ?", period.OrgID, true, true).
		Find(&users).Error; err != nil {
		return err
	}

	sendAfter := period.OpensAt
	if now := time.Now(); sendAfter.Before(now) {
		sendAfter = now
	}
	rows := make([]models.EmailOutbox, 0, len(users))
	for _, u := range users {
		if !ValidAddress(u.Email) {
			continue
		}
		r, err := renderNewRound(s.baseURL, displayName(u), org.Name, int(period.Month), int(period.Year), period.ClosesAt)
		if err != nil {
			return err
		}
		rows = append(rows, models.EmailOutbox{
			OrgID: period.OrgID, Kind: KindNewRound,
			DedupeKey: dedupeKey("period:%s:user:%s", period.ID, u.ID),
			ToEmail:   u.Email, ToName: u.FullName,
			Subject: r.Subject, TextBody: r.Text, HTMLBody: r.HTML,
			SendAfter: sendAfter,
		})
	}
	return s.Enqueue(tx, rows)
}

// EnqueueSummary queues the dashboard summary for each recipient. Recipients must already
// be validated as active leaders of the sender's org.
func (s *Service) EnqueueSummary(tx *gorm.DB, sender models.User, orgName, note string, m SummaryMetrics, recipients []models.User) error {
	rows := make([]models.EmailOutbox, 0, len(recipients))
	for _, u := range recipients {
		path := "/dashboard"
		if u.Role == models.RoleExecutive {
			path = "/executive"
		}
		r, err := renderSummary(s.baseURL, path, displayName(u), orgName, displayName(sender), note, m)
		if err != nil {
			return err
		}
		senderID := sender.ID
		rows = append(rows, models.EmailOutbox{
			OrgID: sender.OrgID, Kind: KindSummary,
			ToEmail: u.Email, ToName: u.FullName,
			Subject: r.Subject, TextBody: r.Text, HTMLBody: r.HTML,
			CreatedBy: &senderID,
		})
	}
	return s.Enqueue(tx, rows)
}

// TestMessage renders the SMTP test email for user.
func (s *Service) TestMessage(user models.User) (Message, error) {
	r, err := renderTest(displayName(user), time.Now())
	if err != nil {
		return Message{}, err
	}
	return r.to(user.Email, user.FullName), nil
}

func displayName(u models.User) string {
	if u.FullName != "" {
		return u.FullName
	}
	return u.Email
}

// KindPasswordReset bodies carry a live reset link, so the worker blanks them once the
// message is delivered or given up on.
const KindPasswordReset = "password_reset"

// EnqueuePasswordReset queues the reset-link email. token is the plaintext; only its hash is
// stored in password_reset_tokens.
func (s *Service) EnqueuePasswordReset(tx *gorm.DB, user models.User, token string, expiresAt time.Time, requestedByHR bool) error {
	link := s.baseURL + "/reset-password?token=" + url.QueryEscape(token)
	r, err := renderPasswordReset(displayName(user), link, expiresAt, requestedByHR)
	if err != nil {
		return err
	}
	return s.Enqueue(tx, []models.EmailOutbox{{
		OrgID: user.OrgID, Kind: KindPasswordReset,
		ToEmail: user.Email, ToName: user.FullName,
		Subject: r.Subject, TextBody: r.Text, HTMLBody: r.HTML,
	}})
}
