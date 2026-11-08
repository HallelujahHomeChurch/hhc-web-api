package onlinebulletins

import (
	"math"
	"strings"
)

// Reviewed WordArt shadows prove only these canonical titles at these source bands.
// New artwork stays reviewable until an independently confirmed example exists.
var currentBodyArtwork = []struct {
	title, subtitle string
	images          []GeometryImage
}{
	{"詩篇廿三篇、洗革拉戰役", "被聖靈充滿必有的三個看見", []GeometryImage{
		{SHA256: "741094bdc4f6bf2e2bf2b918ebc19ead97980c95752e384698a1bfdeb5191ef6", Box: sourceArtBox(62.04, 71.16, 394.02, 52.02)},
		{SHA256: "415a74ee960f9f780926c0bfe03a953570d9cce8e2ca278a3f5d450aa63fdb27", Box: sourceArtBox(243.24, 181.321, 293.34, 28.379)},
	}},
	{"永恆的命定和呼召", "我們起來建造吧!", []GeometryImage{
		{SHA256: "595148280fc9e1e3f2dabd9fc8f203c9a4d780d4a7c8c0966bfd4c231a60e766", Box: sourceArtBox(61.08, 75.96, 360.18, 52.62)},
		{SHA256: "c5f71a5c6b6dbab9b8f0e9467d6af190d79dd24939674402d1d327f091caa488", Box: sourceArtBox(306, 176.041, 226.86, 30.539)},
		{SHA256: "14c988b39eb3202e1be7fb3180bd315965f39f46bd5d8281cf625aaec57fc1b6", Box: sourceArtBox(446.76, 178.081, 86.1, 31.979)},
		{SHA256: "27f54836fd5c49a1b699e15d63d90ff77846b24ca8949b6245c189916c90e913", Box: sourceArtBox(459.96, 180.839, 72.9, 29.221)},
	}},
	{"這條路上我們一起走", "五個特點、七個要素、五個態度、三個品格", []GeometryImage{
		{SHA256: "c8cea794ea99a015c42fb7aa79996160a7a3ace8d63070382a289bde7d6d6c22", Box: sourceArtBox(65.76, 69.72, 355.74, 52.62)},
		{SHA256: "bb848ddb9e0d788d190e871da1db1d5a9793647476a533f301891776d72cb90a", Box: sourceArtBox(151.8, 186.601, 385.74, 30.539)},
	}},
}

func sourceArtBox(x, y, width, height float64) Box {
	return Box{X: x / templateV1Width, Y: y / templateV1Height, Width: width / templateV1Width, Height: height / templateV1Height}
}

func verifiedCurrentBodyArtwork(page GeometryPage, metadata CanonicalMetadata) bool {
	if page.Number != 2 || math.Abs(page.Width-templateV1Width) > .02 || math.Abs(page.Height-templateV1Height) > .02 {
		return false
	}
	compact := func(text string) string { return strings.Join(strings.Fields(strings.TrimLeft(text, "～~")), "") }
	for _, profile := range currentBodyArtwork {
		if compact(metadata.Title) != compact(profile.title) || compact(metadata.Subtitle) != compact(profile.subtitle) || len(page.Images) != len(profile.images) {
			continue
		}
		seen := map[string]bool{}
		for _, image := range page.Images {
			matched := false
			for _, expected := range profile.images {
				matched = matched || image.SHA256 == expected.SHA256 && historicalImageBox(image.Box, expected.Box)
			}
			if !matched || seen[image.SHA256] {
				return false
			}
			seen[image.SHA256] = true
		}
		return true
	}
	return false
}

// The reader replaces this template-owned WordArt with canonical text, not a raster transcription.
func templateOwnedHeaderArtwork(page GeometryPage, slots []FixedSlot, metadata CanonicalMetadata) bool {
	if page.Number != 2 || !supportedTemplatePaper(page) || len(page.Images) < 1 || len(page.Images) > 12 {
		return false
	}
	want := map[string]bool{"title": true, "topRule": true}
	if metadata.Subtitle != "" {
		want["subtitle"] = true
	}
	for _, slot := range slots {
		if !validBox(slot.Box) || !want[slot.Element] {
			return false
		}
		delete(want, slot.Element)
	}
	if len(want) != 0 {
		return false
	}
	title, subtitle := false, metadata.Subtitle == ""
	var titleLayers, subtitleLayers []Box
	for _, image := range page.Images {
		if !validBox(image.Box) {
			return false
		}
		top, bottom, height := image.Box.Y*page.Height, (image.Box.Y+image.Box.Height)*page.Height, image.Box.Height*page.Height
		switch {
		case top >= 55 && top <= 135 && bottom <= 180 && height >= 20 && height <= 80:
			title = title || top <= 100 && height >= 35 && image.Box.Width >= .2
			titleLayers = append(titleLayers, image.Box)
		case metadata.Subtitle != "" && top >= 160 && top <= 200 && bottom <= 245 && height >= 20 && height <= 45:
			subtitle = subtitle || image.Box.Width >= .2
			subtitleLayers = append(subtitleLayers, image.Box)
		default:
			return false
		}
	}
	return title && subtitle && connectedArtworkLayers(titleLayers) && connectedArtworkLayers(subtitleLayers)
}

