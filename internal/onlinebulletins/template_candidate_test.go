package onlinebulletins

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func candidateGeometry() GeometryDocument {
	source := GeometryDocument{SourceSHA256: strings.Repeat("a", 64), PopplerVersion: "25.03.0", Pages: make([]GeometryPage, 4)}
	for index := range source.Pages {
		source.Pages[index] = GeometryPage{Number: index + 1, Width: templateV1Width, Height: templateV1Height}
	}
	add := func(page int, text string, x, y, width float64) {
		source.Pages[page-1].Texts = append(source.Pages[page-1].Texts, GeometryText{Text: text, FontFamily: "MingLiU", FontSize: 12, Box: Box{X: x, Y: y, Width: width, Height: .015}})
	}
	add(1, "主題", .3, .20, .3)
	add(1, "副標", .3, .23, .3)
	add(1, "Sep.20.2026", .1, .17, .2)
	add(1, "第1739期", .4, .17, .2)
	for _, label := range []struct {
		text string
		y    float64
	}{{"Welcome", .28}, {"Worship", .31}, {"Work", .34}, {"Word", .42}} {
		add(1, label.text+"：", .1, label.y, .1)
	}
	add(1, "KEEP_welcome。", .22, .28, .5)
	for index, marker := range []string{"①", "②", "③"} {
		add(1, marker, .22+float64(index)*.2, .31, .025)
		add(1, fmt.Sprintf("KEEP_song%d", index+1), .25+float64(index)*.2, .31, .14)
	}
	for index := 0; index < 3; index++ {
		add(1, fmt.Sprintf("%d.KEEP_work。", index+1), .22, .34+float64(index)*.024, .5)
	}
	for index := 0; index < 6; index++ {
		add(1, fmt.Sprintf("%d.KEEP_question？", index+1), .08, .445+float64(index)*.027, .7)
	}
	add(1, "本週金句", .08, .8, .09)
	add(1, "KEEP_verse。", .20, .784, .6)
	add(1, "教會地址：測試地址", .06, .9, .3)
	add(2, "主題", .115, .33, .3)
	add(2, "KEEP_body。", .155, .36, .6)
	add(3, "小組詩歌", .08, .12, .3)
	add(3, "1.KEEP_hymn(新歌)", .08, .20, .3)
	add(3, "KEEP_lyric。", .08, .225, .3)
	add(4, "KEEP_summary。", .2, .05, .7)
	add(4, "家教會公佈欄", .3, .23, .3)
	add(4, "1.KEEP_announcement。", .06, .27, .7)
	add(4, "得勝與代求", .04, .47, .15)
	add(4, "1.KEEP_prayer。", .06, .49, .7)
	add(4, "主日敬拜服事表", .2, 470/templateV1Height, .3)
	add(4, "DROP_roster", .2, 495/templateV1Height, .5)
	add(4, "DROP_attendance", .2, 570/templateV1Height, .5)
	add(4, "DROP_offerings", .2, 620/templateV1Height, .5)
	return source
}

func candidateIssue() CanonicalIssue {
	return CanonicalIssue{IssueID: "b08a80e8-6388-4166-9a2a-23d336f74b7a", Series: "general", ContentLocale: "zh-Hant", Metadata: CanonicalMetadata{Title: "主題", Subtitle: "副標", IssueNumber: 1739, Date: "2026-09-20"}}
}

