package onlinebulletins

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode"
)

func TestParseGeometryXMLPreservesPointBoxesAndInlineText(t *testing.T) {
	xml := `<pdf2xml><page number="1" width="595" height="841"><fontspec id="0" size="18" family="DFKaiShu" color="#000000"/><text top="100" left="60" width="140" height="24" font="0">經文<b>強調</b>。</text></page><page number="2" width="612" height="792"><text top="20" left="40" width="100" height="24" font="0">KEEP_正文</text></page></pdf2xml>`
	pages := []Page{{ID: "p1", Width: 595.32, Height: 841.92}, {ID: "p2", Width: 612, Height: 792}}
	geometry, err := parseGeometryXML(strings.NewReader(xml), pages)
	if err != nil {
		t.Fatal(err)
	}
	if len(geometry.Pages) != 2 || geometry.Pages[0].Width != 595.32 || geometry.Pages[1].Height != 792 {
		t.Fatal("source page dimensions lost")
	}
	text := geometry.Pages[0].Texts[0]
	if text.Text != "經文強調。" || text.FontFamily != "DFKaiShu" || text.FontSize != 18 || math.Abs(text.Box.X-60/595.32) > 1e-15 || math.Abs(text.Box.Height-24/841.92) > 1e-15 {
		t.Fatalf("text=%+v", text)
	}
}

func TestParseGeometryXMLRejectsMalformedOrUnmappableGeometry(t *testing.T) {
	base := `<pdf2xml><page number="1" width="595" height="841"><fontspec id="0" size="16" family="MingLiU"/><text top="100" left="60" width="140" height="24" font="0">文字。</text></page></pdf2xml>`
	for _, bad := range []string{
		strings.Replace(base, `width="140"`, `width="0"`, 1),
		strings.Replace(base, `top="100"`, `top="NaN"`, 1),
		strings.Replace(base, `font="0"`, `font="missing"`, 1),
		strings.Replace(base, `number="1"`, `number="2"`, 1),
		strings.Replace(base, `width="595"`, `width="300"`, 1),
		strings.Replace(base, "文字。", "\uFFFD", 1),
		strings.Replace(base, "文字。", `<script>hidden</script>`, 1),
		base + `<pdf2xml/>`,
		`<!DOCTYPE pdf2xml [<!ENTITY leak SYSTEM "file:///etc/passwd">]>` + base,
	} {
		if _, err := parseGeometryXML(strings.NewReader(bad), []Page{{ID: "p1", Width: 595.32, Height: 841.92}}); !errors.Is(err, ErrInvalidGeometry) {
			t.Fatalf("malformed geometry accepted: %v", err)
		}
	}
}

func TestHymnReadingOrderIsColumnMajorOnlyOnPenultimatePage(t *testing.T) {
	texts := []GeometryText{{Text: "right-top", Box: Box{X: .6, Y: .1}}, {Text: "left-bottom", Box: Box{X: .1, Y: .8}}, {Text: "left-top", Box: Box{X: .1, Y: .1}}}
	orderGeometryText(texts, true)
	if texts[0].Text != "left-top" || texts[1].Text != "left-bottom" || texts[2].Text != "right-top" {
		t.Fatal("hymn columns interleaved")
	}
	orderGeometryText(texts, false)
	if texts[1].Text != "right-top" {
		t.Fatal("body reading order used columns")
	}
}

func TestEmptyWordProcessorRunsDoNotBecomeTextGeometry(t *testing.T) {
	xml := `<pdf2xml><page number="1" width="595" height="841"><fontspec id="0" size="16" family="MingLiU"/><text top="100" left="60" width="0" height="24" font="0"> </text><text top="100" left="1128" width="800" height="24" font="0"> </text><text top="100" left="60" width="140" height="24" font="0">文字。</text></page></pdf2xml>`
	geometry, err := parseGeometryXML(strings.NewReader(xml), []Page{{ID: "p1", Width: 595.32, Height: 841.92}})
	if err != nil || len(geometry.Pages[0].Texts) != 1 {
		t.Fatalf("empty runs retained/rejected: %v", err)
	}
}

func TestExcludedBackOverflowDoesNotRejectReadableContent(t *testing.T) {
	for _, tc := range []struct {
		name, anchor, top string
		accepted          bool
	}{
		{"excluded", "主日敬拜服事表", "663", true},
		{"no anchor", "其他內容", "663", false},
		{"retained prose", "主日敬拜服事表", "300", false},
		{"crosses boundary", "主日敬拜服事表", "460", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			xml := `<pdf2xml>`
			pages := []Page{}
			for i := 1; i <= 4; i++ {
				pages = append(pages, Page{Width: 595.32, Height: 841.92})
				xml += fmt.Sprintf(`<page number="%d" width="595" height="841"><fontspec id="0" size="12" family="MingLiU"/><text top="100" left="50" width="100" height="16" font="0">保留內容</text>`, i)
				if i == 4 {
					xml += `<text top="468" left="50" width="100" height="16" font="0">` + tc.anchor + `</text><text top="` + tc.top + `" left="275" width="460" height="16" font="0">不得保留的溢出內容</text>`
				}
				xml += `</page>`
			}
			geometry, err := parseGeometryXML(strings.NewReader(xml+`</pdf2xml>`), pages)
			if tc.accepted {
				if err != nil || len(geometry.Pages[3].Texts) != 2 {
					t.Fatalf("excluded overflow: %v", err)
				}
			} else if !errors.Is(err, ErrInvalidGeometry) {
				t.Fatalf("retained invalid geometry accepted: %v", err)
			}
		})
	}
}

