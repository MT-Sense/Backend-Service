package models

import "time"

type RoutingRule struct {
	ID           string   `json:"id"`
	Topic        string   `json:"topic"`
	Keywords     []string `json:"keywords"`
	DepartmentID string   `json:"departmentId"`
	Enabled      bool     `json:"enabled"`
}
type RoutingPolicy struct {
	OrgID   string        `gorm:"primaryKey;size:64" json:"-"`
	Version int           `json:"version"`
	Rules   []RoutingRule `gorm:"serializer:json" json:"rules"`
}
type RoutingHistory struct {
	At           time.Time `json:"at"`
	Actor        string    `json:"actor"`
	Action       string    `json:"action"`
	Note         string    `json:"note"`
	Topic        string    `json:"topic"`
	DepartmentID string    `json:"departmentId"`
}
type RoutingCase struct {
	ID           string           `gorm:"primaryKey;size:64" json:"id"`
	OrgID        string           `gorm:"index;not null;size:64" json:"-"`
	Title        string           `json:"title"`
	Summary      string           `gorm:"type:text" json:"summary"`
	Topic        string           `json:"topic"`
	DepartmentID string           `json:"departmentId"`
	Status       string           `json:"status"`
	MatchReason  string           `json:"matchReason"`
	Version      int              `json:"version"`
	History      []RoutingHistory `gorm:"serializer:json" json:"history"`
	CreatedAt    time.Time        `json:"createdAt"`
	UpdatedAt    time.Time        `json:"updatedAt"`
}
