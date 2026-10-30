package dto

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/mt-sense/backend-service/internal/analytics"
	"github.com/mt-sense/backend-service/internal/models"
)

func TestTopicTrendCalendarGapsAcrossYear(t *testing.T) {
	points := NewTopicTrendWindow([]analytics.TopicTrendPoint{
		{Month: "2026-08", Score: 1},
		{Month: "2026-11", Score: 2.7},
		{Month: "2027-02", Score: 4.1},
	}, &models.SurveyPeriod{Year: 2027, Month: 2})
	if len(points) != 6 || points[0].Month != "2026-09" || points[5].Month != "2027-02" {
		t.Fatalf("wrong calendar window: %+v", points)
	}
	if points[2].Score == nil || *points[2].Score != 2.7 || points[5].Score == nil || *points[5].Score != 4.1 {
		t.Fatal("visible values changed or aliased", points)
	}
	if points[0].Score != nil || points[3].Score != nil || points[4].Score != nil {
		t.Fatal("missing/suppressed months must stay null", points)
	}
	encoded, err := json.Marshal(points)
	if err != nil || !strings.Contains(string(encoded), `"month":"2027-01","score":null`) {
		t.Fatalf("gap lost in JSON: %s %v", encoded, err)
	}
}
