package onlinebulletins

import (
	"math"
	"strings"
)

var coverFooterFields = []struct {
	element     string
	left, right float64
	lines       []string
}{
	{"contact", 0, .42, []string{
		"教會地址：臺北市仁愛路三段29號B1",
		"Address:No.29,B1,Sec3,Ren-Ai.Rd,Taipei,Taiwan",
		"電子郵件 (E-mail)：home.church@msa.hinet.net",
		"教會網址 (Website)：www.alive.org.tw",
		"電話 (Phone)：(02) 2776-1157 / 8773-1972",
		"傳真 (Fax)：(02) 2781-4980",
	}},
	{"websiteQRLabel", .42, .51, []string{"家教會官網"}},
	{"youtubeQRLabel", .51, .60, []string{"家教會", "YOUTUBE"}},
	{"streamQRLabel", .60, .70, []string{"神國大樂", "京成兄妹"}},
	{"scanHint", .70, 1, []string{"歡迎使用您的手機掃描", "左方的QRCODE圖樣", "可以立即加入家教會官網", "直播/下載週報/福音餐會"}},
}

// Reviewed source-image identities locate the legal code-owned replacements.
// Never keep source pixels or infer an unknown image's purpose from its size.
var coverGraphicFields = []struct {
	element, sha256 string
	box             Box
}{
	{"logo", "2b1f2ed34df0edd7f3217a97312ce42190c923742ac7eb5ab56a7afb3b582161", Box{X: .128166, Y: .035633, Width: .157226, Height: .111174}},
	{"websiteQR", "4c5bfab9ff5ffb6c60ca78ab6331950842ea507e568b99afea5293441535e46f", Box{X: .432708, Y: .858573, Width: .079369, Height: .055706}},
	{"youtubeQR", "8f081fe5c5b7413390cd8de52259f852d1257b25bf91c883e5a3979310f918e0", Box{X: .532571, Y: .862433, Width: .063327, Height: .046620}},
	{"streamQR", "d87db04790d600818b33ff1b6e7268b97292dc8ed1635e347e1ac3f02e19b9df", Box{X: .623614, Y: .861899, Width: .066435, Height: .046976}},
}

func coverGraphicElement(image GeometryImage) string {
	for _, field := range coverGraphicFields {
		if image.SHA256 == field.sha256 && math.Abs(image.Box.X-field.box.X) <= .003 && math.Abs(image.Box.Y-field.box.Y) <= .003 && math.Abs(image.Box.Width-field.box.Width) <= .003 && math.Abs(image.Box.Height-field.box.Height) <= .003 {
			return field.element
		}
	}
	return ""
}

func isCoverMastheadAnchor(image GeometryImage) bool {
	return image.SHA256 == "a00d0e18ecaa51e26fe18e88d5550ca4a16091729a8573b267b04a9bc7e47803" && math.Abs(image.Box.X-.345696) <= .003 && math.Abs(image.Box.Y-.079247) <= .003 && math.Abs(image.Box.Width-.429853) <= .003 && math.Abs(image.Box.Height-.019456) <= .003
}

// The online cover ends at the weekly verse. Footer QR codes are excluded,
// but only their reviewed identities may be ignored, never arbitrary images.
func isExcludedCoverQR(image GeometryImage) bool {
	if image.Box.Y < .85 {
		return false
	}
	for _, field := range coverGraphicFields[1:] {
		if image.SHA256 == field.sha256 {
			return true
		}
	}
	return false
}

func coverReaderLogoSlots(page GeometryPage) ([]FixedSlot, []ReviewIssue) {
	slots := []FixedSlot{}
	for _, image := range page.Images {
		if coverGraphicElement(image) == "logo" {
			slots = append(slots, FixedSlot{ID: "fixed-cover-logo", Element: "logo", Box: image.Box, Style: ParagraphStyle{FontSize: 8, LineHeight: 8}})
		}
	}
	if len(slots) != 1 {
		return nil, []ReviewIssue{{Code: "fixed_template_source_mismatch", ComponentID: "cover", Blocking: true}}
	}
	return slots, nil
}

