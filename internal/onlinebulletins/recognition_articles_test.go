package onlinebulletins

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

func TestRecognitionRejectsUnownedBodyAndFusedAttribution(t *testing.T) {
	for _, scenario := range []string{"unowned", "fused_title", "fused_speaker", "late_speaker"} {
		t.Run(scenario, func(t *testing.T) {
			source, pages, results := recognitionDocumentFixture(t)
			for i := range results[1].Blocks {
				if strings.HasPrefix(results[1].Blocks[i].Role, "body_") {
					results[1].Blocks[i].Group = "article"
				}
			}
			switch scenario {
			case "unowned":
				for i := range results[1].Blocks {
					if results[1].Blocks[i].Role == "body_paragraph" {
						results[1].Blocks[i].Group = "another-article"
					}
				}
			case "fused_title":
				for i := range source.Pages[1].Texts {
					if source.Pages[1].Texts[i].Text == "主題" {
						source.Pages[1].Texts[i].Text = "主題～Ada"
					}
				}
				for i := range pages[1].Native {
					if pages[1].Native[i].Text == "主題" {
						pages[1].Native[i].Text = "主題～Ada"
					}
				}
				for i := range results[1].Blocks {
					if results[1].Blocks[i].Role == "body_title" {
						results[1].Blocks[i].Text = "主題～Ada"
					}
				}
			case "fused_speaker", "late_speaker":
				// Reusing the paragraph's exact source keeps complete native coverage.
				for i := range results[1].Blocks {
					if results[1].Blocks[i].Role == "body_paragraph" {
						if scenario == "fused_speaker" {
							results[1].Blocks[i].Role = "body_speaker"
							for n := range source.Pages[1].Texts {
								if strings.Contains(source.Pages[1].Texts[n].Text, "KEEP_body") {
									source.Pages[1].Texts[n].Text = "～Ada"
								}
							}
							for n := range pages[1].Native {
								if strings.Contains(pages[1].Native[n].Text, "KEEP_body") {
									pages[1].Native[n].Text = "～Ada"
								}
							}
							results[1].Blocks[i].Text = "～Ada"
						} else {
							// A second attribution after prose cannot silently attach to the previous article.
							b := results[1].Blocks[i]
							results[1].Blocks[i].Role = "body_paragraph"
							b.Role = "body_speaker"
							b.SourceIDs = []string{"p2-late"}
							b.Text = "Ada"
							part := source.Pages[1].Texts[len(source.Pages[1].Texts)-1]
							part.Text = "Ada"
							part.Box.Y = .8
							source.Pages[1].Texts = append(source.Pages[1].Texts, part)
							// Reprepare to preserve server-owned source IDs and ordering.
							next, err := PrepareRecognitionPages(source, recognitionRasters(t, 4))
							if err != nil {
								t.Fatal(err)
							}
							pages = next
							b.SourceIDs = []string{pages[1].Native[len(pages[1].Native)-1].ID}
							b.Box = part.Box
							results[1].Blocks = append(results[1].Blocks, b)
						}
						break
					}
				}
			}
			if _, _, err := AssembleRecognition(source, pages, results, candidateIssue(), ConversionTemplateSettings(), validDocument(4).LayoutManifest.Assets); err == nil {
				t.Fatal("semantically invalid source was accepted")
			}
		})
	}
}

