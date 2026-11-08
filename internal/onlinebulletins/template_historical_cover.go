package onlinebulletins

import (
	"math"
	"strings"
	"time"
)

type historicalCoverProposal struct {
	fixed            []FixedSlot
	issues           []ReviewIssue
	recognizedImages []GeometryImage
}

var historicalCoverRows = []struct {
	element, text string
	x, y          float64
}{
	{"historicalVision", "一個異象：合一與宣教", .421975, .146477},
	{"historicalGoals", "兩個目標：宣教為中國、中國為宣教", .455637, .165434},
	{"historicalActions", "三個行動：共同生活、愛與成全、恩膏傳承", .489300, .184533},
	{"historicalCommitment", "四個堅持：宣教主導、靈恩神學、團隊事奉、門徒訓練", .522794, .203490},
}

func historicalFixedElement(element string) bool {
	switch element {
	case "historicalVision", "historicalGoals", "historicalGospelGoals", "historicalActions", "historicalCommitment":
		return true
	}
	return false
}

func historicalCoverFinding(code string) ReviewIssue {
	return ReviewIssue{Code: code, ComponentID: "cover", SourcePage: 1, Blocking: true}
}

// Complete native rows prove wording; image anchors only permit a reviewable
// reconstruction. Neither paper size nor an issue number selects a profile.
func historicalCoverHeader(page GeometryPage, metadata CanonicalMetadata) (*historicalCoverProposal, error) {
	if page.Number != 1 || !supportedTemplatePaper(page) {
		return nil, ErrUnsupportedSource
	}
	band := page
	band.Texts = nil
	hasHistorical := false
	outsideBand := false
	for _, part := range page.Texts {
		// Include unmatched evidence between the masthead and theme row, not
		// just the four expected baselines; an extra row must stay reviewable.
		if part.Box.Y >= .12 && part.Box.Y < .245 && part.Box.X >= .4 {
			band.Texts = append(band.Texts, part)
		}
		if part.Box.Y < .28 {
			for _, prefix := range []string{"一個異象", "兩個目標", "三個行動", "四個堅持"} {
				if strings.HasPrefix(strings.TrimSpace(part.Text), prefix) {
					hasHistorical = true
					outsideBand = outsideBand || part.Box.Y < .14 || part.Box.Y >= .235 || part.Box.X < .4
				}
			}
		}
	}
	if !hasHistorical {
		return outlinedHistoricalCover(page, metadata)
	}
	proposal := &historicalCoverProposal{}
	mismatch := func() (*historicalCoverProposal, error) {
		return &historicalCoverProposal{issues: []ReviewIssue{historicalCoverFinding("fixed_template_source_mismatch")}}, nil
	}
	if outsideBand {
		return mismatch()
	}
	lines, err := sourceLines(band, false)
	if err != nil {
		return nil, err
	}
	if len(lines) != len(historicalCoverRows) {
		return mismatch()
	}
	for i, row := range historicalCoverRows {
		line := lines[i]
		element, expected := row.element, row.text
		if i == 1 && strings.Join(strings.Fields(lineText(line)), "") == "兩個目標：福音為華人、華人為福音" {
			element, expected = "historicalGospelGoals", "兩個目標：福音為華人、華人為福音"
		}
		parts := wholeSourceRuns(line.parts, expected)
		if len(parts) != len(line.parts) || len(parts) == 0 {
			return mismatch()
		}
		box := textBounds(parts)
		if math.Abs(box.X-row.x) > .006 || math.Abs(box.Y-row.y) > .006 {
			return mismatch()
		}
		size := 0.0
		for _, part := range parts {
			size = math.Max(size, part.FontSize)
		}
		proposal.fixed = append(proposal.fixed, FixedSlot{ID: "fixed-cover-" + element, Element: element, Box: box, Style: ParagraphStyle{FontSize: size, LineHeight: math.Max(size, box.Height*page.Height)}})
	}
	return proposal, nil
}

// Hand-reviewed source images distinguish historical and current outlined
// Letter headers. Foreground wording remains an explicit proposal.
const currentOutlinedMasthead = "fd9ce69e74ef5f57b9cc948e25a9fb0d86c05b64204bb6126352f1c085bab096"

