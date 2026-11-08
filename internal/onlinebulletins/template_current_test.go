package onlinebulletins

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestCurrentCandidateUsesVerifiedArtAndSemanticFramesWithoutBlanketWaiver(t *testing.T) {
	source := candidateGeometry()
	canonical := candidateIssue()
	canonical.Metadata.Title, canonical.Metadata.Subtitle = currentBodyArtwork[0].title, currentBodyArtwork[0].subtitle
	source.Pages[0].Texts[0].Text, source.Pages[0].Texts[1].Text = canonical.Metadata.Title, canonical.Metadata.Subtitle
	source.Pages[0].Images = historicalNativeGeometry().Pages[0].Images
	for i, text := range []string{"異象使命：華人遍地興起、福音傳到地極", "團契行動：共同生活、愛與成全、恩膏傳承", "堅持理想：宣教主導、靈恩神學、團隊事奉、門徒訓練"} {
		source.Pages[0].Texts = append(source.Pages[0].Texts, GeometryText{Text: text, FontFamily: "MingLiU", FontSize: 9, Box: Box{X: .44, Y: .156 + float64(i)*.019, Width: .5, Height: .011}})
	}
	source.Pages[1].Images = append([]GeometryImage(nil), currentBodyArtwork[0].images...)
	manifest := validDocument(4).LayoutManifest
	manifest.RendererVersion = "v8"
	before, _ := json.Marshal(source)
	candidate, issues, err := ApplyTemplateV1(source, canonical, manifest)
	if err != nil || candidate.Document == nil {
		t.Fatal(err)
	}
	for _, issue := range issues {
		if issue.Code == "inferred_body_art_geometry" || issue.Code == "unmapped_back_decoration" || issue.Code == "unknown_source_image" && issue.SourcePage == 2 || issue.Code == "unmapped_fixed_template" && issue.ComponentID == "" {
			t.Fatal("verified fixed profile still needs blanket review", issue)
		}
	}
	if len(issues) == 0 {
		t.Fatal("fixture's missing pastor/sidebar/page-number checks were erased")
	}
	after, _ := json.Marshal(source)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("candidate changed source evidence")
	}
	source.Pages[1].Images[0].Box.Y += .01
	_, moved, err := ApplyTemplateV1(source, canonical, manifest)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, issue := range moved {
		found = found || issue.Code == "unknown_source_image" && issue.SourcePage == 2
	}
	if !found {
		t.Fatal("moved artwork lost its review requirement")
	}
}

func TestCurrentArtworkRequiresCompleteIdentityPlacementAndCanonicalText(t *testing.T) {
	for _, profile := range currentBodyArtwork {
		page := GeometryPage{Number: 2, Width: templateV1Width, Height: templateV1Height, Images: append([]GeometryImage(nil), profile.images...)}
		metadata := CanonicalMetadata{Title: profile.title, Subtitle: profile.subtitle}
		if !verifiedCurrentBodyArtwork(page, metadata) {
			t.Fatal("reviewed original artwork not recognized")
		}
		for _, name := range []string{"page", "paper", "missing", "duplicate", "unknown", "moved", "title", "subtitle"} {
			t.Run(profile.title+"/"+name, func(t *testing.T) {
				p, m := page, metadata
				p.Images = append([]GeometryImage(nil), page.Images...)
				switch name {
				case "page":
					p.Number++
				case "paper":
					p.Width = 612
				case "missing":
					p.Images = p.Images[1:]
				case "duplicate":
					p.Images = append(p.Images, p.Images[0])
				case "unknown":
					p.Images[0].SHA256 = strings.Repeat("e", 64)
				case "moved":
					p.Images[0].Box.Y += .01
				case "title":
					m.Title += "異"
				case "subtitle":
					m.Subtitle += "異"
				}
				if verifiedCurrentBodyArtwork(p, m) {
					t.Fatal("unverified artwork silently accepted")
				}
			})
		}
	}
}