// WordArt fragments must overlap into one header, not hide a separate image in the same band.
func connectedArtworkLayers(layers []Box) bool {
	if len(layers) < 2 {
		return true
	}
	connected := make([]bool, len(layers))
	connected[0] = true
	for count := 1; count < len(layers); {
		before := count
		for i, box := range layers {
			if connected[i] {
				continue
			}
			for j, other := range layers {
				if connected[j] && box.X < other.X+other.Width && other.X < box.X+box.Width && box.Y < other.Y+other.Height && other.Y < box.Y+box.Height {
					connected[i] = true
					count++
					break
				}
			}
		}
		if count == before {
			return false
		}
	}
	return true
}

var currentFixedFields = []struct {
	page    int
	element string
}{
	{0, "logo"}, {0, "masthead"}, {0, "title"}, {0, "titleLabel"}, {0, "date"}, {0, "issueNumber"}, {0, "pastor"},
	{0, "visionMission"}, {0, "visionFellowship"}, {0, "visionCommitment"},
	{0, "welcomeLabel"}, {0, "worshipLabel"}, {0, "workLabel"}, {0, "wordLabel"}, {0, "verseLabel"},
	{1, "title"}, {1, "topRule"}, {-2, "hymnLabel"},
	{-1, "summaryFrame"}, {-1, "announcementsFrame"}, {-1, "prayersFrame"}, {-1, "announcementsLabel"}, {-1, "prayersLabel"},
	{-1, "summarySidebarTagline"}, {-1, "summarySidebarTitle"},
}

func currentFixedTemplateIssues(document Document) []ReviewIssue {
	issues := []ReviewIssue{}
	for _, field := range currentFixedFields {
		page := field.page
		if page < 0 {
			page += len(document.Pages)
		}
		count := 0
		for _, slot := range document.LayoutManifest.Pages[page].FixedSlots {
			if slot.Element == field.element {
				count++
			}
		}
		if count == 1 {
			continue
		}
		component := "cover"
		switch page {
		case 1:
			component = document.Components[1].ID
		case len(document.Pages) - 2:
			component = "hymns"
		case len(document.Pages) - 1:
			component = "back-summary"
			if strings.HasPrefix(field.element, "announcements") {
				component = "announcements"
			} else if strings.HasPrefix(field.element, "prayers") {
				component = "prayers"
			}
		}
		issues = append(issues, ReviewIssue{Code: "unmapped_fixed_template", SourcePage: page + 1, ComponentID: component, Blocking: true})
	}
	return issues
}

// Frames are reader presentation around already validated semantic content,
// not evidence that an absent source border or unknown source text was recognized.
func currentBackFrameSlots(page GeometryPage, slots []Slot, labels []FixedSlot) ([]FixedSlot, []ReviewIssue) {
	unmapped := []ReviewIssue{{Code: "unmapped_back_decoration", ComponentID: "back-summary", SourcePage: page.Number, Blocking: true}}
	if page.backContentBottom <= 0 {
		return nil, unmapped
	}
	seen := map[string]bool{}
	for _, frame := range page.Frames {
		if seen[frame.Element] || (frame.Element != "summaryFrame" && frame.Element != "announcementsFrame" && frame.Element != "prayersFrame") || !validBox(frame.Box) || (frame.Box.Y+frame.Box.Height)*page.Height > backContentBottom(page) {
			return nil, unmapped
		}
		seen[frame.Element] = true
	}
	frames := []FixedSlot{}
	for i, id := range []string{"back-summary", "announcements", "prayers"} {
		left, top, right, bottom := 1.0, 1.0, 0.0, 0.0
		include := func(box Box) {
			left, top = math.Min(left, box.X), math.Min(top, box.Y)
			right, bottom = math.Max(right, box.X+box.Width), math.Max(bottom, box.Y+box.Height)
		}
		found := false
		for _, slot := range slots {
			if slot.ComponentID == id {
				found = true
				include(slot.Box)
			}
		}
		if !found {
			return nil, unmapped
		}
		label := []string{"", "announcementsLabel", "prayersLabel"}[i]
		count := 0
		for _, slot := range labels {
			if slot.Element == label {
				count++
				include(slot.Box)
			}
		}
		if label != "" && count != 1 {
			return nil, unmapped
		}
		box := Box{X: math.Max(0, left-2/page.Width), Y: math.Max(0, top-2/page.Height), Width: math.Min(1, right+2/page.Width) - math.Max(0, left-2/page.Width), Height: bottom - math.Max(0, top-2/page.Height)}
		if !validBox(box) || bottom*page.Height > backContentBottom(page) {
			return nil, unmapped
		}
		element := []string{"summaryFrame", "announcementsFrame", "prayersFrame"}[i]
		frames = append(frames, FixedSlot{sourcePage: page.Number, ID: "fixed-back-" + element, Element: element, Box: box, Style: ParagraphStyle{FontSize: 6, LineHeight: 6}})
	}
	// A summary border should use the available panel width, not hug the
	// source's inset text column. Keep the proven vertical sidebar outside it.
	left := math.Min(frames[1].Box.X, frames[2].Box.X)
	right := math.Max(frames[1].Box.X+frames[1].Box.Width, frames[2].Box.X+frames[2].Box.Width)
	for _, label := range labels {
		if label.Element == "summarySidebarTitle" || label.Element == "summarySidebarTagline" {
			left = math.Max(left, label.Box.X+label.Box.Width+12/page.Width)
		}
	}
	summary := &frames[0].Box
	left = math.Min(left, summary.X)
	right = math.Max(right, summary.X+summary.Width)
	summary.X, summary.Width = left, right-left
	return frames, nil
}