func coverMastheadSlot(page GeometryPage) (*FixedSlot, []ReviewIssue) {
	matches := 0
	if page.Number == 1 {
		for _, image := range page.Images {
			if isCoverMastheadAnchor(image) {
				matches++
			}
		}
	}
	if matches != 1 {
		return nil, []ReviewIssue{{Code: "fixed_template_source_mismatch", ComponentID: "cover", Blocking: true}}
	}
	// The source image is only the WordArt shadow, not the foreground's box.
	// Native foreground placement is the reviewed church-owned V1 profile;
	// it is provisional until the full renderer/source visual gate passes.
	return &FixedSlot{ID: "fixed-cover-masthead", Element: "masthead", Box: Box{X: 218.25 / templateV1Width, Y: 44 / templateV1Height, Width: 246.12 / templateV1Width, Height: 36 / templateV1Height}, Style: ParagraphStyle{FontSize: 28, LineHeight: 36, LetterSpacing: -.135}}, nil
}

func coverFooterSlots(page GeometryPage) ([]FixedSlot, []ReviewIssue, error) {
	if page.Number != 1 {
		return nil, nil, ErrUnsupportedSource
	}
	slots, issues := []FixedSlot{}, []ReviewIssue{}
	mismatch := func() {
		issues = append(issues, ReviewIssue{Code: "fixed_template_source_mismatch", ComponentID: "cover", Blocking: true})
	}
	for _, field := range coverFooterFields {
		band := page
		band.Texts = nil
		for _, part := range page.Texts {
			if part.Box.Y >= .85 && part.Box.X >= field.left && part.Box.X < field.right {
				band.Texts = append(band.Texts, part)
			}
		}
		lines, err := sourceLines(band, false)
		if err != nil {
			return nil, nil, err
		}
		parts, text, size := []GeometryText{}, "", 0.0
		for _, line := range lines {
			parts = append(parts, line.parts...)
			text += lineText(line)
			for _, part := range line.parts {
				size = math.Max(size, part.FontSize)
			}
		}
		compact := func(text string) string { return strings.Join(strings.Fields(text), "") }
		if len(lines) != len(field.lines) || compact(text) != compact(strings.Join(field.lines, "")) {
			mismatch()
			continue
		}
		box := textBounds(parts)
		leading := math.Max(size, box.Height*page.Height)
		if len(lines) > 1 {
			leading = (textBounds(lines[len(lines)-1].parts).Y - textBounds(lines[0].parts).Y) * page.Height / float64(len(lines)-1)
		}
		slots = append(slots, FixedSlot{ID: "fixed-cover-" + field.element, Element: field.element, Box: box, Style: ParagraphStyle{FontSize: size, LineHeight: leading}})
	}
	for _, field := range coverGraphicFields {
		var found *GeometryImage
		duplicate := false
		for index := range page.Images {
			part := &page.Images[index]
			if coverGraphicElement(*part) != field.element {
				continue
			}
			if found != nil {
				duplicate = true
				break
			}
			found = part
		}
		if found == nil || duplicate {
			mismatch()
			continue
		}
		slots = append(slots, FixedSlot{ID: "fixed-cover-" + field.element, Element: field.element, Box: found.Box, Style: ParagraphStyle{FontSize: 8, LineHeight: 8}})
	}
	return slots, issues, nil
}

// These are church-owned, weekly-invariant labels. Their three vision rows have
// different source origins; combining them changes the reference cover layout.
func coverChurchSlots(page GeometryPage) ([]FixedSlot, []ReviewIssue, error) {
	return coverChurchFieldSlots(page, true)
}