var outlinedHistoricalAnchors = []struct {
	logo, masthead string
	footer         [3]string
}{
	{"7ffe7e623170a51415b8628853df420c05a76f33dbe70ae159b275bad9cc530f", "c046969d906573ed942217ea8bc55eaa6d9f4d5795c8c9bd4575dd7a9744fb17", [3]string{"b61571e95bf1ddea4537d186f30b4c26ba03034d8687788044979437d88f5125", "a5401ae1c23d2b72707bc272f04d23b7dcb3386f6a425efac0b89805687a08da", "f487e1018bd4caa3d79f56d71ba8a7c1d113facdf16ce1c1fc0489aefa676a35"}},
	{"84e00570648d78276704c48b41e50dae11d536683537d9f70f88a2eed3c865cf", "13190cfb73aa3d33ada276cc5b37571f4b95b8763e9a82bb3c84528ea9c0329c", [3]string{"0ecb86f9aadf640df219b8831a38a4033bebd773a1705c4f32354563321d8ef2", "7eea481f7a24c0ede39d22c63d11dd51b1f8045a6c3e8ca556b026a239451979", "e24b4e454187ee17fcee156bf5421b5e34c4d685fad95413cca539bbfa32e5cc"}},
	{"84e00570648d78276704c48b41e50dae11d536683537d9f70f88a2eed3c865cf", currentOutlinedMasthead, [3]string{"0ecb86f9aadf640df219b8831a38a4033bebd773a1705c4f32354563321d8ef2", "732bbc333cf1479397886345af823482c0f17ce57b70c94103fbc0af1c5b98a2", "7d99cafe720d13d0fb9295bb6ce0db853581a9680aa0393e31447573beec6ded"}},
}

