package onlinebulletins

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func v9CandidateSource() (GeometryDocument, CanonicalIssue, LayoutManifest, TemplateSettings) {
	source, canonical := candidateGeometry(), candidateIssue()
	canonical.Metadata.Title, canonical.Metadata.Subtitle = currentBodyArtwork[0].title, currentBodyArtwork[0].subtitle
	source.Pages[0].Texts[0].Text, source.Pages[0].Texts[1].Text = canonical.Metadata.Title, canonical.Metadata.Subtitle
	source.Pages[0].Images = historicalNativeGeometry().Pages[0].Images
	source.Pages[1].Images = append([]GeometryImage(nil), currentBodyArtwork[0].images...)
	for i, text := range []string{"異象使命：新的使命內容", "團契行動：新的團契內容", "堅持理想：新的理想內容"} {
		source.Pages[0].Texts = append(source.Pages[0].Texts, GeometryText{Text: text, FontFamily: "MingLiU", FontSize: 9, Box: Box{X: .44 + float64(i)*.03, Y: .156 + float64(i)*.019, Width: .45, Height: .011}})
	}
	manifest := validDocument(4).LayoutManifest
	manifest.RendererVersion = "v9"
	snapshot := TemplateSettings{Version: 7, TemplateText: TemplateText{VisionMission: "遍地華人興起、福音傳到地極", VisionFellowship: "共同生活、愛與成全、恩膏傳承", VisionCommitment: "宣教主導、靈恩神學、團隊事奉、門徒訓練"}}
	return source, canonical, manifest, snapshot
}

func TestV9CandidateUsesCopiedSnapshotNotSourceValues(t *testing.T) {
	source, canonical, manifest, snapshot := v9CandidateSource()
	before, _ := json.Marshal(source)
	candidate, issues, err := ApplyTemplateV9(source, canonical, manifest, snapshot)
	if err != nil || candidate.Document == nil {
		t.Fatal("V9 candidate unavailable", err)
	}
	document := candidate.Document
	if document.TemplateSnapshot == nil || *document.TemplateSnapshot != snapshot || document.LayoutManifest.RendererVersion != "v9" {
		t.Fatal("queued snapshot not attached before validation")
	}
	for _, issue := range issues {
		if issue.Code == "fixed_template_text_variant" {
			t.Fatal("source value changes still require review")
		}
	}
	for _, element := range []string{"visionMission", "visionFellowship", "visionCommitment"} {
		count := 0
		for _, slot := range document.LayoutManifest.Pages[0].FixedSlots {
			if slot.Element == element {
				count++
			}
		}
		if count != 1 {
			t.Fatal("snapshot row missing or duplicated", element)
		}
	}
	after, _ := json.Marshal(source)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("source evidence mutated")
	}
	encoded, _ := json.Marshal(document)
	if strings.Contains(string(encoded), "新的使命內容") {
		t.Fatal("source fixed text escaped into saved content")
	}
	snapshot.VisionMission = "後續設定"
	if document.TemplateSnapshot.VisionMission == snapshot.VisionMission {
		t.Fatal("snapshot alias changed historical candidate")
	}
}

func TestV9CandidateRejectsMissingInvalidOrHistoricalSnapshotProfile(t *testing.T) {
	for _, name := range []string{"zero", "unsafe", "empty", "spaces", "control", "legacy", "earlier"} {
		t.Run(name, func(t *testing.T) {
			source, canonical, manifest, snapshot := v9CandidateSource()
			switch name {
			case "zero":
				snapshot.Version = 0
			case "unsafe":
				snapshot.Version = 9007199254740992
			case "empty":
				snapshot.VisionMission = ""
			case "spaces":
				snapshot.VisionMission = " 前後空白 "
			case "control":
				snapshot.VisionMission = "不同\u202e文字"
			case "legacy":
				manifest.RendererVersion = "v8"
			case "earlier":
				canonical.Metadata.IssueNumber = 1738
			}
			candidate, _, err := ApplyTemplateV9(source, canonical, manifest, snapshot)
			if err == nil || candidate.Document != nil {
				t.Fatal("invalid V9 snapshot/profile accepted")
			}
		})
	}
}

func TestV9DoesNotEnterLegacyCandidateWithoutSnapshot(t *testing.T) {
	source, canonical, manifest, _ := v9CandidateSource()
	if _, _, err := ApplyTemplateV1(source, canonical, manifest); err == nil {
		t.Fatal("legacy entry inferred mutable settings")
	}
}

func TestV9SnapshotRowsKeepMalformedLabelsLocated(t *testing.T) {
	for _, name := range []string{"missing", "moved", "duplicate", "duplicate_moved", "duplicate_left", "unknown", "empty", "off_edge"} {
		t.Run(name, func(t *testing.T) {
			source, _, _, _ := v9CandidateSource()
			page := source.Pages[0]
			index := len(page.Texts) - 3
			switch name {
			case "missing":
				page.Texts = append(page.Texts[:index], page.Texts[index+1:]...)
			case "moved":
				page.Texts[index].Box.Y += .04
			case "duplicate":
				page.Texts = append(page.Texts, page.Texts[index])
			case "duplicate_moved", "duplicate_left":
				duplicate := page.Texts[index]
				if name == "duplicate_moved" {
					duplicate.Box.Y = .135
				} else {
					duplicate.Box.X = .1
				}
				page.Texts = append(page.Texts, duplicate)
			case "unknown":
				page.Texts[index].Text = "另一個未知標籤：內容"
			case "empty":
				page.Texts[index].Text = "異象使命："
			case "off_edge":
				page.Texts[index].Box.X = .96
				page.Texts[index].Box.Width = .03
			}
			slots, issues, err := coverSnapshotChurchSlots(page)
			if err != nil {
				t.Fatal(err)
			}
			for _, slot := range slots {
				if slot.Element == "visionMission" {
					t.Fatal("unverified row silently substituted")
				}
			}
			located := false
			for _, issue := range issues {
				located = located || issue.Code == "fixed_template_source_mismatch" && issue.ComponentID == "cover" && issue.SourcePage == 1 && issue.Blocking
			}
			if !located {
				t.Fatal("malformed row lost its located review issue")
			}
		})
	}
}
