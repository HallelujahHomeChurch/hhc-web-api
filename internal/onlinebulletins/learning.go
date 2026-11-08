package onlinebulletins

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"strings"
	"unicode/utf8"
)

type BodyKindCorrection struct {
	ComponentID string `json:"componentId"`
	SourcePage  int    `json:"sourcePage"`
	Signature   string `json:"signature"`
	Title       string `json:"title"`
	Speaker     string `json:"speaker"`
	Kind        string `json:"kind"`
}

// Call only after the saved revision has passed review and layout validation.
func CollectBodyKindCorrections(base, local Document, evidence GeometryDocument) []BodyKindCorrection {
	if base.ContentLocale != "zh-Hant" || base.Series != "general" || base.IssueID != local.IssueID || base.Series != local.Series || base.ContentLocale != local.ContentLocale || base.SourceAssetChecksum != local.SourceAssetChecksum || base.SourcePageCount != local.SourcePageCount || base.TemplateVersion != local.TemplateVersion || !reflect.DeepEqual(base.TemplateSnapshot, local.TemplateSnapshot) || !reflect.DeepEqual(base.Pages, local.Pages) || base.LayoutManifest.RendererVersion != local.LayoutManifest.RendererVersion || base.LayoutManifest.RendererArtifactSha256 != local.LayoutManifest.RendererArtifactSha256 || (base.LayoutManifest.RendererVersion != "v8" && base.LayoutManifest.RendererVersion != "v9" && base.LayoutManifest.RendererVersion != "v10") || evidence.SourceSHA256 != base.SourceAssetChecksum || evidence.PopplerVersion != PopplerVersion || len(base.Components) != len(local.Components) {
		return nil
	}
	var corrections []BodyKindCorrection
	for index, component := range base.Components {
		edited := local.Components[index]
		if component.Type != "bodySection" || component.BodySection == nil || edited.BodySection == nil || component.ID != edited.ID || component.Type != edited.Type || component.BodySection.Kind == edited.BodySection.Kind {
			continue
		}
		switch edited.BodySection.Kind {
		case "sermon", "testimony", "teaching", "reflection":
		default:
			continue
		}
		body := *component.BodySection
		body.Kind = edited.BodySection.Kind
		if !reflect.DeepEqual(&body, edited.BodySection) {
			continue
		}
		correction, ok := sourceBodyKindCorrection(base, component, evidence)
		if ok {
			correction.Kind = edited.BodySection.Kind
			corrections = append(corrections, correction)
		}
	}
	return corrections
}

func sourceBodyKindCorrection(document Document, component Component, evidence GeometryDocument) (BodyKindCorrection, bool) {
	section := component.BodySection
	// shortcut: inherited or ambiguous speakers remain manual until their source chain is proven.
	if len(section.Title.Sentences) != 1 || section.Title.Sentences[0].Source == nil || len(section.Contributors) != 1 || section.Contributors[0].Role != "speaker" || len(section.Contributors[0].Name.Sentences) != 1 || section.Contributors[0].Name.Sentences[0].Source == nil {
		return BodyKindCorrection{}, false
	}
	titleSource := section.Title.Sentences[0].Source
	speakerSource := section.Contributors[0].Name.Sentences[0].Source
	if titleSource.Page != speakerSource.Page || titleSource.Page < 2 || titleSource.Page >= document.SourcePageCount-1 {
		return BodyKindCorrection{}, false
	}
	text := func(block Block) string {
		var value strings.Builder
		for _, sentence := range block.Sentences {
			for _, span := range sentence.Spans {
				value.WriteString(span.Text)
			}
		}
		return strings.Join(strings.Fields(value.String()), "")
	}
	title, speaker := text(section.Title), text(section.Contributors[0].Name)
	if title == "" || speaker == "" || utf8.RuneCountInString(title) > 512 || utf8.RuneCountInString(speaker) > 256 {
		return BodyKindCorrection{}, false
	}
	var matches []BodyKindCorrection
	for _, page := range evidence.Pages {
		if page.Number != titleSource.Page || !supportedTemplatePaper(page) {
			continue
		}
		lines, err := sourceLines(page, false)
		if err != nil {
			continue
		}
		for _, line := range lines {
			separators := 0
			for _, char := range lineText(line) {
				if strings.ContainsRune("～〜~", char) {
					separators++
				}
			}
			if separators != 1 {
				continue
			}
			for _, frame := range page.Frames {
				inside, outside, ok := framedBodyHeading(line, []GeometryFrame{frame})
				if !ok || len(outside) < 2 || !isSpeakerSeparator(outside[0].Text) || inside[0].Box != titleSource.Box || outside[1].Box != speakerSource.Box || strings.Join(strings.Fields(lineText(geometryLine{parts: inside})), "") != title || strings.Join(strings.Fields(lineText(geometryLine{parts: outside[1:]})), "") != speaker {
					continue
				}
				fonts := make([]struct {
					Family string
					Size   float64
				}, 0, len(line.parts))
				for _, part := range line.parts {
					fonts = append(fonts, struct {
						Family string
						Size   float64
					}{part.FontFamily, part.FontSize})
				}
				profile, _ := json.Marshal([]any{document.TemplateVersion, document.LayoutManifest.RendererVersion, document.LayoutManifest.RendererArtifactSha256, page.Width, page.Height, title, speaker, fonts})
				digest := sha256.Sum256(profile)
				matches = append(matches, BodyKindCorrection{ComponentID: component.ID, SourcePage: page.Number, Signature: hex.EncodeToString(digest[:]), Title: title, Speaker: speaker})
			}
		}
	}
	if len(matches) != 1 {
		return BodyKindCorrection{}, false
	}
	return matches[0], true
}
