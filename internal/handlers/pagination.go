package handlers

import (
	"fmt"
	"strconv"
)

func parsePagination(pageText, sizeText string) (int, int, error) {
	page, size := 1, 10
	var err error
	if pageText != "" {
		page, err = strconv.Atoi(pageText)
		if err != nil || page < 1 {
			return 0, 0, fmt.Errorf("page must be a positive integer")
		}
	}
	if sizeText != "" {
		size, err = strconv.Atoi(sizeText)
		if err != nil || size < 1 || size > 100 {
			return 0, 0, fmt.Errorf("pageSize must be between 1 and 100")
		}
	}
	return page, size, nil
}
