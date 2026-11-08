package onlinebulletins

import (
	"encoding/json"
	"testing"
)

func TestLearningMatchesParserProducedSourceRuns(t *testing.T) {
	for _, name := range []string{"title-runs", "speaker-runs", "ambiguous-speaker"} {
		t.Run(name, func(t *testing.T) {
			base, local, evidence := learningDocuments(t)
			page := &evidence.Pages[0]
			for index := range page.Texts {
				page.Texts[index].Box.Y = .4
			}
			page.Frames[0].Box.Y = .398
			switch name {
			case "title-runs":
				page.Texts[0].Text, page.Texts[0].Box.Width = "信", .02
				page.Texts = append(page.Texts, GeometryText{Text: "息", FontFamily: "DFXingShu", FontSize: 16, Box: Box{X: .12, Y: .4, Width: .03, Height: .02}})
			case "speaker-runs":
				page.Texts[2].Text, page.Texts[2].Box.Width = "講", .03
				page.Texts = append(page.Texts, GeometryText{Text: "員", FontFamily: "PMingLiU", FontSize: 16, Box: Box{X: .21, Y: .4, Width: .04, Height: .02}})
			case "ambiguous-speaker":
				page.Texts[2].Text = "講員～另人"
			}
			page.Texts = append(page.Texts, GeometryText{Text: "正文。", FontFamily: "MingLiU", FontSize: 14, Box: Box{X: .14, Y: .44, Width: .3, Height: .02}})
			mapping, err := bodyComponents(evidence.Pages)
			if err != nil || len(mapping.components) != 1 {
				t.Fatal("real parser rejected source", err)
			}
			base.Components[1].BodySection = mapping.components[0].BodySection
			raw, _ := json.Marshal(base)
			if err = json.Unmarshal(raw, &local); err != nil {
				t.Fatal(err)
			}
			local.Components[1].BodySection.Kind = "sermon"
			got := CollectBodyKindCorrections(base, local, evidence)
			if name == "ambiguous-speaker" {
				if len(got) != 0 {
					t.Fatal("ambiguous speaker learned", got)
				}
				return
			}
			if len(got) != 1 || got[0].Title != "信息" || got[0].Speaker != "講員" {
				t.Fatal("explicit source runs lost", got)
			}
			applied, err := ApplyBodyKindRules(&base, evidence, []LearningRuleInput{{Signature: got[0].Signature, Version: 2, Kind: "sermon"}})
			if err != nil || len(applied) != 1 || base.Components[1].BodySection.Kind != "sermon" {
				t.Fatal("multi-run rule cannot be reused", applied, err)
			}
		})
	}
}
