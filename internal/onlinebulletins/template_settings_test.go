package onlinebulletins

import (
	"strings"
	"testing"
)

func TestTemplateTextIsBoundedPlainSingleLineCopy(t *testing.T) {
	valid := TemplateText{VisionMission: "遍地華人興起、福音傳到地極", VisionFellowship: "共同生活、愛與成全、恩膏傳承", VisionCommitment: "宣教主導、靈恩神學、團隊事奉、門徒訓練"}
	if got, ok := NormalizeTemplateText(valid); !ok || got != valid {
		t.Fatal("valid church values rejected", got, ok)
	}
	for _, value := range []string{"", " ", "第一行\n第二行", "文本\r", "文本\t", "文字\u202e", "文字\u2028", "文字\u2029", strings.Repeat("文", 65), string([]byte{0xff})} {
		input := valid
		input.VisionMission = value
		if _, ok := NormalizeTemplateText(input); ok {
			t.Fatalf("accepted unsafe or oversized value %q", value)
		}
	}
	valid.VisionMission = "  遍地華人興起、福音傳到地極  "
	got, ok := NormalizeTemplateText(valid)
	if !ok || strings.HasPrefix(got.VisionMission, " ") {
		t.Fatal("surrounding spaces not normalized")
	}
	for _, field := range []string{"fellowship", "commitment"} {
		input := valid
		if field == "fellowship" {
			input.VisionFellowship = ""
		} else {
			input.VisionCommitment = ""
		}
		if _, ok := NormalizeTemplateText(input); ok {
			t.Fatal("missing field accepted", field)
		}
	}
}
