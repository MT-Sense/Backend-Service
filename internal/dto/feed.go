package dto

import (
	"strings"
	"unicode/utf8"

	"github.com/mt-sense/backend-service/internal/models"
)

// FeedPost mirrors the frontend's FeedPost. OptedIn and Published are always true on the
// public feed — they are carried so the client type matches, not because the value varies.
type FeedPost struct {
	ID        string   `json:"id"`
	Text      string   `json:"text"`
	Hashtags  []string `json:"hashtags"`
	CreatedAt string   `json:"createdAt"`
	Upvotes   int      `json:"upvotes"`
	Downvotes int      `json:"downvotes"`
	HRReplied bool     `json:"hrReplied"`
	OptedIn   bool     `json:"optedIn"`
	Published bool     `json:"published"`
	HRReply   string   `json:"hrReply"`
	// MyVote is the caller's own vote on this post: 1, -1, or 0 for none.
	MyVote int `json:"myVote"`
	// ActionItem is what the company is doing about this post, or null.
	ActionItem *LinkedActionItem `json:"actionItem"`
	// HRRepliedAt is a YYYY-MM-DD date like CreatedAt, or "" when there is no reply.
	HRRepliedAt string `json:"hrRepliedAt"`
}

func NewFeedPost(p *models.FeedPost) FeedPost {
	hashtags := p.Hashtags
	if hashtags == nil {
		hashtags = []string{}
	}
	repliedAt := ""
	if p.HRRepliedAt != nil {
		repliedAt = p.HRRepliedAt.Format("2006-01-02")
	}
	return FeedPost{
		HRReply:     p.HRReply,
		HRRepliedAt: repliedAt,
		ID:        p.ID,
		Text:      p.Text,
		Hashtags:  hashtags,
		CreatedAt: p.PostedOn,
		Upvotes:   p.Upvotes,
		Downvotes: p.Downvotes,
		HRReplied: p.HRReplied,
		OptedIn:   p.OptedIn,
		Published: p.Published,
	}
}

func NewFeedPosts(posts []models.FeedPost) []FeedPost {
	out := make([]FeedPost, 0, len(posts))
	for i := range posts {
		out = append(out, NewFeedPost(&posts[i]))
	}
	return out
}

type FeedList struct {
	Posts  []FeedPost `json:"posts"`
	Total  int64      `json:"total"`
	Limit  int        `json:"limit"`
	Offset int        `json:"offset"`
}

type VoteRequest struct {
	Direction int `json:"direction"` // +1 or -1
}

func (r *VoteRequest) Validate() []string {
	if r.Direction != 1 && r.Direction != -1 {
		return []string{"direction must be 1 or -1"}
	}
	return nil
}

const MaxHRReplyLength = 2000

// ModerateRequest is gate two of the publish flow. Fields are pointers so "not supplied" is
// distinguishable from "set to false/empty". HRReply sets HR's public answer; an empty
// string removes it. HRReplied is kept for older clients and is ignored when HRReply is set.
type ModerateRequest struct {
	Published *bool   `json:"published"`
	HRReplied *bool   `json:"hrReplied"`
	HRReply   *string `json:"hrReply"`
	// ActionItemID links an action item of the same org; "" removes the link.
	ActionItemID *string `json:"actionItemId"`
}

func (r *ModerateRequest) IsEmpty() bool {
	return r.Published == nil && r.HRReplied == nil && r.HRReply == nil && r.ActionItemID == nil
}

// LinkedActionItem is the slice of an action item shown next to a feed post — enough for
// "in progress / done", nothing about who opened it.
type LinkedActionItem struct {
	ID         string    `json:"id"`
	Topic      Localized `json:"topic"`
	Status     string    `json:"status"`
	TargetDate string    `json:"targetDate"`
	DoneAt     string    `json:"doneAt"`
}

func NewLinkedActionItem(a *models.ActionItem) *LinkedActionItem {
	full := NewActionItem(a)
	return &LinkedActionItem{ID: full.ID, Topic: full.Topic, Status: full.Status, TargetDate: full.TargetDate, DoneAt: full.DoneAt}
}

