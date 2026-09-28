package emergingtopics

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"unicode"

	"github.com/mt-sense/backend-service/internal/aiservice"
	"github.com/mt-sense/backend-service/internal/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Save links AI-discovered topics to an anonymous response. Topic identity is scoped to the
// organization and derived from a normalized label so repeated batches reuse the same row.
func Save(tx *gorm.DB, orgID, responseID string, topics []aiservice.EmergingTopic) error {
	seen := make(map[string]bool)
	for _, candidate := range topics {
		normalized := normalize(candidate.LabelTH)
		if normalized == "" || seen[normalized] {
			continue
		}
		seen[normalized] = true

		topicID := stableID(orgID, normalized)
		labelTH := strings.TrimSpace(candidate.LabelTH)
		labelEN := strings.TrimSpace(candidate.LabelEN)
		if labelEN == "" {
			labelEN = labelTH
		}
		topic := models.EmergingTopic{
			ID:              topicID,
			OrgID:           orgID,
			NormalizedLabel: normalized,
			Label:           models.Localized{TH: labelTH, EN: labelEN},
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&topic).Error; err != nil {
			return err
		}

		link := models.ResponseEmergingTopic{ResponseID: responseID, EmergingTopicID: topicID}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&link).Error; err != nil {
			return err
		}
	}

	return nil
}

func normalize(label string) string {
	var builder strings.Builder
	spacePending := false
	for _, char := range strings.ToLower(strings.TrimSpace(label)) {
		switch {
		case unicode.IsLetter(char), unicode.IsDigit(char):
			if spacePending && builder.Len() > 0 {
				builder.WriteByte(' ')
			}
			builder.WriteRune(char)
			spacePending = false
		case unicode.IsSpace(char), unicode.IsPunct(char), unicode.IsSymbol(char):
			spacePending = true
		}
	}

	return builder.String()
}

func stableID(orgID, normalized string) string {
	digest := sha256.Sum256([]byte(orgID + "\x00" + normalized))

	return "emerging_" + hex.EncodeToString(digest[:12])
}