func TestIndependentRecognitionDisagreementCannotAuthorizePublication(t *testing.T) {
	for _, agree := range []bool{false, true} {
		t.Run(fmt.Sprint(agree), func(t *testing.T) {
			input := recognitionFixture(t)
			input.Page = 2
			input.TotalPages = 4
			input.Native = nil
			for i, text := range []string{"恩典", "Ada", "正文一。", "信心", "Bob", "正文二。"} {
				input.Native = append(input.Native, RecognitionText{ID: fmt.Sprintf("p2-t%d", i+1), Text: text, Box: Box{X: .1, Y: .1 + float64(i)*.1, Width: .6, Height: .03}})
			}
			roles := []string{"body_title", "body_speaker", "body_paragraph", "body_title", "body_speaker", "body_paragraph"}
			proposal := RecognitionResult{Page: 2, Blocks: []RecognitionBlock{}, UnassignedIDs: []string{}}
			for i, role := range roles {
				kind := ""
				if role == "body_title" {
					kind = "testimony"
				}
				proposal.Blocks = append(proposal.Blocks, RecognitionBlock{Role: role, SectionKind: kind, Mode: "native", Group: fmt.Sprintf("article-%d", i/3), SourceIDs: []string{input.Native[i].ID}})
			}
			calls := 0
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				var payload map[string]any
				if json.NewDecoder(r.Body).Decode(&payload) != nil {
					t.Fatal("payload")
				}
				encoded, _ := json.Marshal(payload)
				if calls == 2 && bytes.Contains(encoded, []byte("previousProposal")) {
					t.Error("independent pass was anchored to the first proposal")
				}
				sourceData := payload["input"].([]any)[0].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
				var sent map[string]json.RawMessage
				if json.Unmarshal([]byte(strings.TrimPrefix(sourceData, "SOURCE DATA:\n")), &sent) != nil {
					t.Fatal("invalid source data")
				}
				if bytes.Contains(sent["native"], []byte(`"characters":`)) {
					t.Error("unnecessary per-glyph coordinates sent to provider")
				}
				row := proposal
				row.Blocks = slices.Clone(proposal.Blocks)
				if !agree && calls != 2 {
					row.Blocks[3].Role = "body_subtitle"
					row.Blocks[3].SectionKind = ""
					row.Blocks[4].Role = "body_paragraph"
					for i := 3; i < 6; i++ {
						row.Blocks[i].Group = "article-0"
					}
				}
				if agree && calls == 2 {
					for i := range row.Blocks {
						row.Blocks[i].Group = "renamed-" + row.Blocks[i].Group
					}
				}
				json.NewEncoder(w).Encode(recognitionResponse(row))
			}))
			defer server.Close()
			client, err := NewAzureExtractor(server.URL, "candidate", "key", "", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			result, usage, err := client.RecognizeVerifiedPage(context.Background(), input, nil)
			if agree {
				if err != nil || calls != 2 || !hashPattern.MatchString(result.SemanticSHA256) {
					t.Fatal("equivalent independent inventory did not verify", calls, err)
				}
			} else if err == nil || calls != 3 || result.SemanticSHA256 != "" || usage.Requests != 3 {
				t.Fatal("semantic disagreement published or exceeded bounded budget", calls, err)
			}
		})
	}
}

func TestRecognitionContinuationBindsPreviousVerifiedPage(t *testing.T) {
	_, pages, results := recognitionDocumentFixture(t)
	input, result := pages[1], results[1]
	result.SemanticSHA256, _ = recognitionSemanticSHA256(input, result)
	continuation, err := NextRecognitionContinuation(input, result)
	if err != nil {
		t.Fatal(err)
	}
	next := pages[2]
	next.Continuation = continuation
	next.Native = []RecognitionText{{ID: "p3-t1", Text: "承接正文。", Box: Box{X: .1, Y: .1, Width: .6, Height: .03}}}
	continued := RecognitionResult{Page: 3, Blocks: []RecognitionBlock{{Role: "body_paragraph", Mode: "native", Group: continuation.Group, SourceIDs: []string{"p3-t1"}, Text: "承接正文。", Box: next.Native[0].Box}}, UnassignedIDs: []string{}}
	original, ok := recognitionSemanticSHA256(next, continued)
	if !ok {
		t.Fatal("verified continuation rejected")
	}
	changed := *continuation
	changed.PreviousSHA256 = strings.Repeat("e", 64)
	next.Continuation = &changed
	modified, ok := recognitionSemanticSHA256(next, continued)
	if !ok || modified == original {
		t.Fatal("previous page repair did not invalidate dependent proof")
	}
	continued.Blocks[0].Group = "unknown-article"
	if _, ok = recognitionSemanticSHA256(next, continued); ok {
		t.Fatal("unowned cross-page content accepted")
	}
}

func TestRecognitionEvidenceRejectsLegacySemanticReceipt(t *testing.T) {
	document := validDocument(4)
	document.LayoutManifest.RendererVersion = "v11"
	document.LayoutManifest.RendererArtifactSha256 = RendererV11SHA256
	evidence := &RecognitionEvidence{ProtocolSHA256: RecognitionProtocolSHA256(), Deployment: RecognitionDeployment, ModelVersion: RecognitionModelVersion, SourceSHA256: document.SourceAssetChecksum}
	for i := 1; i <= 4; i++ {
		evidence.Pages = append(evidence.Pages, RecognitionPageEvidence{Page: i, InputSHA256: strings.Repeat("a", 64), SemanticSHA256: strings.Repeat("b", 64), Usage: RecognitionUsage{Model: RecognitionModelName, Requests: 2}})
	}
	if !ValidRecognitionEvidence(evidence, document) {
		t.Fatal("valid semantic proof rejected")
	}
	evidence.Pages[2].SemanticSHA256 = ""
	if ValidRecognitionEvidence(evidence, document) {
		t.Fatal("old geometry-only receipt accepted for publication")
	}
}