func TestLetterBackOverflowUsesPaperRelativeServiceTableBand(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		anchorTop, overflowTop float64
		accepted               bool
	}{
		{"shorter paper", 429.112811, 708.685635, true},
		{"anchor too high", 390, 708.685635, false},
		{"anchor too low", 510, 708.685635, false},
		{"retained prose", 429.112811, 410, false},
		{"crossing anchor", 429.112811, 425, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var xml strings.Builder
			xml.WriteString(`<pdf2xml>`)
			pages := make([]Page, 4)
			for i := range pages {
				pages[i] = Page{Width: 612, Height: 792}
				fmt.Fprintf(&xml, `<page number="%d" width="612" height="792"><fontspec id="0" size="12" family="MingLiU"/><text top="100" left="50" width="100" height="16" font="0">保留內容</text>`, i+1)
				if i == 3 {
					fmt.Fprintf(&xml, `<text top="%f" left="168" width="82" height="12" font="0">主日敬拜服事表</text><text top="%f" left="275" width="418" height="8" font="0">DROP_overflow</text>`, tc.anchorTop, tc.overflowTop)
				}
				xml.WriteString(`</page>`)
			}
			xml.WriteString(`</pdf2xml>`)
			geometry, err := parseGeometryXML(strings.NewReader(xml.String()), pages)
			if tc.accepted {
				if err != nil || len(geometry.Pages[3].Texts) != 2 {
					t.Fatalf("valid Letter back rejected: %v", err)
				}
			} else if !errors.Is(err, ErrInvalidGeometry) {
				t.Fatalf("unproven excluded overflow accepted: %v", err)
			}
		})
	}
}

func TestParsePDFControlledFixtures(t *testing.T) {
	version, err := exec.Command("pdftohtml", "-v").CombinedOutput()
	if err != nil || !strings.Contains(string(version), "pdftohtml version "+PopplerVersion+"\n") {
		if os.Getenv("HHC_REQUIRE_POPPLER") == "1" {
			t.Fatal("pinned Poppler required")
		}
		t.Skip("pinned Poppler verified in the isolated extractor image")
	}
	for _, fixture := range []struct {
		name  string
		pages int
	}{{"1739-sanitized.pdf", 12}, {"1740-sanitized.pdf", 16}} {
		t.Run(fixture.name, func(t *testing.T) {
			geometry, err := ParsePDF(filepath.Join("testdata", fixture.name))
			if err != nil {
				t.Fatal(err)
			}
			if len(geometry.Pages) != fixture.pages || len(geometry.SourceSHA256) != 64 || geometry.PopplerVersion != PopplerVersion {
				t.Fatal("source evidence mismatch")
			}
			for index, page := range geometry.Pages {
				if page.Number != index+1 || page.Width != 595.32 || page.Height != 841.92 || len(page.Texts) == 0 {
					t.Fatal("missing source geometry")
				}
				for _, image := range page.Images {
					if len(image.SHA256) != 64 || image.Width < 1 || image.Height < 1 || image.source != "" {
						t.Fatal("invalid image evidence")
					}
				}
			}
		})
	}
}

func TestPDFPreflightRejectsEncryptedActiveAndOverLimitSources(t *testing.T) {
	info := "Pages: 12\nEncrypted: no\nJavaScript: no\nForm: none\n"
	if count, err := supportedPDFInfo([]byte(info)); err != nil || count != 12 {
		t.Fatal("valid preflight rejected")
	}
	for _, bad := range []string{strings.Replace(info, "Encrypted: no", "Encrypted: yes", 1), strings.Replace(info, "JavaScript: no", "JavaScript: yes", 1), strings.Replace(info, "Form: none", "Form: AcroForm", 1), strings.Replace(info, "Pages: 12", "Pages: 41", 1)} {
		if _, err := supportedPDFInfo([]byte(bad)); err == nil {
			t.Fatal("unsupported source accepted")
		}
	}
	if _, err := sourcePDFPages([]byte("Page 1 size: 595.32 x 841.92 pts\n"), 2); err == nil {
		t.Fatal("missing page accepted")
	}
}

func TestParserOutputLimitDoesNotRetainDiagnostics(t *testing.T) {
	output := &limitedProcessOutput{}
	if _, err := output.Write(make([]byte, (64<<10)+1)); !errors.Is(err, ErrGeometryLimits) || output.Len() != 0 {
		t.Fatal("unbounded diagnostic output retained")
	}
	directory := t.TempDir()
	file, err := os.Create(filepath.Join(directory, "large-output"))
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate((100 << 20) + 1); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(boundedOutputDirectory(directory), ErrGeometryLimits) {
		t.Fatal("temporary output exceeded cap")
	}
}

// Opt-in local preflight: never logs original text or paths and commits no source PDF.
func TestPrivatePDFPreflight(t *testing.T) {
	path := os.Getenv("HHC_PRIVATE_PDF_SOURCE")
	if path == "" {
		t.Skip("private source not supplied")
	}
	geometry, err := ParsePDF(path)
	if err != nil {
		t.Fatal(err)
	}
	allowed, issues, err := filterTemplateV1Geometry(geometry)
	if err != nil || len(allowed.Pages[len(allowed.Pages)-1].Texts) == 0 {
		t.Fatalf("back anchor/filter failed: %v %v", issues, err)
	}
	for index, page := range allowed.Pages {
		for _, part := range page.Texts {
			for _, point := range part.Text {
				if point == '\uf0ab' || point == '\uf0ac' || (point >= '\uf08c' && point <= '\uf090') {
					t.Logf("symbol_font=%q code_point=U+%04X", part.FontFamily, point)
				}
			}
		}
		lines, err := sourceLines(page, index == len(allowed.Pages)-2)
		if err != nil {
			t.Fatalf("source line grouping page=%d: %v", page.Number, err)
		}
		parts := 0
		for _, line := range lines {
			parts += len(line.parts)
		}
		if parts != len(page.Texts) {
			t.Fatal("source line grouping lost source runs")
		}
	}
	for _, page := range geometry.Pages {
		t.Logf("page=%d text_boxes=%d image_boxes=%d", page.Number, len(page.Texts), len(page.Images))
		for _, image := range page.Images {
			t.Logf("image page=%d box=%+v pixels=%dx%d sha256=%s", page.Number, image.Box, image.Width, image.Height, image.SHA256)
		}
	}
}