// Snapshot values replace only these labelled rows, never arbitrary header text.
func coverSnapshotChurchSlots(page GeometryPage) ([]FixedSlot, []ReviewIssue, error) {
	slots, issues, err := coverChurchFieldSlots(page, false)
	if err != nil {
		return nil, nil, err
	}
	lines, err := sourceLines(page, false)
	if err != nil {
		return nil, nil, err
	}
	for i, field := range []struct{ element, label string }{{"visionMission", "異象使命："}, {"visionFellowship", "團契行動："}, {"visionCommitment", "堅持理想："}} {
		var matched []GeometryText
		labelCount := 0
		for _, line := range lines {
			if textBounds(line.parts).Y < .25 {
				labelCount += strings.Count(strings.Join(strings.Fields(lineText(line)), ""), field.label)
			}
			for _, part := range line.parts {
				if part.Box.X >= .42 && math.Abs(part.Box.Y-(.156+float64(i)*.019)) <= .006 {
					matched = append(matched, part)
				}
			}
		}
		text := ""
		for _, part := range matched {
			text += strings.Join(strings.Fields(part.Text), "")
		}
		if labelCount != 1 || len(matched) == 0 || !strings.HasPrefix(text, field.label) || strings.Count(text, field.label) != 1 || text == field.label {
			issues = append(issues, ReviewIssue{Code: "fixed_template_source_mismatch", ComponentID: "cover", SourcePage: 1, Blocking: true})
			continue
		}
		box := textBounds(matched)
		box.Width = .94 - box.X
		if !validBox(box) {
			issues = append(issues, ReviewIssue{Code: "fixed_template_source_mismatch", ComponentID: "cover", SourcePage: 1, Blocking: true})
			continue
		}
		size := 0.0
		for _, part := range matched {
			size = math.Max(size, part.FontSize)
		}
		slots = append(slots, FixedSlot{ID: "fixed-cover-" + field.element, Element: field.element, Box: box, Style: ParagraphStyle{FontSize: size, LineHeight: math.Max(size, box.Height*page.Height)}})
	}
	return slots, issues, nil
}

func coverChurchFieldSlots(page GeometryPage, currentVision bool) ([]FixedSlot, []ReviewIssue, error) {
	if page.Number != 1 {
		return nil, nil, ErrUnsupportedSource
	}
	lines, err := sourceLines(page, false)
	if err != nil {
		return nil, nil, err
	}
	fields := []struct {
		element, text string
		y             float64
	}{
		{"visionMission", "異象使命：華人遍地興起、福音傳到地極", .156},
		{"visionFellowship", "團契行動：共同生活、愛與成全、恩膏傳承", .175},
		{"visionCommitment", "堅持理想：宣教主導、靈恩神學、團隊事奉、門徒訓練", .194},
		{"pastor", "主任牧師：曾英欽牧師", .208},
		{"titleLabel", "本週靈糧主題", .245},
	}
	if !currentVision {
		fields = fields[3:]
	}
	slots, issues := []FixedSlot{}, []ReviewIssue{}
	for _, field := range fields {
		var matched []GeometryText
		duplicate := false
		for _, line := range lines {
			if math.Abs(textBounds(line.parts).Y-field.y) > .006 {
				continue
			}
			parts := wholeSourceRuns(line.parts, field.text)
			if len(parts) == 0 && field.element == "visionMission" {
				parts = wholeSourceRuns(line.parts, "異象使命：遍地華人興起、福音傳到地極")
				if len(parts) > 0 {
					issues = append(issues, ReviewIssue{Code: "fixed_template_text_variant", ComponentID: "cover", Blocking: true})
				}
			}
			if len(parts) == 0 {
				continue
			}
			if len(matched) > 0 {
				duplicate = true
				break
			}
			matched = parts
		}
		if len(matched) == 0 || duplicate {
			issues = append(issues, ReviewIssue{Code: "fixed_template_source_mismatch", ComponentID: "cover", Blocking: true})
			continue
		}
		box := textBounds(matched)
		size := 0.0
		for _, part := range matched {
			size = math.Max(size, part.FontSize)
		}
		slots = append(slots, FixedSlot{ID: "fixed-cover-" + field.element, Element: field.element, Box: box, Style: ParagraphStyle{FontSize: size, LineHeight: math.Max(size, box.Height*page.Height)}})
	}
	return slots, issues, nil
}
