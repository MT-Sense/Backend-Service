package handlers

import "testing"

func TestParsePeriodPagination(t *testing.T) {
	for _, item := range []struct {
		page, size         string
		valid              bool
		wantPage, wantSize int
	}{
		{"", "", true, 1, 10}, {"2", "5", true, 2, 5}, {"1", "100", true, 1, 100},
		{"0", "10", false, 0, 0}, {"-1", "10", false, 0, 0}, {"abc", "10", false, 0, 0},
		{"1", "0", false, 0, 0}, {"1", "101", false, 0, 0}, {"1", "5.5", false, 0, 0},
		{"99999999999999999999999999999", "10", false, 0, 0},
	} {
		page, size, err := parsePagination(item.page, item.size)
		if (err == nil) != item.valid || (item.valid && (page != item.wantPage || size != item.wantSize)) {
			t.Errorf("%q/%q: got %d/%d %v", item.page, item.size, page, size, err)
		}
	}
}