func TestPrivateCoverLabelGeometry(t *testing.T) {
	path := os.Getenv("HHC_PRIVATE_PDF_SOURCE")
	if path == "" {
		t.Skip("private source not supplied")
	}
	geometry, err := ParsePDF(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"Welcome", "Worship", "Work", "Word", "本週", "金句"} {
		found := false
		for _, part := range geometry.Pages[0].Texts {
			if (label == "本週" || label == "金句") && part.Box.Y < .6 {
				continue
			}
			index := strings.Index(part.Text, label)
			if index < 0 {
				continue
			}
			found = true
			suffix := strings.TrimSpace(part.Text[index+len(label):])
			standalone := suffix == ":" || suffix == "：" || suffix == ""
			t.Logf("template_label=%s standalone_run=%t font=%s size=%g box=%+v", label, standalone, part.FontFamily, part.FontSize, part.Box)
		}
		if !found {
			t.Fatal("cover template anchor missing")
		}
	}
}

func TestPrivateCoverComponents(t *testing.T) {
	path := os.Getenv("HHC_PRIVATE_PDF_SOURCE")
	if path == "" {
		t.Skip("private source not supplied")
	}
	geometry, err := ParsePDF(path)
	if err != nil {
		t.Fatal(err)
	}
	component, slots, fixed, issues, err := coverComponent(geometry.Pages[0])
	if err != nil {
		t.Fatalf("cover mapping failed: %v", err)
	}
	cover := component.Cover
	t.Logf("cover worship=%d work=%d questions=%d slots=%d fixed=%d review_findings=%d", len(cover.Worship), len(cover.Work), len(cover.WordQuestions), len(slots), len(fixed), len(issues))
	if len(cover.Worship) < 3 || len(cover.Worship) > 5 || len(cover.Work) != 3 || len(cover.WordQuestions) < 6 || len(fixed) != 5 || len(slots) == 0 {
		t.Fatal("reference cover component cardinality lost")
	}
	for _, block := range cover.WeeklyVerses {
		for _, sentence := range block.Sentences {
			for _, span := range sentence.Spans {
				if span.FontRole != "scripture" && span.FontRole != "symbol" {
					t.Fatal("weekly verse lost scripture role")
				}
			}
		}
	}
	var verse strings.Builder
	for _, block := range cover.WeeklyVerses {
		verse.WriteString(privateBlockText(block))
	}
	if !verseReferenceEnd.MatchString(strings.Join(strings.Fields(verse.String()), "")) {
		t.Fatal("weekly verse lost its closing source citation")
	}
	sourceBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// This checksum-bound glyph was visually verified inside the outlined fixed verse label.
	outlinedVerseLabel := fmt.Sprintf("%x", sha256.Sum256(sourceBytes)) == "dee2e5837f65779a69523904a563286a0653d936b9ec1100767fc0b360a2675b"
	fixedLabelResidues := 0
	verifiedSource := geometry.Pages[0]
	if outlinedVerseLabel {
		verifiedSource.Texts = nil
		for _, part := range geometry.Pages[0].Texts {
			box := part.Box
			if strings.TrimSpace(part.Text) == "。" && box.X >= .10 && box.X+box.Width <= .13 && box.Y >= .835 && box.Y+box.Height <= .85 {
				fixedLabelResidues++
				continue
			}
			verifiedSource.Texts = append(verifiedSource.Texts, part)
		}
	}
	lines, err := sourceLines(verifiedSource, false)
	if err != nil {
		t.Fatal(err)
	}
	label := regexp.MustCompile(`^[一二三四]、(Welcome|Worship|Work|Word)[：:]?`)
	var sourceText, resultText strings.Builder
	started, footer := false, false
	for _, line := range lines {
		text := strings.Join(strings.Fields(lineText(line)), "")
		if strings.HasPrefix(text, "一、Welcome") {
			started = true
		}
		if !started {
			continue
		}
		if strings.HasPrefix(text, "教會地址") {
			footer = true
			break
		}
		sourceText.WriteString(strings.ReplaceAll(label.ReplaceAllString(text, ""), "本週金句", ""))
	}
	if outlinedVerseLabel && fixedLabelResidues != 1 {
		t.Fatalf("verified outlined-label residue changed: count=%d", fixedLabelResidues)
	}
	t.Logf("source fixed-label residues=%d", fixedLabelResidues)
	for _, block := range componentBlocks(&component) {
		resultText.WriteString(privateBlockText(*block))
	}
	want := privateSourceSymbols.Replace(sourceText.String())
	got := privateSourceSymbols.Replace(strings.Join(strings.Fields(resultText.String()), ""))
	if !started || !footer || want != got {
		wantRunes, gotRunes := []rune(want), []rune(got)
		for index := 0; index < len(wantRunes) && index < len(gotRunes); index++ {
			if wantRunes[index] != gotRunes[index] {
				t.Logf("first differing scalar=%d source=U+%04X result=U+%04X", index, wantRunes[index], gotRunes[index])
				break
			}
		}
		t.Fatalf("cover dynamic text differs: anchors=%t/%t source=%d result=%d scalars", started, footer, len([]rune(want)), len([]rune(got)))
	}
}