func TestTemplateOwnedHeaderArtworkIsBoundedToCanonicalHeaderBands(t *testing.T) {
	page := GeometryPage{Number: 2, Width: templateV1Width, Height: templateV1Height, Images: append([]GeometryImage(nil), currentBodyArtwork[0].images...)}
	metadata := CanonicalMetadata{Title: "新的命定與呼召、洗革拉戰役", Subtitle: "被聖靈充滿必有的三個看見"}
	slots, _ := bodyCanonicalArtSlots(page, metadata)
	if !templateOwnedHeaderArtwork(page, slots, metadata) {
		t.Fatal("canonical reader header still depends on issue-specific shadow hashes")
	}
	for _, name := range []string{"body-image", "crosses-divider", "missing-title", "missing-subtitle", "missing-slot", "wrong-page"} {
		t.Run(name, func(t *testing.T) {
			p, s := page, slots
			p.Images = append([]GeometryImage(nil), page.Images...)
			switch name {
			case "body-image":
				p.Images = append(p.Images, GeometryImage{Box: Box{X: .1, Y: .5, Width: .3, Height: .1}})
			case "crosses-divider":
				p.Images[1].Box.Height = .2
			case "missing-title":
				p.Images = p.Images[1:]
			case "missing-subtitle":
				p.Images = p.Images[:1]
			case "missing-slot":
				s = nil
			case "wrong-page":
				p.Number = 3
			}
			if templateOwnedHeaderArtwork(p, s, metadata) {
				t.Fatal("non-header content was silently discarded")
			}
		})
	}
}

func layeredHeaderGeometry() GeometryPage {
	// Measured from the current A4 source; hashes are not part of the recognition rule.
	return GeometryPage{Number: 2, Width: templateV1Width, Height: templateV1Height, Images: []GeometryImage{
		{Box: sourceArtBox(72.24, 77.04, 357.42, 52.62)},
		{Box: sourceArtBox(307.32, 96, 91.02, 27.9)},
		{Box: sourceArtBox(318.48, 94.439, 204.42, 25.259)},
		{Box: sourceArtBox(444.96, 96, 77.94, 27.9)},
		{Box: sourceArtBox(453.96, 98.28, 68.94, 25.38)},
		{Box: sourceArtBox(272.52, 178.56, 265.74, 30.54)},
		{Box: sourceArtBox(449.4, 180.839, 88.86, 31.859)},
	}}
}

func TestTemplateOwnedHeaderArtworkSupportsConnectedWordArtLayers(t *testing.T) {
	page := layeredHeaderGeometry()
	metadata := CanonicalMetadata{Title: "刻骨銘心、愛的碰觸(恭賀田蓉家園十週年)", Subtitle: "落實神兒女的四樣基本功"}
	slots, _ := bodyCanonicalArtSlots(page, metadata)
	if !templateOwnedHeaderArtwork(page, slots, metadata) {
		t.Fatal("connected title/shadow layers still require issue-specific approval")
	}
	for i, j := 0, len(page.Images)-1; i < j; i, j = i+1, j-1 {
		page.Images[i], page.Images[j] = page.Images[j], page.Images[i]
	}
	if !templateOwnedHeaderArtwork(page, slots, metadata) {
		t.Fatal("image enumeration order changed header recognition")
	}
	page = layeredHeaderGeometry()
	for _, name := range []string{"missing-main-title", "disconnected-title-image", "disconnected-subtitle-image", "too-many-layers", "outside-header"} {
		t.Run(name, func(t *testing.T) {
			p := page
			p.Images = append([]GeometryImage(nil), page.Images...)
			switch name {
			case "missing-main-title":
				p.Images = p.Images[1:]
			case "disconnected-title-image":
				p.Images = append(p.Images, GeometryImage{Box: sourceArtBox(5, 96, 20, 25)})
			case "disconnected-subtitle-image":
				p.Images = append(p.Images, GeometryImage{Box: sourceArtBox(5, 180, 20, 25)})
			case "too-many-layers":
				p.Images = append(p.Images, p.Images...)
			case "outside-header":
				p.Images[1].Box.Y = .5
			}
			if templateOwnedHeaderArtwork(p, slots, metadata) {
				t.Fatal("unrelated image or missing title was silently accepted")
			}
		})
	}
}

func TestLayeredArtworkRequiresV10AndIndependentCoverMatch(t *testing.T) {
	for _, name := range []string{"matched", "cover-mismatch", "v9"} {
		t.Run(name, func(t *testing.T) {
			source, canonical, manifest, snapshot := v9CandidateSource()
			source.Pages[1].Images = layeredHeaderGeometry().Images
			manifest.RendererVersion = "v10"
			if name == "cover-mismatch" {
				source.Pages[0].Texts[0].Text += "不同"
			} else if name == "v9" {
				manifest.RendererVersion = "v9"
			}
			candidate, issues, err := applyTemplate(source, canonical, manifest, &snapshot)
			if err != nil || candidate.Document == nil {
				t.Fatal("candidate unavailable", err)
			}
			artReview := false
			for _, issue := range issues {
				artReview = artReview || issue.Code == "inferred_body_art_geometry" || issue.Code == "unknown_source_image" && issue.SourcePage == 2
			}
			if artReview != (name != "matched") {
				t.Fatal("header artwork bypassed the independent cover/version guard", issues)
			}
		})
	}
}

