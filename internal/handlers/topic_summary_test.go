package handlers

import (
	"github.com/mt-sense/backend-service/internal/aiservice"
	"testing"
)

func TestTopicThemeEvidenceValidation(t *testing.T) {
	comments := []aiservice.TopicComment{}
	for _, ref := range []string{"c1", "c2", "c3", "c4", "c5"} {
		comments = append(comments, aiservice.TopicComment{Ref: ref, Text: "Shared workload planning needs improvement"})
	}
	valid := func() *aiservice.TopicSummaryResult {
		theme := aiservice.TopicTheme{TitleTH: "การวางแผน", TitleEN: "Planning", SummaryTH: "ปรับปรุงการวางแผนงาน", SummaryEN: "Improve planning"}
		for _, c := range comments {
			theme.Evidence = append(theme.Evidence, aiservice.TopicCitation{Ref: c.Ref, Excerpt: "Shared workload planning"})
		}
		return &aiservice.TopicSummaryResult{Themes: []aiservice.TopicTheme{theme}}
	}
	result, err := validateTopicThemes(comments, valid())
	if err != nil || len(result) != 1 || result[0].SupportingCount != 5 || len(result[0].Excerpts) != 1 {
		t.Fatalf("valid evidence rejected: %+v %v", result, err)
	}
	for _, mutation := range []string{"unknown", "duplicate", "fabricated", "four", "too-many-themes", "missing-title", "missing-result"} {
		t.Run(mutation, func(t *testing.T) {
			r := valid()
			switch mutation {
			case "unknown":
				r.Themes[0].Evidence[0].Ref = "other-scope-ref"
			case "duplicate":
				r.Themes[0].Evidence[0].Ref = "c2"
			case "fabricated":
				r.Themes[0].Evidence[0].Excerpt = "This never appeared in the source"
			case "four":
				r.Themes[0].Evidence = r.Themes[0].Evidence[:4]
			case "too-many-themes":
				r.Themes = []aiservice.TopicTheme{r.Themes[0], r.Themes[0], r.Themes[0], r.Themes[0]}
			case "missing-title":
				r.Themes[0].TitleTH = ""
			case "missing-result":
				r = nil
			}
			if _, err := validateTopicThemes(comments, r); err == nil {
				t.Fatal("unsupported summary accepted")
			}
		})
	}
	empty, err := validateTopicThemes(comments, &aiservice.TopicSummaryResult{Themes: []aiservice.TopicTheme{}})
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatal("empty result should be an explicit empty array")
	}
}
