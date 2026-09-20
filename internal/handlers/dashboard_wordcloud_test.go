package handlers

import "testing"

func TestWordCloudTermsCountsDistinctResponsesAndSuppressesSmallGroups(t *testing.T) {
	responses := [][]string{
		{"ระบบ", "ระบบ", "ทีม"},
		{"ระบบ", "ทีม"},
		{"ระบบ", "ทีม"},
		{"ระบบ", "ทีม"},
		{"ระบบ"},
	}
	terms := wordCloudTerms(responses)
	if len(terms) != 1 {
		t.Fatalf("expected only one visible term, got %v", terms)
	}
	if terms[0].Term.TH != "ระบบ" || terms[0].Frequency != 5 {
		t.Fatalf("unexpected term: %+v", terms[0])
	}
}
