package analytics

import "testing"

func TestSentimentPercentages(t *testing.T) {
	regression := SentimentPercentages(0, 1, 7)
	if regression.Positive != 0 || regression.Neutral != 13 || regression.Negative != 87 {
		t.Fatalf("rounding regression: %+v", regression)
	}
	for positive := int64(0); positive <= 30; positive++ {
		for neutral := int64(0); neutral <= 30; neutral++ {
			for negative := int64(0); negative <= 30; negative++ {
				got := SentimentPercentages(positive, neutral, negative)
				values := []int{got.Positive, got.Neutral, got.Negative}
				sum := 0
				for i, value := range values {
					if value < 0 || value > 100 {
						t.Fatalf("out of range: %v", got)
					}
					counts := []int64{positive, neutral, negative}
					if counts[i] == 0 && value != 0 {
						t.Fatalf("empty bucket gained share: %v", got)
					}
					sum += value
				}
				want := 100
				if positive+neutral+negative == 0 {
					want = 0
				}
				if sum != want {
					t.Fatalf("sum %d, want %d: %+v", sum, want, got)
				}
			}
		}
	}
}
