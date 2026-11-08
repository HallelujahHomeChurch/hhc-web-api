package onlinebulletins

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

var historicalCoverTestRows = []struct{ element, text string }{
	{"historicalVision", "一個異象：合一與宣教"},
	{"historicalGoals", "兩個目標：宣教為中國、中國為宣教"},
	{"historicalActions", "三個行動：共同生活、愛與成全、恩膏傳承"},
	{"historicalCommitment", "四個堅持：宣教主導、靈恩神學、團隊事奉、門徒訓練"},
}

func TestHistoricalFixedElementsRequireV6(t *testing.T) {
	for _, version := range []string{"v1", "v2", "v3", "v4", "v5", "v6", "v7", "v8"} {
		for _, row := range append(append([]struct{ element, text string }{}, historicalCoverTestRows...), struct{ element, text string }{"historicalGospelGoals", "兩個目標：福音為華人、華人為福音"}) {
			d := validDocument(4)
			d.LayoutManifest.RendererVersion = version
			if version == "v2" {
				d.ContentLocale, d.TemplateVersion, d.LayoutManifest.TemplateVersion = "zh-Hans", "v2", "v2"
			}
			d.LayoutManifest.Pages[0].FixedSlots = []FixedSlot{{ID: "fixed-historical", Element: row.element, Box: Box{X: .4, Y: .14, Width: .5, Height: .02}, Style: ParagraphStyle{FontSize: 10, LineHeight: 14}}}
			encoded, err := json.Marshal(d)
			if err != nil {
				t.Fatal(err)
			}
			_, err = DecodeDocument(bytes.NewReader(encoded))
			if (version == "v6" || version == "v7" || version == "v8") != (err == nil) {
				t.Fatalf("%s %s: %v", version, row.element, err)
			}
		}
	}
	for _, locale := range []string{"zh-Hant", "zh-Hans"} {
		d := validDocument(4)
		d.ContentLocale, d.LayoutManifest.RendererVersion = locale, "v6"
		if locale == "zh-Hans" {
			d.TemplateVersion, d.LayoutManifest.TemplateVersion = "v2", "v2"
		}
		d.LayoutManifest.Pages[0].FixedSlots = []FixedSlot{{ID: "fixed-current", Element: "visionMission", Box: Box{X: .4, Y: .14, Width: .5, Height: .02}, Style: ParagraphStyle{FontSize: 10, LineHeight: 14}}}
		if _, err := ValidateDocument(d); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHistoricalNativeGospelGoalsPreservesReviewedVariant(t *testing.T) {
	s := historicalNativeGeometry()
	for i := range s.Pages[0].Texts {
		if s.Pages[0].Texts[i].Text == "兩個目標：宣教為中國、中國為宣教" {
			s.Pages[0].Texts[i].Text = "兩個目標：福音為華人、華人為福音"
		}
	}
	c, issues := historicalCandidate(t, s, candidateIssue().Metadata)
	if historicalSlotCount(c.Document) != 4 || hasCoverFinding(issues, "fixed_template_source_mismatch") {
		t.Fatal("reviewed native variant lost")
	}
	found := false
	for _, slot := range c.Document.LayoutManifest.Pages[0].FixedSlots {
		if slot.Element == "historicalGoals" {
			t.Fatal("old wording substituted")
		}
		found = found || slot.Element == "historicalGospelGoals"
	}
	if !found {
		t.Fatal("native gospel wording missing")
	}
}

func TestHistoricalV7PreservesV6SourceAndAnchors(t *testing.T) {
	source := historicalNativeGeometry()
	manifest := validDocument(4).LayoutManifest
	manifest.RendererVersion = "v6"
	previous, previousIssues, err := ApplyTemplateV1(source, candidateIssue(), manifest)
	if err != nil || previous.Document == nil {
		t.Fatal(err)
	}
	manifest.RendererVersion = "v7"
	next, nextIssues, err := ApplyTemplateV1(source, candidateIssue(), manifest)
	if err != nil || next.Document == nil {
		t.Fatal(err)
	}
	previous.Document.LayoutManifest.RendererVersion = "v7"
	previousIssues = slices.DeleteFunc(previousIssues, func(issue ReviewIssue) bool { return issue.Code == "unmapped_body_header" })
	if !reflect.DeepEqual(previous, next) || !reflect.DeepEqual(previousIssues, nextIssues) {
		t.Fatal("additive renderer contract changed source semantics")
	}
}

func TestHistoricalV8PreservesV7SourceAndFindings(t *testing.T) {
	manifest := validDocument(4).LayoutManifest
	manifest.RendererVersion = "v7"
	previous, previousIssues, err := ApplyTemplateV1(historicalNativeGeometry(), candidateIssue(), manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.RendererVersion = "v8"
	next, nextIssues, err := ApplyTemplateV1(historicalNativeGeometry(), candidateIssue(), manifest)
	if err != nil {
		t.Fatal(err)
	}
	previous.Document.LayoutManifest.RendererVersion = "v8"
	if !reflect.DeepEqual(previous, next) || !reflect.DeepEqual(previousIssues, nextIssues) {
		t.Fatal("V8 composition contract changed source semantics or findings")
	}
}

func TestHistoricalV7PreservesLocatedCoverFindings(t *testing.T) {
	for _, name := range []string{"canonical-mismatch", "outlined-rejection"} {
		t.Run(name, func(t *testing.T) {
			source, canonical := historicalNativeGeometry(), candidateIssue()
			if name == "canonical-mismatch" {
				canonical.Metadata.Title = "待校對的主題"
			} else {
				source = outlinedCoverGeometry(false)
				source.Pages[0].Images[0].Box.X += .05
			}
			manifest := validDocument(4).LayoutManifest
			var previous []ReviewIssue
			for _, version := range []string{"v6", "v7", "v8"} {
				manifest.RendererVersion = version
				candidate, issues, err := ApplyTemplateV1(source, canonical, manifest)
				if err != nil || candidate.Document == nil {
					t.Fatal(err)
				}
				if version == "v6" {
					previous = slices.DeleteFunc(issues, func(issue ReviewIssue) bool { return issue.Code == "unmapped_body_header" })
				} else if !reflect.DeepEqual(previous, issues) {
					t.Fatal("V7 changed located/deduplicated cover findings")
				}
			}
		})
	}
}

func TestHistoricalProfileDoesNotChangeDynamicAnchorsOrLegacyOutput(t *testing.T) {
	s := historicalNativeGeometry()
	manifest := validDocument(4).LayoutManifest
	manifest.RendererVersion = "v5"
	legacy, _, err := ApplyTemplateV1(s, candidateIssue(), manifest)
	if err != nil || legacy.Document == nil {
		t.Fatal(err)
	}
	next, _ := historicalCandidate(t, s, candidateIssue().Metadata)
	if !reflect.DeepEqual(legacy.Document.Components, next.Document.Components) || !reflect.DeepEqual(legacy.Document.Pages, next.Document.Pages) || !reflect.DeepEqual(legacy.Evidence, next.Evidence) {
		t.Fatal("dynamic identity or source evidence changed")
	}
	if historicalSlotCount(legacy.Document) != 0 {
		t.Fatal("legacy producer gained new fixed elements")
	}
	for i := range legacy.Document.LayoutManifest.Pages {
		if !reflect.DeepEqual(legacy.Document.LayoutManifest.Pages[i].Slots, next.Document.LayoutManifest.Pages[i].Slots) {
			t.Fatal("sentence/page anchors changed")
		}
	}
}

func historicalNativeGeometry() GeometryDocument {
	source := candidateGeometry()
	for i := range source.Pages[0].Texts {
		if source.Pages[0].Texts[i].Text == "第1739期" {
			source.Pages[0].Texts[i].Box.X = .23
		}
	}
	for i, row := range historicalCoverTestRows {
		part := GeometryText{Text: row.text, FontFamily: "MingLiU", FontSize: 10, Box: Box{X: .422 + float64(i)*.0336, Y: .1465 + float64(i)*.019, Width: .17 + float64(i)*.077, Height: .012}}
		if i == 3 {
			part.Text, part.Box.Width = "四個堅持：宣教主導、靈恩神學、團隊事奉、", .336
			source.Pages[0].Texts = append(source.Pages[0].Texts,
				GeometryText{Text: "門", FontFamily: "DFKaiShu", FontSize: 10, Box: Box{X: part.Box.X + .336, Y: part.Box.Y, Width: .017, Height: .012}},
				GeometryText{Text: "徒訓練", FontFamily: "MingLiU", FontSize: 10, Box: Box{X: part.Box.X + .353, Y: part.Box.Y, Width: .05, Height: .012}})
		}
		source.Pages[0].Texts = append(source.Pages[0].Texts, part)
	}
	for _, field := range []struct {
		text        string
		x, y, width float64
	}{{"主任牧師：曾英欽牧師", .096, .208, .21}, {"本週靈糧主題", .077, .245, .142}} {
		source.Pages[0].Texts = append(source.Pages[0].Texts, GeometryText{Text: field.text, FontFamily: "MingLiU", FontSize: 12, Box: Box{X: field.x, Y: field.y, Width: field.width, Height: .016}})
	}
	source.Pages[0].Images = []GeometryImage{
		{SHA256: "2b1f2ed34df0edd7f3217a97312ce42190c923742ac7eb5ab56a7afb3b582161", Box: Box{X: .128166, Y: .035633, Width: .157226, Height: .111174}},
		{SHA256: "a00d0e18ecaa51e26fe18e88d5550ca4a16091729a8573b267b04a9bc7e47803", Box: Box{X: .345696, Y: .079247, Width: .429853, Height: .019456}},
	}
	return source
}

func historicalCandidate(t *testing.T, source GeometryDocument, metadata CanonicalMetadata) (DocumentCandidate, []ReviewIssue) {
	t.Helper()
	manifest := validDocument(4).LayoutManifest
	manifest.RendererVersion = "v6"
	canonical := candidateIssue()
	canonical.Metadata = metadata
	candidate, issues, err := ApplyTemplateV1(source, canonical, manifest)
	if err != nil || candidate.Document == nil {
		t.Fatalf("V6 historical candidate: %v", err)
	}
	return candidate, issues
}

func hasCoverFinding(issues []ReviewIssue, code string) bool {
	for _, issue := range issues {
		if issue.Code == code && issue.ComponentID == "cover" && issue.SourcePage == 1 && issue.Blocking {
			return true
		}
	}
	return false
}

func historicalSlotCount(d *Document) int {
	count := 0
	for _, slot := range d.LayoutManifest.Pages[0].FixedSlots {
		if strings.HasPrefix(slot.Element, "historical") {
			count++
		}
	}
	return count
}

func TestHistoricalNativeCoverPreservesFourRows(t *testing.T) {
	source := historicalNativeGeometry()
	before, _ := json.Marshal(source)
	// Source order is not reading order; one label also uses multiple native font runs.
	for i, j := 0, len(source.Pages[0].Texts)-1; i < j; i, j = i+1, j-1 {
		source.Pages[0].Texts[i], source.Pages[0].Texts[j] = source.Pages[0].Texts[j], source.Pages[0].Texts[i]
	}
	candidate, issues := historicalCandidate(t, source, candidateIssue().Metadata)
	if historicalSlotCount(candidate.Document) != 4 {
		t.Fatal("four independent rows missing")
	}
	for _, slot := range candidate.Document.LayoutManifest.Pages[0].FixedSlots {
		if strings.HasPrefix(slot.Element, "vision") {
			t.Fatal("current wording substituted")
		}
		if strings.HasPrefix(slot.Element, "historical") && (slot.Box.X < .42 || slot.Box.Y < .14 || slot.Style.FontSize != 10) {
			t.Fatal("native row geometry changed")
		}
	}
	if hasCoverFinding(issues, "fixed_template_source_mismatch") {
		t.Fatal("recognized native header still missing")
	}
	after, _ := json.Marshal(candidate.Evidence)
	if !strings.Contains(string(after), historicalCoverTestRows[0].text) || !strings.Contains(string(before), "DROP_") {
		t.Fatal("source evidence lost")
	}
	for _, change := range []string{"punctuation", "mixed", "duplicate", "duplicate-outside-band", "extra", "extra-below-band", "extra-above-band", "moved"} {
		t.Run(change, func(t *testing.T) {
			s := historicalNativeGeometry()
			for i := range s.Pages[0].Texts {
				if s.Pages[0].Texts[i].Text != historicalCoverTestRows[0].text {
					continue
				}
				switch change {
				case "punctuation":
					s.Pages[0].Texts[i].Text = "一個異象:合一與宣教"
				case "mixed":
					s.Pages[0].Texts[i].Text = "異象使命：華人遍地興起、福音傳到地極"
				case "duplicate":
					s.Pages[0].Texts = append(s.Pages[0].Texts, s.Pages[0].Texts[i])
				case "duplicate-outside-band":
					extra := s.Pages[0].Texts[i]
					extra.Box.X, extra.Box.Y = .1, .1
					s.Pages[0].Texts = append(s.Pages[0].Texts, extra)
				case "extra":
					extra := s.Pages[0].Texts[i]
					extra.Text, extra.Box.Y = "五個目標：UNKNOWN", .224
					s.Pages[0].Texts = append(s.Pages[0].Texts, extra)
				case "extra-below-band", "extra-above-band":
					extra := s.Pages[0].Texts[i]
					extra.Text, extra.Box.X, extra.Box.Y = "五個目標：UNKNOWN", .65, .24
					if change == "extra-above-band" {
						extra.Box.Y = .13
					}
					s.Pages[0].Texts = append(s.Pages[0].Texts, extra)
				case "moved":
					s.Pages[0].Texts[i].Box.X = .1
				}
				break
			}
			c, findings := historicalCandidate(t, s, candidateIssue().Metadata)
			if historicalSlotCount(c.Document) != 0 || !hasCoverFinding(findings, "fixed_template_source_mismatch") {
				t.Fatal("partial/unknown header acquired reviewed profile")
			}
		})
	}
}

// This opt-in test uses private originals but logs only structural counts.
func TestPrivateHistoricalCoverProfile(t *testing.T) {
	path, snapshot := os.Getenv("HHC_PRIVATE_PDF_SOURCE"), os.Getenv("HHC_PRIVATE_CANONICAL_METADATA")
	if path == "" || snapshot == "" {
		t.Skip("private original and canonical metadata not supplied")
	}
	var metadata CanonicalMetadata
	if json.Unmarshal([]byte(snapshot), &metadata) != nil {
		t.Fatal("invalid private canonical snapshot")
	}
	class := map[int]string{1731: "outlined", 1732: "native", 1733: "outlined", 1734: "native", 1735: "native", 1736: "native", 1737: "outlined", 1738: "native", 1739: "current", 1740: "current", 1741: "current"}[metadata.IssueNumber]
	if class == "" {
		t.Fatal("source has no independent acceptance expectation")
	}
	geometry, err := ParsePDF(path)
	if err != nil {
		t.Fatal(err)
	}
	proposal, err := historicalCoverHeader(geometry.Pages[0], metadata)
	if err != nil {
		t.Fatal(err)
	}
	if class == "current" {
		if proposal != nil {
			t.Fatal("modern header acquired historical wording")
		}
	} else {
		if proposal == nil {
			t.Fatal("historical header unrecognized")
		}
		rows, variant := 0, false
		for _, slot := range proposal.fixed {
			if historicalFixedElement(slot.Element) {
				rows++
			}
			variant = variant || slot.Element == "historicalGospelGoals"
		}
		if rows != 4 || variant != (metadata.IssueNumber == 1738) {
			t.Fatalf("historical rows=%d variant=%v", rows, variant)
		}
		if class == "outlined" {
			if len(proposal.recognizedImages) != len(geometry.Pages[0].Images) || !hasCoverFinding(proposal.issues, "unmapped_fixed_template") {
				t.Fatal("outlined identity/inference boundary lost")
			}
		} else if len(proposal.issues) != 0 || len(proposal.recognizedImages) != 0 {
			t.Fatal("native text lost its source proof")
		}
	}
	canonical, manifest := candidateIssue(), validDocument(len(geometry.Pages)).LayoutManifest
	canonical.Metadata, manifest.RendererVersion = metadata, "v5"
	legacy, _, err := ApplyTemplateV1(geometry, canonical, manifest)
	if err != nil || legacy.Document == nil {
		t.Fatal("legacy candidate unavailable")
	}
	manifest.RendererVersion = "v6"
	next, findings, err := ApplyTemplateV1(geometry, canonical, manifest)
	if err != nil || next.Document == nil {
		t.Fatalf("V6 candidate unavailable: %v", err)
	}
	if !reflect.DeepEqual(legacy.Document.Components, next.Document.Components) || !reflect.DeepEqual(legacy.Document.Pages, next.Document.Pages) || !reflect.DeepEqual(legacy.Evidence, next.Evidence) {
		t.Fatal("private source content/anchors changed")
	}
	for i := range legacy.Document.LayoutManifest.Pages {
		if !reflect.DeepEqual(legacy.Document.LayoutManifest.Pages[i].Slots, next.Document.LayoutManifest.Pages[i].Slots) {
			t.Fatal("private source dynamic geometry changed")
		}
	}
	for _, issue := range findings {
		if issue.Code == "unknown_source_image" && issue.SourcePage == 1 {
			t.Fatal("reviewed source cover image unrecognized")
		}
	}
	t.Logf("issue=%d class=%s pages=%d fixed=%d findings=%d dynamic_anchors_unchanged=true", metadata.IssueNumber, class, len(next.Document.Pages), len(next.Document.LayoutManifest.Pages[0].FixedSlots), len(findings))
}

func outlinedCoverGeometry(alternate bool) GeometryDocument {
	s := candidateGeometry()
	s.Pages[0].Width, s.Pages[0].Height = 612, 792
	kept := s.Pages[0].Texts[:0]
	for _, part := range s.Pages[0].Texts {
		if part.Box.Y >= .28 {
			kept = append(kept, part)
		}
	}
	s.Pages[0].Texts = kept
	s.Pages[0].Texts = append(s.Pages[0].Texts,
		GeometryText{Text: "門", FontFamily: "DFKaiShu-SB-Estd-BF", FontSize: 9, Box: Box{X: .828039208, Y: .203439169, Width: .015269975, Height: .011787727}},
		GeometryText{Text: "～", FontFamily: "DFKaiShu-SB-Estd-BF", FontSize: 11, Box: Box{X: .518632471, Y: .254967542, Width: .018397464, Height: .014228821}})
	logo, masthead := "7ffe7e623170a51415b8628853df420c05a76f33dbe70ae159b275bad9cc530f", "c046969d906573ed942217ea8bc55eaa6d9f4d5795c8c9bd4575dd7a9744fb17"
	if alternate {
		logo, masthead = "84e00570648d78276704c48b41e50dae11d536683537d9f70f88a2eed3c865cf", "13190cfb73aa3d33ada276cc5b37571f4b95b8763e9a82bb3c84528ea9c0329c"
	}
	s.Pages[0].Images = []GeometryImage{
		{SHA256: logo, Width: 392, Height: 392, Box: Box{X: .159215685, Y: .035454548, Width: .144509802, Height: .111666665}},
		{SHA256: masthead, Width: 1042, Height: 67, Box: Box{X: .358431386, Y: .095151514, Width: .384117650, Height: .019090909}},
	}
	return s
}

func currentOutlinedCoverGeometry() GeometryDocument {
	s := outlinedCoverGeometry(true)
	s.Pages[0].Images[1] = GeometryImage{SHA256: "fd9ce69e74ef5f57b9cc948e25a9fb0d86c05b64204bb6126352f1c085bab096", Width: 1068, Height: 69, Box: Box{X: .358431386, Y: .079242422, Width: .393725489, Height: .019696968}}
	s.Pages[0].Texts[len(s.Pages[0].Texts)-2].Box.X = .818431405
	s.Pages[0].Texts[len(s.Pages[0].Texts)-2].Box.Y = .194348255
	s.Pages[0].Texts[len(s.Pages[0].Texts)-1].Box.X = .609218395
	return s
}

func TestCurrentOutlinedCoverKeepsThreeRowsReviewable(t *testing.T) {
	s := currentOutlinedCoverGeometry()
	c, issues := historicalCandidate(t, s, candidateIssue().Metadata)
	if historicalSlotCount(c.Document) != 0 || !hasCoverFinding(issues, "unmapped_fixed_template") || !hasCoverFinding(issues, "fixed_template_text_variant") {
		t.Fatal("current outlined header must not acquire historical wording or lose review")
	}
	for _, element := range []string{"visionMission", "visionFellowship", "visionCommitment", "title", "subtitle", "date", "issueNumber"} {
		count := 0
		for _, slot := range c.Document.LayoutManifest.Pages[0].FixedSlots {
			if slot.Element == element {
				count++
				if (element == "title" || element == "subtitle") && slot.Style.FontSize != 14 {
					t.Fatal("current compact theme inherited the historical oversized title")
				}
			}
		}
		if count != 1 {
			t.Fatalf("%s: want one current slot, got %d", element, count)
		}
	}
	for _, change := range []string{"moved", "duplicate", "unknown", "native-conflict"} {
		t.Run(change, func(t *testing.T) {
			s := currentOutlinedCoverGeometry()
			switch change {
			case "moved":
				s.Pages[0].Images[1].Box.Y += .02
			case "duplicate":
				s.Pages[0].Images = append(s.Pages[0].Images, s.Pages[0].Images[1])
			case "unknown":
				s.Pages[0].Images[1].SHA256 = strings.Repeat("f", 64)
			case "native-conflict":
				s.Pages[0].Texts = append(s.Pages[0].Texts, GeometryText{Text: "UNKNOWN", FontFamily: "MingLiU", FontSize: 9, Box: Box{X: .4, Y: .15, Width: .1, Height: .02}})
			}
			proposal, err := outlinedHistoricalCover(s.Pages[0], candidateIssue().Metadata)
			if err != nil || proposal == nil || len(proposal.fixed) != 0 || !hasCoverFinding(proposal.issues, "fixed_template_source_mismatch") {
				t.Fatal("unproven current header accepted", err)
			}
		})
	}
}

func TestOutlinedCoverUsesWordingBandIndependentlyOfMasthead(t *testing.T) {
	for _, current := range []bool{false, true} {
		for _, modernMasthead := range []bool{false, true} {
			source := outlinedCoverGeometry(true)
			if current {
				source = currentOutlinedCoverGeometry()
			}
			if modernMasthead {
				source.Pages[0].Images[1] = currentOutlinedCoverGeometry().Pages[0].Images[1]
			} else {
				source.Pages[0].Images[1] = outlinedCoverGeometry(true).Pages[0].Images[1]
			}
			proposal, err := outlinedHistoricalCover(source.Pages[0], candidateIssue().Metadata)
			if err != nil || proposal == nil || len(proposal.fixed) == 0 {
				t.Fatal("known template parts rejected", err)
			}
			foundCurrent := false
			for _, slot := range proposal.fixed {
				foundCurrent = foundCurrent || slot.Element == "visionMission"
			}
			if foundCurrent != current {
				t.Fatal("masthead chose wording instead of the source band")
			}
		}
	}
}

func TestCurrentOutlinedCoverChecksNativeParentheses(t *testing.T) {
	for _, change := range []string{"valid", "unpaired", "duplicate", "canonical-conflict", "font", "position"} {
		t.Run(change, func(t *testing.T) {
			s := currentOutlinedCoverGeometry()
			metadata := candidateIssue().Metadata
			metadata.Title += "(補充)"
			parts := []GeometryText{
				{Text: "(", FontFamily: "TimesNewRomanPSMT", FontSize: 13, Box: Box{X: .485098034, Y: .251543673, Width: .007167825, Height: .018412681}},
				{Text: ") ", FontFamily: "TimesNewRomanPSMT", FontSize: 13, Box: Box{X: .604117636, Y: .251543673, Width: .00839475, Height: .018412681}},
			}
			switch change {
			case "unpaired":
				parts = parts[:1]
			case "duplicate":
				parts = append(parts, parts[0])
			case "canonical-conflict":
				metadata = candidateIssue().Metadata
			case "font":
				parts[0].FontFamily = "Unknown"
			case "position":
				parts[0].Box.Y += .02
			}
			s.Pages[0].Texts = append(s.Pages[0].Texts, parts...)
			proposal, err := outlinedHistoricalCover(s.Pages[0], metadata)
			if err != nil || proposal == nil || (len(proposal.fixed) > 0) != (change == "valid") {
				t.Fatal("native glyph validation disagrees with source", err)
			}
		})
	}
}

func TestCurrentOutlinedCoverChecksRepeatedCanonicalPunctuation(t *testing.T) {
	for _, change := range []string{"valid", "canonical-conflict", "duplicate", "font", "position", "separator"} {
		t.Run(change, func(t *testing.T) {
			source := currentOutlinedCoverGeometry()
			source.Pages[0].Images = outlinedCoverGeometry(false).Pages[0].Images
			page := &source.Pages[0]
			page.Texts[len(page.Texts)-1].Box.X = .414895163
			metadata := candidateIssue().Metadata
			metadata.Title, metadata.Subtitle = "主題!", "副標!內容!"
			parts := []GeometryText{
				{Text: "!", FontFamily: "TimesNewRomanPSMT", FontSize: 17, Box: Box{X: .409411748, Y: .247363652, Width: .009189534, Height: .023606040}},
				{Text: "!", FontFamily: "TimesNewRomanPSMT", FontSize: 13, Box: Box{X: .580980376, Y: .251543673, Width: .007167825, Height: .018412681}},
				{Text: "!", FontFamily: "TimesNewRomanPSMT", FontSize: 13, Box: Box{X: .798235276, Y: .251543673, Width: .007167825, Height: .018412681}},
			}
			switch change {
			case "canonical-conflict":
				metadata.Subtitle = "副標!內容"
			case "duplicate":
				parts[2] = parts[1]
			case "font":
				parts[0].FontFamily = "Unknown"
			case "position":
				parts[0].Box.Y += .02
			case "separator":
				page.Texts[len(page.Texts)-1].Box.X = .29
			}
			page.Texts = append(page.Texts, parts...)
			proposal, err := outlinedHistoricalCover(*page, metadata)
			if err != nil || proposal == nil || (len(proposal.fixed) > 0) != (change == "valid") {
				t.Fatal("native punctuation must match canonical counts and reviewed source geometry", err)
			}
		})
	}
}

func TestOutlinedCoverRequiresReviewedAnchors(t *testing.T) {
	for _, alternate := range []bool{false, true} {
		s := outlinedCoverGeometry(alternate)
		c, issues := historicalCandidate(t, s, candidateIssue().Metadata)
		if historicalSlotCount(c.Document) != 4 || !hasCoverFinding(issues, "unmapped_fixed_template") {
			t.Fatal("outlined profile/proposal missing")
		}
		want := map[string]bool{"logo": false, "masthead": false, "title": false, "subtitle": false, "date": false, "issueNumber": false, "pastor": false, "titleLabel": false}
		for _, slot := range c.Document.LayoutManifest.Pages[0].FixedSlots {
			if _, ok := want[slot.Element]; ok {
				want[slot.Element] = true
			}
		}
		for element, found := range want {
			if !found {
				t.Fatal("missing outlined field", element)
			}
		}
		for _, issue := range issues {
			if issue.Code == "unknown_source_image" && issue.SourcePage == 1 {
				t.Fatal("whole-profile anchors unrecognized")
			}
		}
	}
	for _, change := range []string{"unknown", "moved", "duplicate", "pair", "native-conflict", "missing-glyph", "duplicate-glyph", "size"} {
		t.Run(change, func(t *testing.T) {
			s := outlinedCoverGeometry(false)
			switch change {
			case "unknown":
				s.Pages[0].Images[0].SHA256 = strings.Repeat("f", 64)
			case "moved":
				s.Pages[0].Images[0].Box.X += .02
			case "duplicate":
				s.Pages[0].Images = append(s.Pages[0].Images, s.Pages[0].Images[0])
			case "pair":
				s.Pages[0].Images[1].SHA256 = "13190cfb73aa3d33ada276cc5b37571f4b95b8763e9a82bb3c84528ea9c0329c"
			case "native-conflict":
				s.Pages[0].Texts = append(s.Pages[0].Texts, GeometryText{Text: "異象使命：UNKNOWN", FontFamily: "MingLiU", FontSize: 10, Box: Box{X: .4, Y: .16, Width: .3, Height: .015}})
			case "missing-glyph":
				s.Pages[0].Texts = s.Pages[0].Texts[:len(s.Pages[0].Texts)-1]
			case "duplicate-glyph":
				s.Pages[0].Texts = append(s.Pages[0].Texts, s.Pages[0].Texts[len(s.Pages[0].Texts)-1])
			case "size":
				s.Pages[0].Width, s.Pages[0].Height = templateV1Width, templateV1Height
			}
			c, issues := historicalCandidate(t, s, candidateIssue().Metadata)
			if historicalSlotCount(c.Document) != 0 || !hasCoverFinding(issues, "fixed_template_source_mismatch") {
				t.Fatal("invalid anchor acquired historical wording")
			}
			unknown := false
			for _, issue := range issues {
				unknown = unknown || issue.Code == "unknown_source_image" && issue.SourcePage == 1
			}
			if !unknown {
				t.Fatal("unrecognized source images suppressed")
			}
		})
	}
}

func TestOutlinedCanonicalFieldsRemainReviewProposals(t *testing.T) {
	s := outlinedCoverGeometry(false)
	original := candidateIssue().Metadata
	_, issues := historicalCandidate(t, s, original)
	if !hasCoverFinding(issues, "unmapped_fixed_template") {
		t.Fatal("inferred canonical fields not reviewable")
	}
	metadata := original
	metadata.Subtitle = ""
	c, issues := historicalCandidate(t, s, metadata)
	for _, slot := range c.Document.LayoutManifest.Pages[0].FixedSlots {
		if slot.Element == "subtitle" {
			t.Fatal("absent subtitle invented")
		}
	}
	if !hasCoverFinding(issues, "unmapped_fixed_template") || !reflect.DeepEqual(original, candidateIssue().Metadata) {
		t.Fatal("canonical source/authority changed")
	}
	s = historicalNativeGeometry()
	metadata = original
	metadata.Title = "DIFFERENT"
	_, issues = historicalCandidate(t, s, metadata)
	found := false
	for _, issue := range issues {
		found = found || issue.Code == "canonical_source_mismatch" && issue.Blocking
	}
	if !found {
		t.Fatal("native disagreement silently inferred")
	}
}

func TestCurrentOutlinedChurchSlotsMeetCompositorMinimum(t *testing.T) {
	source := currentOutlinedCoverGeometry()
	proposal, err := outlinedHistoricalCover(source.Pages[0], candidateIssue().Metadata)
	if err != nil || proposal == nil {
		t.Fatal("cover", err)
	}
	count := 0
	for _, slot := range proposal.fixed {
		switch slot.Element {
		case "visionMission", "visionFellowship", "visionCommitment":
			count++
			if slot.Style.FontSize < 9 || slot.Box.Height*source.Pages[0].Height < slot.Style.LineHeight {
				t.Fatal("invalid church text placement")
			}
		}
	}
	if count != 3 {
		t.Fatalf("got %d church slots", count)
	}
}