func TestCurrentFixedChecksNameTheMissingComponentAndPage(t *testing.T) {
	d := validDocument(4)
	for _, field := range currentFixedFields {
		page := field.page
		if page < 0 {
			page += len(d.Pages)
		}
		d.LayoutManifest.Pages[page].FixedSlots = append(d.LayoutManifest.Pages[page].FixedSlots, FixedSlot{Element: field.element})
	}
	if issues := currentFixedTemplateIssues(d); len(issues) != 0 {
		t.Fatal("complete fixed template still has blanket warning", issues)
	}
	d.LayoutManifest.Pages[1].FixedSlots = nil
	for _, issue := range currentFixedTemplateIssues(d) {
		if issue.SourcePage != 2 || issue.ComponentID != d.Components[1].ID || issue.Code != "unmapped_fixed_template" {
			t.Fatal("missing field lacks actionable locator", issue)
		}
	}
	cover := &d.LayoutManifest.Pages[0]
	cover.FixedSlots = append(cover.FixedSlots, cover.FixedSlots[0])
	if len(currentFixedTemplateIssues(d)) < 2 {
		t.Fatal("duplicate fixed field ignored")
	}
}

func TestCurrentBackFramesUseThreeSemanticPanelsNotSourceBorderCount(t *testing.T) {
	source, _, err := filterTemplateV1Geometry(candidateGeometry())
	if err != nil {
		t.Fatal(err)
	}
	page := source.Pages[3]
	_, slots, labels, _, err := backComponents(page)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := json.Marshal(page)
	frames, issues := currentBackFrameSlots(page, slots, labels)
	if len(frames) != 3 || len(issues) != 0 {
		t.Fatal("three semantic panels not reconstructed", issues)
	}
	for _, frame := range frames {
		if !validBox(frame.Box) || (frame.Box.Y+frame.Box.Height)*page.Height > backContentBottom(page) {
			t.Fatal("panel crossed source exclusion boundary")
		}
	}
	for _, malformed := range []GeometryPage{
		{Number: page.Number, Width: page.Width, Height: page.Height},
		{Number: page.Number, Width: page.Width, Height: page.Height, backContentBottom: page.backContentBottom, Frames: []GeometryFrame{{Element: "unknown", Box: frames[0].Box}}},
		{Number: page.Number, Width: page.Width, Height: page.Height, backContentBottom: page.backContentBottom, Frames: []GeometryFrame{{Element: "summaryFrame", Box: frames[0].Box}, {Element: "summaryFrame", Box: frames[0].Box}}},
		{Number: page.Number, Width: page.Width, Height: page.Height, backContentBottom: page.backContentBottom, Frames: []GeometryFrame{{Element: "summaryFrame", Box: Box{X: .1, Y: .1, Width: .8, Height: .7}}}},
	} {
		if _, issues := currentBackFrameSlots(malformed, slots, labels); len(issues) == 0 {
			t.Fatal("unfiltered or ambiguous frame source accepted")
		}
	}
	if _, issues := currentBackFrameSlots(page, slots, labels[:1]); len(issues) == 0 {
		t.Fatal("missing semantic anchor accepted")
	}
	after, _ := json.Marshal(page)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("source geometry mutated")
	}
}

func TestCurrentSummaryFrameUsesPageWidthWithoutCoveringSidebar(t *testing.T) {
	page := GeometryPage{Number: 16, Width: 600, Height: 800, backContentBottom: 464}
	slots := []Slot{
		{ComponentID: "back-summary", Box: Box{X: .185, Y: .054, Width: .715, Height: .17}},
		{ComponentID: "announcements", Box: Box{X: .034, Y: .28, Width: .93, Height: .1}},
		{ComponentID: "prayers", Box: Box{X: .034, Y: .48, Width: .92, Height: .07}},
	}
	labels := []FixedSlot{
		{Element: "announcementsLabel", Box: Box{X: .4, Y: .25, Width: .2, Height: .02}},
		{Element: "prayersLabel", Box: Box{X: .034, Y: .45, Width: .1, Height: .02}},
		{Element: "summarySidebarTitle", Box: Box{X: .089, Y: .108, Width: .019, Height: .064}},
	}
	before, _ := json.Marshal(slots)
	frames, issues := currentBackFrameSlots(page, slots, labels)
	if len(issues) != 0 || len(frames) != 3 {
		t.Fatal("valid panels failed", issues)
	}
	summary := frames[0].Box
	if summary.Width <= .8 || summary.X < .108+12/page.Width {
		t.Fatal("summary squeezed around narrow text or covers sidebar", summary)
	}
	after, _ := json.Marshal(slots)
	if string(before) != string(after) {
		t.Fatal("frame correction changed source text geometry")
	}
}
