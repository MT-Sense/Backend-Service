package dto

// LatestPeriod identifies the next round independently of the table's current page.
type LatestPeriod struct {
	Month int `json:"month"`
	Year  int `json:"year"`
}

type SurveyPeriodPage struct {
	Items        []SurveyPeriod `json:"items"`
	Total        int64          `json:"total"`
	Page         int            `json:"page"`
	PageSize     int            `json:"pageSize"`
	LatestPeriod *LatestPeriod  `json:"latestPeriod"`
}
