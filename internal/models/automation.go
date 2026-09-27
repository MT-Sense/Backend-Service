package models

import "time"

// AutomationSettings is one organization's connector and review policy. Secrets never
// leave the server; token encryption uses a separate deployment key and organization AAD.
type AutomationSettings struct {
	OrgID              string            `gorm:"primaryKey;size:64" json:"-"`
	Revision           int               `json:"revision"`
	MinResponses       int               `json:"minResponses"`
	MentionPercent     int               `json:"mentionPercent"`
	ToolName           string            `json:"toolName"`
	ConnectorType      string            `json:"connectorType"`
	ToolInstructions   string            `gorm:"type:text" json:"toolInstructions"`
	JiraSite           string            `json:"jiraSite"`
	JiraEmail          string            `json:"jiraEmail"`
	JiraProject        string            `json:"jiraProject"`
	JiraIssueType      string            `json:"jiraIssueType"`
	JiraToken          string            `gorm:"type:text" json:"-"`
	AllowJiraWrite     bool              `json:"allowJiraWrite"`
	DepartmentProjects map[string]string `gorm:"serializer:json" json:"departmentProjects"`
	TrainingCatalog    string            `gorm:"type:text" json:"trainingCatalog"`
	ApprovalPolicy     string            `gorm:"type:text" json:"approvalPolicy"`
	BenefitsPolicy     string            `gorm:"type:text" json:"benefitsPolicy"`
	UpdatedAt          time.Time         `json:"updatedAt"`
}

type AutomationEvidence struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Text   string `json:"text"`
}

type AutomationProposal struct {
	ID               string               `gorm:"primaryKey;size:64" json:"id"`
	OrgID            string               `gorm:"size:64;not null;index" json:"-"`
	ScopeKey         string               `gorm:"uniqueIndex;not null" json:"-"`
	PeriodID         string               `gorm:"size:64;index" json:"periodId"`
	DepartmentID     string               `gorm:"size:64" json:"departmentId"`
	Playbook         string               `json:"playbook"`
	Demo             bool                 `json:"demo"`
	Status           string               `gorm:"index" json:"status"`
	Version          int                  `json:"version"`
	Title            string               `json:"title"`
	Rationale        string               `gorm:"type:text" json:"rationale"`
	MissingData      []string             `gorm:"serializer:json" json:"missingData"`
	Evidence         []AutomationEvidence `gorm:"serializer:json" json:"evidence"`
	Action           string               `json:"action"`
	Draft            string               `gorm:"type:text" json:"draft"`
	TargetSite       string               `json:"targetSite"`
	TargetProject    string               `json:"targetProject"`
	TargetIssueType  string               `json:"targetIssueType"`
	SettingsRevision int                  `json:"settingsRevision"`
	ReviewedBy       string               `json:"reviewedBy,omitempty"`
	ReviewedAt       *time.Time           `json:"reviewedAt,omitempty"`
	CreatedAt        time.Time            `json:"createdAt"`
	UpdatedAt        time.Time            `json:"updatedAt"`
	ResultURL        string               `json:"resultUrl,omitempty"`
	ResultNote       string               `json:"resultNote,omitempty"`
}

// Events are immutable snapshots of human edits, decisions and execution outcomes.
type AutomationEvent struct {
	ID         string             `gorm:"primaryKey;size:64" json:"id"`
	OrgID      string             `gorm:"index;size:64" json:"-"`
	ProposalID string             `gorm:"index;size:64" json:"proposalId"`
	ActorID    string             `json:"actorId"`
	Event      string             `json:"event"`
	Version    int                `json:"version"`
	Snapshot   AutomationProposal `gorm:"serializer:json" json:"snapshot"`
	CreatedAt  time.Time          `json:"createdAt"`
}
