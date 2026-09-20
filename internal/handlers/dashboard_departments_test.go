package handlers

import "testing"

func TestDepartmentScoreStatusAndProjection(t *testing.T) {
	for _, item := range []struct {
		score float64
		want  string
	}{
		{4.0, "good"}, {3.9, "watch"}, {3.0, "watch"}, {2.9, "risk"},
	} {
		if got := departmentScoreStatus(item.score); got != item.want {
			t.Errorf("status for %.1f: got %q, want %q", item.score, got, item.want)
		}
	}
	change, forecast := departmentProjection(4.7, 3.9)
	if change != 0.8 || forecast != 5 {
		t.Fatalf("upward projection: got %.1f and %.1f", change, forecast)
	}
	change, forecast = departmentProjection(1.3, 2.4)
	if change != -1.1 || forecast != 1 {
		t.Fatalf("downward projection: got %.1f and %.1f", change, forecast)
	}
}