func (r *ModerateRequest) Validate() []string {
	if r.HRReply != nil {
		*r.HRReply = strings.TrimSpace(*r.HRReply)
		if utf8.RuneCountInString(*r.HRReply) > MaxHRReplyLength {
			return []string{"reply is too long"}
		}
	}
	return nil
}

type PublishedSummary struct {
	ID          string    `json:"id"`
	Title       Localized `json:"title"`
	Body        Localized `json:"body"`
	PublishedAt string    `json:"publishedAt"`
	PeriodID    string    `json:"periodId"`
	Status      string    `json:"status"`
}

func NewPublishedSummaries(summaries []models.PublishedSummary) []PublishedSummary {
	out := make([]PublishedSummary, 0, len(summaries))
	for _, s := range summaries {
		out = append(out, NewPublishedSummary(&s))
	}
	return out
}

func NewPublishedSummary(s *models.PublishedSummary) PublishedSummary {
	periodID := ""
	if s.PeriodID != nil {
		periodID = *s.PeriodID
	}
	status := s.Status
	if status == "" {
		status = models.SummaryPublished
	}
	return PublishedSummary{
		ID:          s.ID,
		Title:       s.Title,
		Body:        s.Body,
		PublishedAt: s.PublishedAt,
		PeriodID:    periodID,
		Status:      status,
	}
}

type ActionItem struct {
	ID         string    `json:"id"`
	Topic      Localized `json:"topic"`
	TopicID    string    `json:"topicId"`
	Assignee   string    `json:"assignee"`
	Status     string    `json:"status"`
	CreatedBy  string    `json:"createdBy"`
	Level      string    `json:"level"`
	TargetDate string    `json:"targetDate"`
	// DoneAt is YYYY-MM-DD, or "" while in progress.
	DoneAt string `json:"doneAt"`
	// CreatedByMe tells an executive's UI which items it may edit.
	CreatedByMe bool `json:"createdByMe"`
}

func NewActionItem(a *models.ActionItem) ActionItem {
	doneAt := ""
	if a.DoneAt != nil {
		doneAt = a.DoneAt.Format("2006-01-02")
	}
	return ActionItem{
		ID:         a.ID,
		Topic:      a.Topic,
		TopicID:    a.TopicID,
		Assignee:   a.Assignee,
		Status:     a.Status,
		CreatedBy:  string(a.CreatedBy),
		Level:      a.Level,
		TargetDate: a.TargetDate,
		DoneAt:     doneAt,
	}
}

// NewActionItemFor marks whether userID opened the item.
func NewActionItemFor(a *models.ActionItem, userID string) ActionItem {
	out := NewActionItem(a)
	out.CreatedByMe = a.CreatedByUserID != nil && *a.CreatedByUserID == userID
	return out
}

func NewActionItems(items []models.ActionItem, userID string) []ActionItem {
	out := make([]ActionItem, 0, len(items))
	for i := range items {
		out = append(out, NewActionItemFor(&items[i], userID))
	}
	return out
}

type CreateActionItemRequest struct {
	Topic      Localized `json:"topic"`
	TopicID    string    `json:"topicId"`
	Assignee   string    `json:"assignee"`
	TargetDate string    `json:"targetDate"`
}

func (r *CreateActionItemRequest) Validate() []string {
	var problems []string
	if r.Topic.TH == "" && r.Topic.EN == "" {
		problems = append(problems, "topic is required")
	}
	r.Assignee = strings.TrimSpace(r.Assignee)
	if utf8.RuneCountInString(r.Assignee) > MaxAssigneeLength {
		problems = append(problems, "assignee is too long")
	}
	r.TargetDate = strings.TrimSpace(r.TargetDate)
	if !validDate(r.TargetDate) {
		problems = append(problems, "targetDate must be YYYY-MM-DD")
	}
	return problems
}
