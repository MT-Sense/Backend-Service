package models

import "time"

type EmailStatus string

const (
	EmailPending EmailStatus = "pending"
	EmailSending EmailStatus = "sending"
	EmailSent    EmailStatus = "sent"
	EmailFailed  EmailStatus = "failed"
)

// EmailOutbox is the transactional outbox for outgoing mail. Handlers insert rendered
// messages here — in the same transaction as the change that triggers them — and the mailer
// worker delivers them. SendAfter defers delivery, e.g. a round reminder until opens_at.
//
// DedupeKey, when set, makes enqueueing idempotent (unique per kind), so a retried request
// cannot email the same person twice for the same event.
type EmailOutbox struct {
	ID        string      `gorm:"column:id;primaryKey;size:64" json:"id"`
	OrgID     string      `gorm:"column:org_id;size:64;not null;index" json:"-"`
	Kind      string      `gorm:"column:kind;size:64;not null;uniqueIndex:uq_email_outbox_dedupe,priority:1" json:"kind"`
	DedupeKey *string     `gorm:"column:dedupe_key;size:255;uniqueIndex:uq_email_outbox_dedupe,priority:2" json:"-"`
	ToEmail   string      `gorm:"column:to_email;size:255;not null" json:"toEmail"`
	ToName    string      `gorm:"column:to_name;size:255;not null" json:"-"`
	Subject   string      `gorm:"column:subject;size:500;not null" json:"subject"`
	TextBody  string      `gorm:"column:text_body;type:text;not null" json:"-"`
	HTMLBody  string      `gorm:"column:html_body;type:text;not null" json:"-"`
	Status    EmailStatus `gorm:"column:status;size:16;not null;index:idx_email_outbox_due,priority:1" json:"status"`
	Attempts  int         `gorm:"column:attempts;not null" json:"attempts"`
	LastError string      `gorm:"column:last_error;type:text;not null" json:"-"`
	SendAfter time.Time   `gorm:"column:send_after;not null;index:idx_email_outbox_due,priority:2" json:"sendAfter"`
	LockedAt  *time.Time  `gorm:"column:locked_at" json:"-"`
	SentAt    *time.Time  `gorm:"column:sent_at" json:"sentAt"`
	CreatedBy *string     `gorm:"column:created_by;size:64" json:"-"`
	CreatedAt time.Time   `gorm:"column:created_at" json:"createdAt"`
}

func (EmailOutbox) TableName() string { return "email_outbox" }