func TestCurrentFirstBodyRetainsSourceBandAndDivider(t *testing.T) {
	source := candidateGeometry()
	canonical := candidateIssue()
	canonical.Metadata.Title, canonical.Metadata.Subtitle = "永恆的命定和呼召", "～我們起來建造吧!"
	source.Pages[1].Images = []GeometryImage{
		{Box: Box{X: .1, Y: 71 / templateV1Height, Width: .6, Height: 52 / templateV1Height}},
		{Box: Box{X: .4, Y: 181 / templateV1Height, Width: .5, Height: 28 / templateV1Height}},
	}
	manifest := validDocument(4).LayoutManifest
	manifest.RendererVersion = "v8"
	body, err := bodyComponents(source.Pages[1:2])
	if err != nil {
		t.Fatal(err)
	}
	candidate, _, err := ApplyTemplateV1(source, canonical, manifest)
	if err != nil || candidate.Document == nil {
		t.Fatal("current candidate missing", err)
	}
	layout := candidate.Document.LayoutManifest.Pages[1]
	if layout.Slots[0].Box.Y != body.slots[0].Box.Y {
		t.Fatal("removed production band pulled source text upward", layout.Slots[0].Box.Y)
	}
	rule := false
	for _, slot := range layout.FixedSlots {
		rule = rule || slot.Element == "topRule"
	}
	if !rule {
		t.Fatal("first-body divider removed")
	}
	for _, component := range candidate.Document.Components {
		if component.BodySection != nil && component.BodySection.Header != nil {
			t.Fatal("omitted production metadata returned")
		}
	}
	legacy := canonical
	legacy.Metadata.IssueNumber = 1738
	previous, _, err := ApplyTemplateV1(source, legacy, manifest)
	if err != nil || previous.Document == nil || previous.Document.LayoutManifest.Pages[1].Slots[0].Box.Y >= body.slots[0].Box.Y {
		t.Fatal("legacy geometry behavior changed", err)
	}
}

func TestCandidateKeepsMixedArticleAndHymnsOnOriginalPage(t *testing.T) {
	source := candidateGeometry()
	source.Pages[2].Texts = append(source.Pages[2].Texts, GeometryText{Text: "KEEP_article_tail。", FontFamily: "MingLiU", FontSize: 12, Box: Box{X: .155, Y: .08, Width: .6, Height: .015}})
	candidate, _, err := ApplyTemplateV1(source, candidateIssue(), validDocument(4).LayoutManifest)
	if err != nil || candidate.Document == nil {
		t.Fatalf("mixed page candidate: %v", err)
	}
	document := candidate.Document
	encoded, _ := json.Marshal(document.Components)
	if strings.Count(string(encoded), "KEEP_article_tail") != 1 || strings.Count(string(encoded), "KEEP_lyric") != 1 {
		t.Fatal("mixed page content lost or duplicated")
	}
	body, hymns := false, false
	for _, slot := range document.LayoutManifest.Pages[2].Slots {
		body = body || strings.HasPrefix(slot.ComponentID, "body-section-")
		hymns = hymns || slot.ComponentID == "hymns"
	}
	if !body || !hymns || len(document.Pages) != 4 {
		t.Fatal("article tail or lyrics moved from original page")
	}
}

func TestCandidateAcceptsLetterInnerPagesWithoutResizing(t *testing.T) {
	source := candidateGeometry()
	for i := 1; i < len(source.Pages); i++ {
		source.Pages[i].Width, source.Pages[i].Height = 612, 792
	}
	candidate, _, err := ApplyTemplateV1(source, candidateIssue(), validDocument(4).LayoutManifest)
	if err != nil || candidate.Document == nil {
		t.Fatalf("mixed paper candidate: %v", err)
	}
	if candidate.Document.Pages[0].Width != templateV1Width || candidate.Document.Pages[1].Width != 612 || candidate.Document.Pages[1].Height != 792 {
		t.Fatal("source paper sizes changed")
	}
	encoded, _ := json.Marshal(candidate.Document.Components)
	if !strings.Contains(string(encoded), "KEEP_body") || !strings.Contains(string(encoded), "KEEP_lyric") {
		t.Fatal("letter body or lyrics lost")
	}
	source.Pages[1].Width = 700
	if candidate, _, err = ApplyTemplateV1(source, candidateIssue(), validDocument(4).LayoutManifest); err == nil && candidate.Document != nil {
		t.Fatal("unrecognized paper accepted")
	}
}