func TestPrivateBodyTextConservation(t *testing.T) {
	path := os.Getenv("HHC_PRIVATE_PDF_SOURCE")
	if path == "" {
		t.Skip("private source not supplied")
	}
	geometry, err := ParsePDF(path)
	if err != nil {
		t.Fatal(err)
	}
	pages := append([]GeometryPage(nil), geometry.Pages[1:len(geometry.Pages)-2]...)
	tail, lyrics, err := splitHymnPage(geometry.Pages[len(geometry.Pages)-2])
	if err != nil {
		t.Fatal(err)
	}
	if tail != nil {
		pages = append(pages, *tail)
	}
	mapping, err := bodyComponents(pages)
	if err != nil {
		t.Fatal(err)
	}
	var source, result strings.Builder
	for _, page := range pages {
		lines, err := sourceLines(page, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range lines {
			box := textBounds(line.parts)
			if box.Y*page.Height < 65 || page.Number == 2 && box.Y < 272/templateV1Height {
				continue
			}
			source.WriteString(lineText(line))
		}
	}
	for _, component := range mapping.components {
		body := component.BodySection
		result.WriteString(privateBlockText(body.Title))
		for _, contributor := range body.Contributors {
			result.WriteString(privateBlockText(contributor.Name))
		}
		for _, block := range body.Blocks {
			result.WriteString(privateBlockText(block))
		}
	}
	// Whitespace is reflowed; speaker separators are represented as fixed slots.
	normalize := func(value string) []rune {
		// Source Wingdings numerals use private-use codepoints, not Unicode numerals.
		value = privateSourceSymbols.Replace(value)
		return []rune(strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) || strings.ContainsRune("~～〜", r) {
				return -1
			}
			return r
		}, value))
	}
	want, got := normalize(source.String()), normalize(result.String())
	if string(want) != string(got) {
		index := 0
		for index < len(want) && index < len(got) && want[index] == got[index] {
			index++
		}
		if index < len(want) && index < len(got) {
			t.Logf("different codepoints source=U+%04X result=U+%04X", want[index], got[index])
		}
		t.Fatalf("body text mismatch at scalar=%d source=%d result=%d", index, len(want), len(got))
	}
	t.Logf("body text conserved: %d nonspacing scalars", len(got))
	hymns, _, _, _, err := hymnComponent(lyrics)
	if err != nil {
		t.Fatal(err)
	}
	lines, err := sourceLines(lyrics, true)
	if err != nil {
		t.Fatal(err)
	}
	source.Reset()
	result.Reset()
	sourceSongs := map[rune]*strings.Builder{}
	var song rune
	for _, line := range lines {
		if textBounds(line.parts).Y < .07 || strings.TrimSpace(lineText(line)) == "小組詩歌" {
			continue
		}
		text := strings.TrimSpace(lineText(line))
		points := []rune(text)
		if len(points) > 2 && points[0] >= '1' && points[0] <= '9' && strings.ContainsRune(".．、", points[1]) {
			song = points[0]
			if sourceSongs[song] != nil {
				t.Fatal("duplicate source song number")
			}
			sourceSongs[song] = &strings.Builder{}
		}
		if sourceSongs[song] == nil {
			t.Fatal("lyrics without a source song number")
		}
		sourceSongs[song].WriteString(text)
	}
	// Two-column PDF placement can be 1,3,2,4; the reading order is numbered songs.
	for number := 0; number < len(sourceSongs); number++ {
		text := sourceSongs['1'+rune(number)]
		if text == nil {
			t.Fatal("source song numbering has a gap")
		}
		source.WriteString(text.String())
	}
	for _, hymn := range hymns.HymnLyrics.Hymns {
		result.WriteString(privateBlockText(hymn.Title))
		for _, section := range hymn.Sections {
			for _, line := range section.Lines {
				result.WriteString(privateBlockText(line))
			}
		}
	}
	want, got = normalize(source.String()), normalize(result.String())
	if string(want) != string(got) {
		t.Fatalf("hymn text/order differs: source=%d result=%d", len(want), len(got))
	}
	t.Logf("hymn text conserved: %d nonspacing scalars", len(got))
}

var privateSourceSymbols = strings.NewReplacer("\uf08c", "➊", "\uf08d", "➋", "\uf08e", "➌", "\uf08f", "➍", "\uf090", "➎", "\uf091", "➏", "\uf092", "➐", "\uf093", "➑", "\uf094", "➒", "\uf095", "➓", "\uf0ab", "★", "\uf0ac", "🟋", "\uf0ad", "🟏")

func TestPrivateBackPrivacyBoundary(t *testing.T) {
	path := os.Getenv("HHC_PRIVATE_PDF_SOURCE")
	if path == "" {
		t.Skip("private source not supplied")
	}
	geometry, err := ParsePDF(path)
	if err != nil {
		t.Fatal(err)
	}
	last := len(geometry.Pages) - 1
	if _, _, _, _, err := backComponents(geometry.Pages[last]); err == nil {
		t.Fatal("unfiltered source was accepted")
	}
	allowed, _, err := filterTemplateV1Geometry(geometry)
	if err != nil {
		t.Fatal(err)
	}
	components, _, _, _, err := backComponents(allowed.Pages[last])
	if err != nil || len(components) != 3 {
		t.Fatalf("retained back sections missing: %v", err)
	}
	for index, kind := range []string{"backSummary", "announcements", "victoriesAndPrayers"} {
		if components[index].Type != kind || len(components[index].Items) == 0 {
			t.Fatal("retained section empty or reordered")
		}
	}
	lines, err := sourceLines(allowed.Pages[last], false)
	if err != nil {
		t.Fatal(err)
	}
	var sourceText, resultText strings.Builder
	announcementsStarted := false
	for _, line := range lines {
		label := strings.Join(strings.Fields(lineText(line)), "")
		if label == "家教會公佈欄" || label == "家教會公布欄" {
			announcementsStarted = true
			continue
		}
		if label == "得勝與代求" {
			continue
		}
		for _, part := range line.parts {
			// The rotated summary sidebar is fixed template text, not editable content.
			if !announcementsStarted && part.Box.X < .165 {
				continue
			}
			sourceText.WriteString(part.Text)
		}
	}
	for _, component := range components {
		for _, item := range component.Items {
			for _, block := range item.Blocks {
				resultText.WriteString(privateBlockText(block))
			}
		}
	}
	nonspacing := func(value string) string {
		return strings.Join(strings.Fields(privateSourceSymbols.Replace(value)), "")
	}
	if nonspacing(sourceText.String()) != nonspacing(resultText.String()) {
		want, got := []rune(nonspacing(sourceText.String())), []rune(nonspacing(resultText.String()))
		for index := 0; index < len(want) && index < len(got); index++ {
			if want[index] != got[index] {
				t.Logf("first differing scalar=%d source=U+%04X result=U+%04X", index, want[index], got[index])
				break
			}
		}
		t.Fatalf("retained back text differs: source=%d result=%d scalars", len([]rune(nonspacing(sourceText.String()))), len([]rune(nonspacing(resultText.String()))))
	}
	// Inject canaries below the verified exclusion boundary without altering the PDF.
	page := &geometry.Pages[last]
	page.Texts = append(page.Texts, GeometryText{Text: "PRIVATE_CANARY", Box: Box{X: .2, Y: .9, Width: .2, Height: .01}})
	page.Images = append(page.Images, GeometryImage{Box: Box{X: .2, Y: .9, Width: .2, Height: .01}, SHA256: strings.Repeat("f", 64)})
	filtered, _, err := filterTemplateV1Geometry(geometry)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(filtered)
	if err != nil || strings.Contains(string(raw), "PRIVATE_CANARY") || strings.Contains(string(raw), strings.Repeat("f", 64)) {
		t.Fatal("excluded text/image canary escaped")
	}
	t.Logf("retained back sections: summary=%d announcements=%d prayers=%d", len(components[0].Items), len(components[1].Items), len(components[2].Items))
}