func outlinedHistoricalCover(page GeometryPage, metadata CanonicalMetadata) (*historicalCoverProposal, error) {
	if math.Abs(page.Width-612) > .02 || math.Abs(page.Height-792) > .02 {
		return nil, nil
	}
	knownAnchor := false
	current := false
	for _, image := range page.Images {
		for _, profile := range outlinedHistoricalAnchors {
			knownAnchor = knownAnchor || image.SHA256 == profile.logo || image.SHA256 == profile.masthead
		}
	}
	if !knownAnchor {
		return nil, nil
	}
	// The wording band and masthead are independent reusable template parts.
	for _, part := range page.Texts {
		current = current || strings.TrimSpace(part.Text) == "門" && historicalImageBox(part.Box, Box{X: .818431405, Y: .194348255, Width: .015269975, Height: .011787727})
	}
	mismatch := &historicalCoverProposal{issues: []ReviewIssue{historicalCoverFinding("fixed_template_source_mismatch")}}
	glyphs := map[string]bool{}
	exclamations, punctuationBoxes := map[float64]int{}, map[Box]bool{}
	for _, part := range page.Texts {
		if part.Box.Y < .28 {
			valid := false
			glyph := strings.TrimSpace(part.Text)
			if part.FontFamily == "DFKaiShu-SB-Estd-BF" && !glyphs[glyph] {
				switch glyph {
				case "門":
					box := Box{X: .828039208, Y: .203439169, Width: .015269975, Height: .011787727}
					if current {
						box.X, box.Y = .818431405, .194348255
					}
					valid = part.FontSize == 9 && historicalImageBox(part.Box, box)
				case "～":
					minX, maxX := .46, .60
					if current {
						minX, maxX = .30, .65
					}
					valid = part.FontSize == 11 && part.Box.X >= minX && part.Box.X <= maxX && historicalImageBox(part.Box, Box{X: part.Box.X, Y: .254967542, Width: .018397464, Height: .014228821})
				}
			}
			if current && !glyphs[glyph] && (glyph == "(" || glyph == ")") && strings.Contains(metadata.Title, glyph) {
				valid = part.FontFamily == "TimesNewRomanPSMT" && part.FontSize == 13 && part.Box.X >= .3 && part.Box.X <= .65 && historicalImageBox(part.Box, Box{X: part.Box.X, Y: .251543673, Width: .0078, Height: .018412681})
			}
			if current && glyph == "!" && part.FontFamily == "TimesNewRomanPSMT" {
				text := metadata.Subtitle
				box := Box{X: part.Box.X, Y: .251543673, Width: .007167825, Height: .018412681}
				if part.FontSize == 17 {
					text = metadata.Title
					box.Y, box.Width, box.Height = .247363652, .009189534, .023606040
				}
				valid = (part.FontSize == 13 || part.FontSize == 17) && part.Box.X >= .3 && part.Box.X <= .9 && !punctuationBoxes[part.Box] && exclamations[part.FontSize] < strings.Count(text, "!") && historicalImageBox(part.Box, box)
				exclamations[part.FontSize]++
				punctuationBoxes[part.Box] = true
			}
			if !valid {
				return mismatch, nil
			}
			glyphs[glyph] = true
		}
	}
	if !glyphs["門"] || !glyphs["～"] || glyphs["("] != glyphs[")"] {
		return mismatch, nil
	}
	for _, profile := range outlinedHistoricalAnchors {
		var logo, masthead *GeometryImage
		valid := true
		for i := range page.Images {
			image := &page.Images[i]
			switch image.SHA256 {
			case profile.logo:
				if logo != nil || image.Width != 392 || image.Height != 392 || !historicalImageBox(image.Box, Box{X: .159215685, Y: .035454548, Width: .144509802, Height: .111666665}) {
					valid = false
				}
				logo = image
			case profile.masthead:
				width, height := 1042, 67
				box := Box{X: .358431386, Y: .095151514, Width: .384117650, Height: .019090909}
				if profile.masthead == currentOutlinedMasthead {
					width, height = 1068, 69
					box = Box{X: .358431386, Y: .079242422, Width: .393725489, Height: .019696968}
				}
				if masthead != nil || image.Width != width || image.Height != height || !historicalImageBox(image.Box, box) {
					valid = false
				}
				masthead = image
			default:
				if image.Box.Y < .28 {
					valid = false
				}
			}
		}
		if !valid || logo == nil || masthead == nil {
			continue
		}
		if _, err := time.Parse("2006-01-02", metadata.Date); err != nil || !boundedText(metadata.Title, true) || !boundedText(metadata.Subtitle, false) || metadata.IssueNumber < 1 {
			return nil, ErrUnsupportedSource
		}
		proposal := &historicalCoverProposal{issues: []ReviewIssue{historicalCoverFinding("unmapped_fixed_template")}, recognizedImages: []GeometryImage{*logo, *masthead}}
		add := func(element string, box Box, size, leading float64) {
			proposal.fixed = append(proposal.fixed, FixedSlot{ID: "fixed-cover-" + element, Element: element, Box: box, Style: ParagraphStyle{FontSize: size, LineHeight: leading}})
		}
		add("logo", logo.Box, 8, 8)
		add("masthead", Box{X: .376, Y: .060, Width: .42, Height: .046}, 25, 30)
		if current {
			for i, element := range []string{"visionMission", "visionFellowship", "visionCommitment"} {
				// The configurable church text compositor requires at least 9 pt.
				add(element, Box{X: .44 + float64(i)*.031, Y: .156 + float64(i)*.019, Width: .51 - float64(i)*.031, Height: .018}, 9, 12)
			}
			// The source mission word order differs from the code-owned current text.
			proposal.issues = append(proposal.issues, historicalCoverFinding("fixed_template_text_variant"))
		} else {
			for i, row := range historicalCoverRows {
				add(row.element, Box{X: .428 + float64(i)*.031, Y: .1465 + float64(i)*.019, Width: .51 - float64(i)*.031, Height: .018}, 8, 12)
			}
		}
		add("date", Box{X: .132, Y: .187, Width: .115, Height: .023}, 11, 15)
		add("issueNumber", Box{X: .248, Y: .187, Width: .085, Height: .023}, 11, 15)
		add("pastor", Box{X: .132, Y: .208, Width: .22, Height: .023}, 11, 15)
		add("titleLabel", Box{X: .10, Y: .238, Width: .158, Height: .034}, 14, 20)
		titleSize := 18.0
		if current {
			// Current outlined themes use a compact shared title/subtitle band.
			titleSize = 14
		}
		add("title", Box{X: .30, Y: .247, Width: .30, Height: .032}, titleSize, 24)
		if metadata.Subtitle != "" {
			add("subtitle", Box{X: .61, Y: .249, Width: .33, Height: .027}, 14, 20)
		}
		// QR codes stay excluded only at their reviewed footer origins.
		for _, image := range page.Images {
			for _, profile := range outlinedHistoricalAnchors {
				for i, hash := range profile.footer {
					boxes := []Box{{X: .438039219, Width: .072745100, Height: .055757573}, {X: .529019600, Width: .058627446, Height: .047272722}, {X: .612352933, Width: .061568626, Height: .047575756}}
					box := boxes[i]
					box.Y = image.Box.Y
					if image.SHA256 == hash && image.Box.Y >= .87 && image.Box.Y <= .9 && historicalImageBox(image.Box, box) {
						proposal.recognizedImages = append(proposal.recognizedImages, image)
					}
				}
			}
		}
		return proposal, nil
	}
	return mismatch, nil
}

func historicalImageBox(actual, expected Box) bool {
	return validBox(actual) && math.Abs(actual.X-expected.X) <= .003 && math.Abs(actual.Y-expected.Y) <= .003 && math.Abs(actual.Width-expected.Width) <= .003 && math.Abs(actual.Height-expected.Height) <= .003
}
