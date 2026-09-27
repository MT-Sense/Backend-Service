package knowledgebase

import (
	"testing"

	"github.com/mt-sense/backend-service/internal/aiservice"
)

func TestSourceHashIsDeterministicAndTracksEvidence(t *testing.T) {
	request := aiservice.KnowledgeCompileRequest{
		Source: aiservice.KnowledgeSource{
			PeriodID:            "period-sep",
			PeriodLabel:         "2026-09",
			TotalResponses:      20,
			AverageSatisfaction: 3.8,
			Topics: []aiservice.KnowledgeTopic{{
				ID:                  "work",
				Label:               "การทำงานและภาระงาน",
				AverageSatisfaction: 3.2,
				MentionCount:        12,
			}},
		},
		PreviousArticles: []aiservice.PreviousKnowledgeArticle{{
			PeriodID:    "period-aug",
			PeriodLabel: "2026-08",
			TitleTH:     "สรุปเดือนสิงหาคม",
			SummaryTH:   "คะแนนเฉลี่ย 3.6/5",
		}},
	}

	first, err := sourceHash(request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := sourceHash(request)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("same evidence must produce same hash: %q != %q", first, second)
	}

	request.Source.TotalResponses++
	changed, err := sourceHash(request)
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Fatal("changing aggregate evidence must invalidate the knowledge source hash")
	}
}

func TestPeriodLabelUsesStableWikiSlug(t *testing.T) {
	if got := periodLabel(2026, 9); got != "2026-09" {
		t.Fatalf("unexpected period label: %q", got)
	}
}
