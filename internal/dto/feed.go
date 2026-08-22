package dto

import "github.com/mt-sense/backend-service/internal/models"

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
}

func NewFeedPost(p *models.FeedPost) FeedPost {
	hashtags := p.Hashtags
	if hashtags == nil {
		hashtags = []string{}
	}
	return FeedPost{
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

// ModerateRequest is gate two of the publish flow. Both fields are pointers so "not
// supplied" is distinguishable from "set to false".
type ModerateRequest struct {
	Published *bool `json:"published"`
	HRReplied *bool `json:"hrReplied"`
}

func (r *ModerateRequest) IsEmpty() bool {
	return r.Published == nil && r.HRReplied == nil
}

type PublishedSummary struct {
	ID          string    `json:"id"`
	Title       Localized `json:"title"`
	Body        Localized `json:"body"`
	PublishedAt string    `json:"publishedAt"`
}

func NewPublishedSummaries(summaries []models.PublishedSummary) []PublishedSummary {
	out := make([]PublishedSummary, 0, len(summaries))
	for _, s := range summaries {
		out = append(out, PublishedSummary{
			ID:          s.ID,
			Title:       s.Title,
			Body:        s.Body,
			PublishedAt: s.PublishedAt,
		})
	}
	return out
}

type ActionItem struct {
	ID         string    `json:"id"`
	Topic      Localized `json:"topic"`
	Assignee   string    `json:"assignee"`
	Status     string    `json:"status"`
	CreatedBy  string    `json:"createdBy"`
	Level      string    `json:"level"`
	TargetDate string    `json:"targetDate"`
}

func NewActionItem(a *models.ActionItem) ActionItem {
	return ActionItem{
		ID:         a.ID,
		Topic:      a.Topic,
		Assignee:   a.Assignee,
		Status:     a.Status,
		CreatedBy:  string(a.CreatedBy),
		Level:      a.Level,
		TargetDate: a.TargetDate,
	}
}

func NewActionItems(items []models.ActionItem) []ActionItem {
	out := make([]ActionItem, 0, len(items))
	for i := range items {
		out = append(out, NewActionItem(&items[i]))
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
	if r.Topic.TH == "" && r.Topic.EN == "" {
		return []string{"topic is required"}
	}
	return nil
}
