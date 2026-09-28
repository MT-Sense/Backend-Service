package emergingtopics

import "testing"

func TestNormalizeMakesEquivalentLabelsStable(t *testing.T) {
	first := normalize("  คุณภาพเครื่องมือ-ภายใน ")
	second := normalize("คุณภาพเครื่องมือ ภายใน")
	if first != second {
		t.Fatalf("expected equivalent labels to normalize equally: %q != %q", first, second)
	}
	if stableID("org-1", first) != stableID("org-1", second) {
		t.Fatal("expected equivalent labels to share an id")
	}
	if stableID("org-1", first) == stableID("org-2", second) {
		t.Fatal("expected topic ids to be organization scoped")
	}
}