func TestPrivateSemanticPageBoundary(t *testing.T) {
	path := os.Getenv("HHC_PRIVATE_PDF_SOURCE")
	if path == "" {
		t.Skip("private source not supplied")
	}
	geometry, err := ParsePDF(path)
	if err != nil {
		t.Fatal(err)
	}
	pages := geometry.Pages
	tail, lyrics, err := splitHymnPage(pages[len(pages)-2])
	if err != nil {
		t.Fatal(err)
	}
	bodyPages := append([]GeometryPage(nil), pages[1:len(pages)-2]...)
	if tail != nil {
		bodyPages = append(bodyPages, *tail)
	}
	body, err := bodyComponents(bodyPages)
	if err != nil {
		t.Fatalf("body: %v", err)
	}
	hymns, _, _, _, err := hymnComponent(lyrics)
	if err != nil {
		t.Fatalf("hymns: %v", err)
	}
	t.Logf("body_sections=%d hymns=%d mixed_page=%t", len(body.components), len(hymns.HymnLyrics.Hymns), tail != nil)
}

func TestPrivateBodyFramesResolveEveryBoxedTitle(t *testing.T) {
	path := os.Getenv("HHC_PRIVATE_PDF_SOURCE")
	if path == "" {
		t.Skip("private source not supplied")
	}
	geometry, err := ParsePDF(path)
	if err != nil {
		t.Fatal(err)
	}
	allowed, _, err := filterTemplateV1Geometry(geometry)
	if err != nil {
		t.Fatal(err)
	}
	pages := allowed.Pages[1 : len(allowed.Pages)-2]
	count := 0
	for _, page := range pages {
		lines, err := sourceLines(page, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, frame := range page.Frames {
			if frame.Element != "bodyHeadingFrame" {
				continue
			}
			count++
			found := false
			for _, line := range lines {
				_, _, match := framedBodyHeading(line, []GeometryFrame{frame})
				found = found || match
			}
			if !found {
				t.Errorf("unresolved frame page=%d box=%+v", page.Number, frame.Box)
				for _, line := range lines {
					box := textBounds(line.parts)
					if box.Y < frame.Box.Y+frame.Box.Height && box.Y+box.Height > frame.Box.Y {
						t.Logf("nearby runs: %+v", line.parts)
					}
				}
			}
		}
	}
	mapping, err := bodyComponents(pages)
	if err != nil {
		t.Fatal(err)
	}
	if len(mapping.components) != count {
		t.Errorf("frames=%d sections=%d", count, len(mapping.components))
	}
	t.Logf("frames=%d sections=%d", count, len(mapping.components))
	for _, issue := range mapping.issues {
		if issue.BlockID == "" {
			continue
		}
		found := false
		for _, component := range mapping.components {
			if component.ID != issue.ComponentID {
				continue
			}
			for _, block := range componentBlocks(&component) {
				if block.ID != issue.BlockID {
					continue
				}
				found = issue.SentenceID == ""
				for _, sentence := range block.Sentences {
					found = found || sentence.ID == issue.SentenceID
				}
			}
		}
		if !found || issue.SourcePage < 0 || issue.SourcePage > len(geometry.Pages) {
			t.Fatalf("invalid finding locator: %#v", issue)
		}
	}
	if reference := os.Getenv("HHC_PRIVATE_HEADING_AUDIT"); reference != "" {
		var reports []struct {
			Path   string `json:"path"`
			Frames []struct {
				Title string `json:"title"`
				Tail  string `json:"tail"`
			} `json:"frames"`
		}
		data, err := os.ReadFile(reference)
		if err != nil || json.Unmarshal(data, &reports) != nil {
			t.Fatal("invalid private heading reference")
		}
		normalize := func(value string) string { return strings.Join(strings.Fields(value), "") }
		matched := false
		for _, report := range reports {
			if filepath.Base(report.Path) != filepath.Base(path) {
				continue
			}
			matched = true
			index := 0
			for _, frame := range report.Frames {
				if normalize(frame.Title) == "小組詩歌" {
					continue
				}
				if index >= len(mapping.components) {
					t.Fatal("source title missing from result")
				}
				body := mapping.components[index].BodySection
				speaker := ""
				if len(body.Contributors) == 1 {
					speaker = privateBlockText(body.Contributors[0].Name)
				}
				if normalize(privateBlockText(body.Title)) != normalize(frame.Title) || normalize(speaker) != strings.TrimLeft(normalize(frame.Tail), "～〜~") {
					t.Errorf("section %d title/speaker differs: got %q / %q; want %q / %q", index+1, privateBlockText(body.Title), speaker, frame.Title, frame.Tail)
				}
				index++
			}
			if index != len(mapping.components) {
				t.Fatal("extra section outside source reference")
			}
		}
		if !matched {
			t.Fatal("source reference missing")
		}
	}
}

func TestPrivateBackComponents(t *testing.T) {
	path := os.Getenv("HHC_PRIVATE_PDF_SOURCE")
	if path == "" {
		t.Skip("private source not supplied")
	}
	geometry, err := ParsePDF(path)
	if err != nil {
		t.Fatal(err)
	}
	allowed, _, err := filterTemplateV1Geometry(geometry)
	if err != nil {
		t.Fatal(err)
	}
	components, _, _, _, err := backComponents(allowed.Pages[len(allowed.Pages)-1])
	if err != nil {
		t.Fatalf("retained back mapping failed: %v", err)
	}
	lines, err := sourceLines(allowed.Pages[len(allowed.Pages)-1], false)
	if err != nil {
		t.Fatal(err)
	}
	counts, section := [2]int{}, -1
	numberedItem := regexp.MustCompile(`^[1-9][0-9]*[.．]`)
	for _, line := range lines {
		text := strings.Join(strings.Fields(lineText(line)), "")
		switch text {
		case "家教會公佈欄", "家教會公布欄":
			section = 0
		case "得勝與代求":
			section = 1
		default:
			if section >= 0 && numberedItem.MatchString(text) {
				counts[section]++
			}
		}
	}
	if counts[0] == 0 || counts[1] == 0 || len(components) != 3 || len(components[0].Items) != 1 || len(components[1].Items) != counts[0] || len(components[2].Items) != counts[1] {
		t.Fatal("retained back component cardinality lost")
	}
}

func TestPrivateHymnComponents(t *testing.T) {
	path := os.Getenv("HHC_PRIVATE_PDF_SOURCE")
	if path == "" {
		t.Skip("private source not supplied")
	}
	geometry, err := ParsePDF(path)
	if err != nil {
		t.Fatal(err)
	}
	component, _, fixed, _, err := hymnComponent(geometry.Pages[len(geometry.Pages)-2])
	if err != nil {
		t.Fatalf("hymn mapping failed: %v", err)
	}
	want := 5
	if len(geometry.Pages) == 16 {
		want = 4
	}
	if len(component.HymnLyrics.Hymns) != want {
		t.Fatal("source hymn count lost")
	}
	pageNumberFound := false
	for _, slot := range fixed {
		if slot.Element == "pageNumber" {
			pageNumberFound = true
			if slot.sourcePage != len(geometry.Pages)-1 {
				t.Fatal("source hymn page number assigned to another page")
			}
		}
	}
	if !pageNumberFound {
		t.Fatal("source hymn page number missing")
	}
	counts := map[string]int{}
	sourceLines, err := sourceLines(geometry.Pages[len(geometry.Pages)-2], true)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range sourceLines {
		if textBounds(line.parts).Y >= .07 && strings.TrimSpace(lineText(line)) != "小組詩歌" {
			var normalized strings.Builder
			for _, part := range line.parts {
				text := part.Text
				if strings.Contains(strings.ToLower(part.FontFamily), "wingdings") {
					text = strings.NewReplacer("\uf0ab", "★", "\uf0ac", "\U0001f7cb").Replace(text)
				}
				normalized.WriteString(text)
			}
			counts[normalized.String()]++
		}
	}
	for _, hymn := range component.HymnLyrics.Hymns {
		counts[privateBlockText(hymn.Title)]--
		for _, section := range hymn.Sections {
			for _, line := range section.Lines {
				counts[privateBlockText(line)]--
			}
		}
	}
	for _, count := range counts {
		if count != 0 {
			t.Fatal("source hymn line lost, duplicated or changed")
		}
	}
}

func TestPrivateBodyComponents(t *testing.T) {
	path := os.Getenv("HHC_PRIVATE_PDF_SOURCE")
	if path == "" {
		t.Skip("private source not supplied")
	}
	geometry, err := ParsePDF(path)
	if err != nil {
		t.Fatal(err)
	}
	mapping, err := bodyComponents(geometry.Pages[1 : len(geometry.Pages)-2])
	if err != nil {
		t.Fatalf("body mapping failed: %v", err)
	}
	components, slots, header, issues := mapping.components, mapping.slots, mapping.header, mapping.issues
	numbers := 0
	for _, slot := range mapping.fixed {
		if slot.Element == "pageNumber" {
			numbers++
		}
	}
	if numbers != len(geometry.Pages)-3 {
		t.Fatalf("source body page numbers: got=%d want=%d", numbers, len(geometry.Pages)-3)
	}
	if len(components) < 2 || len(slots) == 0 || len(header) == 0 || len(issues) == 0 {
		t.Fatal("body text/header/review evidence lost")
	}
	var expected, actual strings.Builder
	for _, page := range geometry.Pages[1 : len(geometry.Pages)-2] {
		lines, err := sourceLines(page, false)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range lines {
			top := textBounds(line.parts).Y * page.Height
			if top >= 65 && (page.Number != 2 || top >= 272) {
				for _, part := range line.parts {
					text := part.Text
					if isWingdingsFont(part.FontFamily) {
						text = strings.NewReplacer("\uf08c", "➊", "\uf08d", "➋", "\uf08e", "➌", "\uf08f", "➍", "\uf090", "➎", "\uf0ab", "★", "\uf0ac", "\U0001f7cb").Replace(text)
					}
					expected.WriteString(text)
				}
			}
		}
	}
	for _, component := range components {
		actual.WriteString(privateBlockText(component.BodySection.Title))
		for _, contributor := range component.BodySection.Contributors {
			actual.WriteString("～")
			actual.WriteString(privateBlockText(contributor.Name))
		}
		for _, block := range component.BodySection.Blocks {
			actual.WriteString(privateBlockText(block))
		}
	}
	if expected.String() != actual.String() {
		t.Fatal("source body text lost, duplicated or changed")
	}
	t.Logf("body components=%d slots=%d header_lines=%d review_findings=%d", len(components), len(slots), len(header), len(issues))
}

func privateBlockText(block Block) string {
	var text strings.Builder
	for _, sentence := range block.Sentences {
		for _, span := range sentence.Spans {
			text.WriteString(span.Text)
		}
	}
	return text.String()
}

func TestPrivateComposedLayoutSubmission(t *testing.T) {
	path := os.Getenv("HHC_PRIVATE_COMPOSED_LAYOUT")
	if path == "" {
		t.Skip("private composed layout not supplied")
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > 8*1024*1024 {
		t.Fatal("private composed layout unavailable")
	}
	var submission struct {
		Document Document `json:"document"`
	}
	if json.Unmarshal(data, &submission) != nil {
		t.Fatal("invalid private composed layout")
	}
	if _, err := ValidateDocument(submission.Document); err != nil {
		t.Fatal(err)
	}
}

func TestPrivateTemplateCandidate(t *testing.T) {
	path, snapshot := os.Getenv("HHC_PRIVATE_PDF_SOURCE"), os.Getenv("HHC_PRIVATE_CANONICAL_METADATA")
	if path == "" || snapshot == "" {
		t.Skip("private source and canonical snapshot not supplied")
	}
	var metadata CanonicalMetadata
	if json.Unmarshal([]byte(snapshot), &metadata) != nil {
		t.Fatal("invalid private canonical snapshot")
	}
	geometry, err := ParsePDF(path)
	if err != nil {
		t.Fatal(err)
	}
	canonical := candidateIssue()
	canonical.Metadata = metadata
	manifest := validDocument(len(geometry.Pages)).LayoutManifest
	output := os.Getenv("HHC_PRIVATE_LAYOUT_SUBMISSION")
	if output != "" {
		manifest.Assets = approvedLocalTemplateAssets(t, os.Getenv("HHC_TEMPLATE_ASSET_DIRECTORY"))
		manifest.RendererArtifactSha256 = os.Getenv("HHC_PRIVATE_RENDERER_DIGEST")
	}
	candidate, issues, err := ApplyTemplateV1(geometry, canonical, manifest)
	if err != nil || candidate.Document == nil {
		t.Fatalf("private source candidate failed: %v issues=%v", err, issues)
	}
	if _, err := ValidateDocument(*candidate.Document); err != nil {
		t.Fatal(err)
	}
	var header *BodyHeader
	for _, component := range candidate.Document.Components {
		if component.BodySection != nil && component.BodySection.Header != nil {
			if header != nil {
				t.Fatal("private source header duplicated across body sections")
			}
			header = component.BodySection.Header
		}
	}
	if header == nil || len(header.Contributors) != 3 {
		t.Fatal("private source header missing from composed candidate")
	}
	if len(issues) == 0 {
		t.Fatal("unresolved source profile incorrectly accepted as ready")
	}
	canonicalArt := 0
	for _, slot := range candidate.Document.LayoutManifest.Pages[1].FixedSlots {
		if slot.Element == "title" || slot.Element == "subtitle" {
			canonicalArt++
		}
	}
	if canonicalArt != 2 {
		t.Fatal("private source native canonical WordArt proposal missing")
	}
	for _, part := range candidate.Evidence.Pages[len(candidate.Evidence.Pages)-1].Texts {
		if (part.Box.Y+part.Box.Height)*templateV1Height > templateV1BackContentBottom {
			t.Fatal("private back crossed candidate boundary")
		}
	}
	if output != "" && writePrivateLayoutSubmission(output, *candidate.Document, metadata) != nil {
		t.Fatal("private layout submission failed")
	}
	t.Logf("candidate pages=%d components=%d blocking_findings=%d", len(candidate.Document.Pages), len(candidate.Document.Components), len(issues))
}

func TestPrivateCoverCanonicalMetadata(t *testing.T) {
	path := os.Getenv("HHC_PRIVATE_PDF_SOURCE")
	if path == "" {
		t.Skip("private source not supplied")
	}
	geometry, err := ParsePDF(path)
	if err != nil {
		t.Fatal(err)
	}
	var metadata CanonicalMetadata
	if raw := os.Getenv("HHC_PRIVATE_CANONICAL_METADATA"); raw == "" {
		t.Skip("private canonical snapshot not supplied")
	} else if json.Unmarshal([]byte(raw), &metadata) != nil {
		t.Fatal("invalid private canonical snapshot")
	}
	slots, issues, err := coverMetadataSlots(geometry.Pages[0], metadata)
	for _, slot := range slots {
		t.Logf("canonical_slot=%s", slot.Element)
		if slot.Element == "title" && (slot.Style.LetterSpacing >= 0 || slot.Style.LetterSpacing < -.5) {
			t.Fatal("compressed reference title lost source tracking")
		}
	}
	if err != nil || len(slots) != 4 || len(issues) != 0 {
		t.Fatalf("source/canonical metadata mapping: slots=%d issues=%d err=%v", len(slots), len(issues), err)
	}
}

func TestPrivateCoverChurchProfile(t *testing.T) {
	path := os.Getenv("HHC_PRIVATE_PDF_SOURCE")
	if path == "" {
		t.Skip("private source not supplied")
	}
	geometry, err := ParsePDF(path)
	if err != nil {
		t.Fatal(err)
	}
	slots, issues, err := coverChurchSlots(geometry.Pages[0])
	if err != nil || len(slots) != 5 || len(issues) != 0 {
		t.Fatalf("source fixed church rows: slots=%d issues=%d err=%v", len(slots), len(issues), err)
	}
}

func TestPrivateCoverFooterProfile(t *testing.T) {
	path := os.Getenv("HHC_PRIVATE_PDF_SOURCE")
	if path == "" {
		t.Skip("private source not supplied")
	}
	geometry, err := ParsePDF(path)
	if err != nil {
		t.Fatal(err)
	}
	slots, issues, err := coverFooterSlots(geometry.Pages[0])
	for _, slot := range slots {
		t.Logf("fixed_footer_slot=%s", slot.Element)
	}
	if err != nil || len(slots) != 9 || len(issues) != 0 {
		t.Fatalf("source fixed footer: slots=%d findings=%d err=%v", len(slots), len(issues), err)
	}
}

func TestPrivateCoverNativeMasthead(t *testing.T) {
	path := os.Getenv("HHC_PRIVATE_PDF_SOURCE")
	if path == "" {
		t.Skip("private source not supplied")
	}
	geometry, err := ParsePDF(path)
	if err != nil {
		t.Fatal(err)
	}
	slot, issues := coverMastheadSlot(geometry.Pages[0])
	if slot == nil || len(issues) != 0 {
		t.Fatal("reviewed native cover masthead anchor not found")
	}
}

func TestPrivateBodyHeaderProfile(t *testing.T) {
	path := os.Getenv("HHC_PRIVATE_PDF_SOURCE")
	if path == "" {
		t.Skip("private source not supplied")
	}
	geometry, err := ParsePDF(path)
	if err != nil {
		t.Fatal(err)
	}
	body, err := bodyComponents(geometry.Pages[1 : len(geometry.Pages)-2])
	if err != nil {
		t.Fatal(err)
	}
	metadata := CanonicalMetadata{IssueNumber: 1739, Date: "2026-09-20"}
	if len(geometry.Pages) == 16 {
		metadata.IssueNumber = 1740
		metadata.Date = "2026-09-27"
	}
	header, err := bodyHeaderComponent(body.header, metadata, len(geometry.Pages), body.components[0].ID)
	if err != nil || header.header == nil || len(header.slots) != 4 || len(header.fixed) != 5 || len(header.issues) == 0 {
		t.Fatalf("body header proposals: slots=%d fixed=%d findings=%v err=%v", len(header.slots), len(header.fixed), header.issues, err)
	}
	date, err := time.Parse("Jan.2.2006", privateBlockText(header.header.LectureDate))
	if err != nil || date.Format("2006-01-02") == metadata.Date {
		t.Fatal("source lecture date lost or replaced by canonical issue date")
	}
}

func TestPrivateBackSidebarProfile(t *testing.T) {
	path := os.Getenv("HHC_PRIVATE_PDF_SOURCE")
	if path == "" {
		t.Skip("private source not supplied")
	}
	geometry, err := ParsePDF(path)
	if err != nil {
		t.Fatal(err)
	}
	allowed, _, err := filterTemplateV1Geometry(geometry)
	if err != nil {
		t.Fatal(err)
	}
	slots, issues := backSidebarSlots(allowed.Pages[len(allowed.Pages)-1])
	if len(slots) != 2 || len(issues) != 0 {
		t.Fatalf("fixed sidebar mapping: slots=%d findings=%v", len(slots), issues)
	}
}

func TestPrivateBackNativeFrames(t *testing.T) {
	path := os.Getenv("HHC_PRIVATE_PDF_SOURCE")
	if path == "" {
		t.Skip("private source not supplied")
	}
	source, err := ParsePDF(path)
	if err != nil {
		t.Fatal(err)
	}
	allowed, _, err := filterTemplateV1Geometry(source)
	if err != nil {
		t.Fatal(err)
	}
	page := allowed.Pages[len(allowed.Pages)-1]
	slots, issues := backFrameSlots(page)
	if len(slots) != 3 || len(issues) != 0 {
		t.Fatal("private native frames missing or ambiguous")
	}
	wanted := []Box{{X: 75.75, Y: 11.25, Width: 486.4, Height: 174}, {X: 12.75, Y: 207.75, Width: 560.8, Height: 161.3}, {X: 18.75, Y: 370.5, Width: 559.45, Height: 90.15}}
	if len(source.Pages) == 16 {
		wanted[0].Height = 187.2
		wanted[1].Y, wanted[1].Height = 215.35, 171.55
		wanted[2].Y, wanted[2].Height = 390.6, 71.75
	}
	for i, slot := range slots {
		got := Box{X: slot.Box.X * page.Width, Y: slot.Box.Y * page.Height, Width: slot.Box.Width * page.Width, Height: slot.Box.Height * page.Height}
		want := wanted[i]
		if math.Abs(got.X-want.X) > .02 || math.Abs(got.Y-want.Y) > .02 || math.Abs(got.Width-want.Width) > .02 || math.Abs(got.Height-want.Height) > .02 {
			t.Fatal("native source frame geometry changed")
		}
	}
}

func TestPrivateLayoutSubmissionIsExclusiveAndOwnerOnly(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "layout.json")
	if err := writePrivateLayoutSubmission(path, validDocument(4), candidateIssue().Metadata); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("private layout submission permissions changed")
	}
	if writePrivateLayoutSubmission(path, validDocument(4), candidateIssue().Metadata) == nil {
		t.Fatal("existing private output overwritten")
	}
	if err := os.Chmod(directory, 0755); err != nil {
		t.Fatal(err)
	}
	if writePrivateLayoutSubmission(filepath.Join(directory, "public.json"), validDocument(4), candidateIssue().Metadata) == nil {
		t.Fatal("private source content written into a public directory")
	}
}

// Opt-in local visual QA only. Export the privacy-filtered document/canonical
// snapshot, never raw evidence, source pixels, fonts or extraction logs.
func writePrivateLayoutSubmission(path string, document Document, metadata CanonicalMetadata) error {
	info, err := os.Stat(filepath.Dir(path))
	if err != nil || !filepath.IsAbs(path) || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return ErrUnsupportedSource
	}
	data, err := json.Marshal(struct {
		Document          Document          `json:"document"`
		CanonicalMetadata CanonicalMetadata `json:"canonicalMetadata"`
	}{document, metadata})
	if err != nil || len(data) > 8<<20 {
		return ErrGeometryLimits
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}