func TestApplyTemplateV1SupportsV4WithoutRestoringExcludedCoverDetails(t *testing.T) {
	manifest := validDocument(4).LayoutManifest
	manifest.RendererVersion = "v4"
	candidate, _, err := ApplyTemplateV1(candidateGeometry(), candidateIssue(), manifest)
	if err != nil || candidate.Document == nil {
		t.Fatalf("v4 candidate missing: %v", err)
	}
	if candidate.Document.LayoutManifest.RendererVersion != "v4" {
		t.Fatal("renderer identity changed")
	}
	for _, slot := range candidate.Document.LayoutManifest.Pages[0].FixedSlots {
		if slot.Element == "contact" || slot.Element == "scanHint" || strings.Contains(slot.Element, "QR") {
			t.Fatal("excluded cover footer restored")
		}
	}
}

func TestApplyTemplateV1ComposesCandidateOnlyAfterPrivacyFiltering(t *testing.T) {
	source := candidateGeometry()
	manifest := validDocument(4).LayoutManifest
	manifest.ContentHash, manifest.LayoutValidationHash = strings.Repeat("c", 64), strings.Repeat("d", 64)
	candidate, issues, err := ApplyTemplateV1(source, candidateIssue(), manifest)
	if err != nil || candidate.Document == nil {
		t.Fatalf("candidate composition: %v", err)
	}
	document := *candidate.Document
	if _, err := ValidateDocument(document); err != nil {
		t.Fatal(err)
	}
	if len(document.Pages) != 4 || len(document.Components) != 6 || document.Pages[1].Width != templateV1Width || document.LayoutManifest.ContentHash != "" || document.LayoutManifest.LayoutValidationHash != "" {
		t.Fatal("source geometry, required components or stale-proof invalidation lost")
	}
	for page, layout := range document.LayoutManifest.Pages {
		for _, slot := range layout.Slots {
			if slot.sourcePage != page+1 {
				t.Fatal("slot assigned to the wrong source page")
			}
		}
	}
	data, err := json.Marshal(candidate)
	if err != nil || strings.Contains(string(data), "DROP_") || strings.Contains(string(data), "服事表") {
		t.Fatal("private source escaped candidate or classification evidence")
	}
	for _, keep := range []string{"KEEP_welcome", "KEEP_body", "KEEP_lyric", "KEEP_summary", "KEEP_announcement", "KEEP_prayer"} {
		if !strings.Contains(string(data), keep) {
			t.Fatal("retained source was lost")
		}
	}
	if len(issues) == 0 || !strings.Contains(source.Pages[3].Texts[len(source.Pages[3].Texts)-1].Text, "DROP_") {
		t.Fatal("blocking review or caller-owned source was lost")
	}
	second, secondIssues, err := ApplyTemplateV1(source, candidateIssue(), manifest)
	other, _ := json.Marshal(second)
	if err != nil || string(data) != string(other) || len(issues) != len(secondIssues) {
		t.Fatal("candidate mapping is not deterministic")
	}
}

func TestApplyTemplateV1UnsupportedBackCannotFabricateContent(t *testing.T) {
	source := candidateGeometry()
	source.Pages[3].Texts = source.Pages[3].Texts[len(source.Pages[3].Texts)-1:]
	candidate, issues, err := ApplyTemplateV1(source, candidateIssue(), validDocument(4).LayoutManifest)
	data, _ := json.Marshal(candidate)
	if err != nil || candidate.Document != nil || len(issues) == 0 || strings.Contains(string(data), "DROP_") {
		t.Fatal("unknown source was fabricated or private back leaked")
	}
}

func TestBackDecorationIssueRetainsOriginalPage(t *testing.T) {
	_, issues := backFrameSlots(GeometryPage{Number: 16})
	if len(issues) != 1 || issues[0].SourcePage != 16 {
		t.Fatalf("unlocatable back decoration: %+v", issues)
	}
}

