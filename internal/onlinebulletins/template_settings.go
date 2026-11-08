package onlinebulletins

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// TemplateText preserves the three values used by a conversion snapshot.
type TemplateText struct {
	VisionMission    string `json:"visionMission"`
	VisionFellowship string `json:"visionFellowship"`
	VisionCommitment string `json:"visionCommitment"`
}

type TemplateSettings struct {
	Version int64 `json:"version"`
	TemplateText
}

type TemplateSettingsRevision struct {
	TemplateSettings
	CreatedBy string `json:"createdBy"`
	CreatedAt string `json:"createdAt"`
}

func NormalizeTemplateText(input TemplateText) (TemplateText, bool) {
	for _, value := range []*string{&input.VisionMission, &input.VisionFellowship, &input.VisionCommitment} {
		if !utf8.ValidString(*value) {
			return TemplateText{}, false
		}
		for _, char := range *value {
			if unicode.IsControl(char) || unicode.Is(unicode.Cf, char) || char == '\u2028' || char == '\u2029' {
				return TemplateText{}, false
			}
		}
		*value = strings.TrimSpace(*value)
		if utf8.RuneCountInString(*value) < 1 || utf8.RuneCountInString(*value) > 64 {
			return TemplateText{}, false
		}
	}
	return input, true
}

// ConversionTemplateSettings is the reviewed, backend-owned cover rule. Jobs
// capture its value; changes require an extractor version bump and never rewrite
// queued jobs or historical documents. Date-based versions distinguish this rule
// from the former settings editor's sequence.
func ConversionTemplateSettings() TemplateSettings {
	return TemplateSettings{Version: 20261016, TemplateText: TemplateText{
		VisionMission:    "遍地華人興起、福音傳到地極",
		VisionFellowship: "共同生活、愛與成全、恩膏傳承",
		VisionCommitment: "宣教主導、靈恩神學、團隊事奉、門徒訓練",
	}}
}