func TestSemanticReceiptDetectsOrderVisualOwnershipAndRetainedRoleChanges(t *testing.T) {
	for _, scenario := range []string{"native_order", "visual_owner", "omitted_hymn"} {
		t.Run(scenario, func(t *testing.T) {
			input := recognitionFixture(t)
			input.Page = 2
			input.TotalPages = 4
			input.Native = []RecognitionText{
				{ID: "p2-t1", Text: "恩典", Box: Box{X: .1, Y: .1, Width: .3, Height: .03}},
				{ID: "p2-t2", Text: "第一段。", Box: Box{X: .1, Y: .2, Width: .3, Height: .03}},
				{ID: "p2-t3", Text: "第二段。", Box: Box{X: .1, Y: .3, Width: .3, Height: .03}},
				{ID: "p2-t4", Text: "信心", Box: Box{X: .1, Y: .4, Width: .3, Height: .03}},
			}
			native := func(i int, role, group string) RecognitionBlock {
				p := input.Native[i]
				kind := ""
				if role == "body_title" {
					kind = "testimony"
				}
				return RecognitionBlock{Mode: "native", Role: role, Group: group, SectionKind: kind, Text: p.Text, Box: p.Box, SourceIDs: []string{p.ID}}
			}
			first := RecognitionResult{Page: 2, UnassignedIDs: []string{}, Blocks: []RecognitionBlock{native(0, "body_title", "A"), native(1, "body_paragraph", "A"), native(2, "body_paragraph", "A"), native(3, "body_title", "B")}}
			if scenario == "visual_owner" {
				first.Blocks = append(first.Blocks, RecognitionBlock{Mode: "visual", Role: "body_paragraph", Group: "A", Text: "獨立圖形正文。", SourceIDs: []string{}, Box: Box{X: .1, Y: .6, Width: .6, Height: .03}})
			}
			if scenario == "omitted_hymn" {
				first.Blocks[2].Role = "hymn_verse"
				first.Blocks[2].Group = "song"
			}
			second := first
			second.Blocks = slices.Clone(first.Blocks)
			switch scenario {
			case "native_order":
				second.Blocks[1], second.Blocks[2] = second.Blocks[2], second.Blocks[1]
			case "visual_owner":
				second.Blocks[len(second.Blocks)-1].Group = "B"
			case "omitted_hymn":
				second.Blocks[2].Role = "fixed_label"
			}
			if !validMaterializedRecognition(input, first) || !validMaterializedRecognition(input, second) {
				t.Fatal("fixture failed source validation")
			}
			a, ok := recognitionSemanticSHA256(input, first)
			if !ok {
				t.Fatal("first semantic result invalid")
			}
			b, ok := recognitionSemanticSHA256(input, second)
			if ok && a == b {
				t.Fatal("different emitted reading content received the same semantic receipt")
			}
		})
	}
}

func TestRecognitionSeparatorUsesExistingNativeComposerAnchor(t *testing.T) {
	source, _, results := recognitionDocumentFixture(t)
	for i := range source.Pages[1].Texts {
		part := &source.Pages[1].Texts[i]
		if part.Text != "主題" {
			continue
		}
		part.Text = "恩典～Ada"
		for n, r := range []rune(part.Text) {
			box := part.Box
			box.X += float64(n) * part.Box.Width / 6
			box.Width = part.Box.Width / 6
			source.Pages[1].Glyphs = append(source.Pages[1].Glyphs, GeometryGlyph{Text: string(r), Box: box})
		}
	}
	pages, err := PrepareRecognitionPages(source, recognitionRasters(t, 4))
	if err != nil {
		t.Fatal(err)
	}
	var blocks []RecognitionBlock
	for _, block := range results[1].Blocks {
		if block.Role != "body_title" {
			blocks = append(blocks, block)
			continue
		}
		for i, role := range []string{"body_title", "body_separator", "body_speaker"} {
			next := block
			next.Role = role
			next.SourceIDs = []string{}
			next.SourceRanges = []RecognitionSourceRange{{ID: block.SourceIDs[0], Start: []int{0, 2, 3}[i], End: []int{2, 3, 6}[i]}}
			if role != "body_title" {
				next.SectionKind = ""
			}
			var ok bool
			next.Text, next.Box, ok = recognitionNativeValue(pages[1], next)
			if !ok {
				t.Fatal("range binding")
			}
			blocks = append(blocks, next)
		}
	}
	results[1].Blocks = blocks
	candidate, _, err := AssembleRecognition(source, pages, results, candidateIssue(), ConversionTemplateSettings(), validDocument(4).LayoutManifest.Assets)
	if err != nil {
		t.Fatal(err)
	}
	body := candidate.Document.Components[1]
	var separator *FixedSlot
	for i := range candidate.Document.LayoutManifest.Pages[1].FixedSlots {
		slot := &candidate.Document.LayoutManifest.Pages[1].FixedSlots[i]
		if slot.Element == "speakerSeparator" {
			separator = slot
		}
	}
	if separator == nil || separator.ID != "fixed-"+body.ID+"-speakerSeparator" {
		t.Fatal("native composer cannot bind separator to its article title")
	}
	if math.Abs(separator.Style.LineHeight-math.Max(separator.Style.FontSize, separator.Box.Height*candidate.Document.Pages[1].Height)) > 1e-9 {
		t.Fatal("separator used paragraph leading instead of native inline metrics")
	}
}