func TestMissingPrintedPageIssuesRetainEachOriginalPage(t *testing.T) {
	_, issues, err := ApplyTemplateV1(candidateGeometry(), candidateIssue(), validDocument(4).LayoutManifest)
	if err != nil {
		t.Fatal(err)
	}
	pages := map[int]bool{}
	for _, issue := range issues {
		if issue.Code == "missing_source_page_number" {
			pages[issue.SourcePage] = true
		}
	}
	if len(pages) != 2 || !pages[2] || !pages[3] {
		t.Fatalf("unlocatable missing page numbers: %+v", pages)
	}
}

func TestApplyTemplateV1UnknownImageRemainsBlockingWithoutSourcePixels(t *testing.T) {
	source := candidateGeometry()
	source.Pages[0].Images = []GeometryImage{{Box: Box{X: .1, Y: .1, Width: .1, Height: .1}, Width: 100, Height: 100, SHA256: strings.Repeat("e", 64)}}
	_, issues, err := ApplyTemplateV1(source, candidateIssue(), validDocument(4).LayoutManifest)
	if err != nil {
		t.Fatal(err)
	}
	for _, issue := range issues {
		if issue.Code == "unknown_source_image" && issue.Blocking {
			if issue.SourcePage != 1 {
				t.Fatalf("missing original image page: %+v", issue)
			}
			return
		}
	}
	t.Fatal("unknown raster was silently accepted")
}

func TestApplyTemplateV1RecognizesGraphicsOnlyByReviewedIdentityPageAndGeometry(t *testing.T) {
	for _, check := range []struct {
		name    string
		page    int
		x       float64
		unknown bool
	}{
		{"verified-cover-logo", 0, .128166, false}, {"same-hash-wrong-position", 0, .20, true}, {"same-hash-body-page", 1, .128166, true},
	} {
		t.Run(check.name, func(t *testing.T) {
			source := candidateGeometry()
			source.Pages[check.page].Images = []GeometryImage{{SHA256: "2b1f2ed34df0edd7f3217a97312ce42190c923742ac7eb5ab56a7afb3b582161", Box: Box{X: check.x, Y: .035633, Width: .157226, Height: .111174}}}
			_, issues, err := ApplyTemplateV1(source, candidateIssue(), validDocument(4).LayoutManifest)
			if err != nil {
				t.Fatal(err)
			}
			unknown := false
			for _, issue := range issues {
				if issue.Code == "unknown_source_image" && issue.Blocking {
					unknown = true
				}
			}
			if unknown != check.unknown {
				t.Fatalf("unknown image=%t want=%t", unknown, check.unknown)
			}
		})
	}
}

func TestApplyTemplateV1RejectsUntrustedIdentityAndManifestBeforeReturningEvidence(t *testing.T) {
	issue := candidateIssue()
	issue.ContentLocale = "en"
	if candidate, _, err := ApplyTemplateV1(candidateGeometry(), issue, validDocument(4).LayoutManifest); !errors.Is(err, ErrUnsupportedSource) || len(candidate.Evidence.Pages) != 0 {
		t.Fatal("unsupported edition returned extraction evidence")
	}
	source := candidateGeometry()
	source.PopplerVersion = "unverified"
	if _, _, err := ApplyTemplateV1(source, candidateIssue(), validDocument(4).LayoutManifest); !errors.Is(err, ErrUnsupportedSource) {
		t.Fatal("unverified parser provenance accepted")
	}
	manifest := validDocument(4).LayoutManifest
	manifest.Assets[0].URL = "https://foreign.example/font.woff2"
	if candidate, _, err := ApplyTemplateV1(candidateGeometry(), candidateIssue(), manifest); !errors.Is(err, ErrTemplateAssets) || len(candidate.Evidence.Pages) != 0 {
		t.Fatal("document-selected external asset was accepted")
	}
}
