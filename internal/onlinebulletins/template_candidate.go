package onlinebulletins

import (
	"errors"
	"math"
	"regexp"
	"strconv"

	"github.com/google/uuid"
)

var sourceFailurePage = regexp.MustCompile(`(?:^|_)page_([0-9]+)(?:$|_)`)

type CanonicalIssue struct {
	IssueID       string            `json:"issueId"`
	Series        string            `json:"series"`
	ContentLocale string            `json:"contentLocale"`
	Metadata      CanonicalMetadata `json:"metadata"`
}

// Evidence has already passed the exclusion boundary. An unsupported source
// has no document: never fabricate an empty component to make it publishable.
type DocumentCandidate struct {
	SourceCoverPNG       []byte                 `json:"sourceCoverPng,omitempty"`
	Recognition          *RecognitionEvidence   `json:"recognition,omitempty"`
	SourceImages         map[string]SourceImage `json:"sourceImages,omitempty"`
	Document             *Document              `json:"document,omitempty"`
	Evidence             GeometryDocument       `json:"evidence"`
	LearningMetrics      *LearningMetrics       `json:"learningMetrics,omitempty"`
	LearningApplications []LearningApplication  `json:"learningApplications,omitempty"`
}

// The manifest is explicit trusted job input, after LoadTemplateV1Assets, not
// hidden mutable configuration or an uploaded document's renderer choice.
func ApplyTemplateV1(source GeometryDocument, canonical CanonicalIssue, manifest LayoutManifest) (DocumentCandidate, []ReviewIssue, error) {
	return applyTemplate(source, canonical, manifest, nil)
}

func applyTemplate(source GeometryDocument, canonical CanonicalIssue, manifest LayoutManifest, snapshot *TemplateSettings) (DocumentCandidate, []ReviewIssue, error) {
	if uuid.Validate(canonical.IssueID) != nil || canonical.Series != "general" || canonical.ContentLocale != "zh-Hant" || !hashPattern.MatchString(source.SourceSHA256) || source.PopplerVersion != PopplerVersion {
		return DocumentCandidate{}, nil, ErrUnsupportedSource
	}
	if manifest.TemplateVersion != "v1" || (manifest.RendererVersion != "v1" && manifest.RendererVersion != "v3" && manifest.RendererVersion != "v4" && manifest.RendererVersion != "v5" && manifest.RendererVersion != "v6" && manifest.RendererVersion != "v7" && manifest.RendererVersion != "v8" && !((manifest.RendererVersion == "v9" || manifest.RendererVersion == "v10") && snapshot != nil)) || !hashPattern.MatchString(manifest.RendererArtifactSha256) || !validManifestAssets(manifest.Assets) {
		return DocumentCandidate{}, nil, ErrTemplateAssets
	}
	composed := manifest.RendererVersion == "v7" || manifest.RendererVersion == "v8" || manifest.RendererVersion == "v9" || manifest.RendererVersion == "v10"
	currentRenderer := manifest.RendererVersion == "v8" || manifest.RendererVersion == "v9" || manifest.RendererVersion == "v10"
	allowed, issues, err := filterTemplateV1Geometry(source)
	if err != nil {
		return DocumentCandidate{}, nil, err
	}
	candidate := DocumentCandidate{Evidence: allowed}
	stage, sourcePage := "cover", 1
	unsupported := func(err error) (DocumentCandidate, []ReviewIssue, error) {
		if !errors.Is(err, ErrUnsupportedSource) && !errors.Is(err, ErrInvalidGeometry) && !errors.Is(err, ErrGeometryLimits) {
			return DocumentCandidate{}, nil, err
		}
		if match := sourceFailurePage.FindStringSubmatch(err.Error()); len(match) == 2 {
			if page, parseErr := strconv.Atoi(match[1]); parseErr == nil && page >= 1 && page <= len(allowed.Pages) {
				sourcePage = page
			}
		}
		return candidate, append(issues, ReviewIssue{Code: "unsupported_" + stage + "_structure", SourcePage: sourcePage, Blocking: true}), nil
	}
	cover, coverSlots, coverFixed, coverIssues, err := coverComponent(allowed.Pages[0])
	if err != nil {
		return unsupported(err)
	}
	issues = append(issues, coverIssues...)
	var historical *historicalCoverProposal
	if manifest.RendererVersion == "v6" || composed {
		historical, err = historicalCoverHeader(allowed.Pages[0], canonical.Metadata)
		if err != nil {
			return unsupported(err)
		}
		if historical != nil {
			coverFixed = append(coverFixed, historical.fixed...)
			issues = append(issues, historical.issues...)
		}
	}
	var metadataSlots, churchSlots, footerSlots []FixedSlot
	currentOutlined := false
	if historical != nil && snapshot != nil {
		for _, slot := range historical.fixed {
			currentOutlined = currentOutlined || slot.Element == "visionMission"
		}
	}
	currentProfile := currentRenderer && canonical.Metadata.IssueNumber >= 1739 && (historical == nil || currentOutlined)
	if currentOutlined {
		// Snapshot-owned church wording is automatic; unreadable issue metadata still needs a person.
		kept := issues[:0]
		for _, issue := range issues {
			if issue.ComponentID == "cover" && (issue.Code == "unmapped_fixed_template" || issue.Code == "fixed_template_text_variant") {
				continue
			}
			kept = append(kept, issue)
		}
		issues = append(kept, ReviewIssue{Code: "source_metadata_confirmation", ComponentID: "cover", SourcePage: 1, Blocking: true})
	}
	var mastheadSlot *FixedSlot
	coverMetadataMatched := false
	if historical == nil || len(historical.recognizedImages) == 0 {
		var metadataIssues, churchIssues, footerIssues, mastheadIssues []ReviewIssue
		metadataSlots, metadataIssues, err = coverMetadataSlots(allowed.Pages[0], canonical.Metadata)
		if err != nil {
			return unsupported(err)
		}
		issues = append(issues, metadataIssues...)
		coverMetadataMatched = len(metadataIssues) == 0
		if snapshot != nil {
			churchSlots, churchIssues, err = coverSnapshotChurchSlots(allowed.Pages[0])
		} else {
			churchSlots, churchIssues, err = coverChurchFieldSlots(allowed.Pages[0], historical == nil)
		}
		if err != nil {
			return unsupported(err)
		}
		issues = append(issues, churchIssues...)
		if manifest.RendererVersion != "v1" {
			footerSlots, footerIssues = coverReaderLogoSlots(allowed.Pages[0])
		} else {
			footerSlots, footerIssues, err = coverFooterSlots(allowed.Pages[0])
		}
		if err != nil {
			return unsupported(err)
		}
		issues = append(issues, footerIssues...)
		mastheadSlot, mastheadIssues = coverMastheadSlot(allowed.Pages[0])
		issues = append(issues, mastheadIssues...)
	}
	stage, sourcePage = "hymn", len(allowed.Pages)-1
	bodyTail, hymnPage, err := splitHymnPage(allowed.Pages[len(allowed.Pages)-2])
	if err != nil {
		return unsupported(err)
	}
	bodyPages := append([]GeometryPage(nil), allowed.Pages[1:len(allowed.Pages)-2]...)
	if bodyTail != nil {
		bodyPages = append(bodyPages, *bodyTail)
	}
	stage, sourcePage = "body", 2
	body, err := bodyComponents(bodyPages)
	if err != nil {
		return unsupported(err)
	}
	for _, issue := range body.issues {
		// V7 composes measured output; the source frame proves the semantic split.
		if composed && issue.Code == "inferred_inline_speaker_geometry" && body.framedSpeakers[issue.ComponentID] {
			continue
		}
		issues = append(issues, issue)
	}
	artSlots, artIssues := bodyCanonicalArtSlots(allowed.Pages[1], canonical.Metadata)
	verifiedArt := currentProfile && verifiedCurrentBodyArtwork(allowed.Pages[1], canonical.Metadata) && len(artSlots) >= 2
	if currentProfile && manifest.RendererVersion == "v10" && coverMetadataMatched && templateOwnedHeaderArtwork(allowed.Pages[1], artSlots, canonical.Metadata) {
		verifiedArt = true
	}
	if currentOutlined && manifest.RendererVersion == "v10" && templateOwnedHeaderArtwork(allowed.Pages[1], artSlots, canonical.Metadata) {
		verifiedArt = true
		issues = append(issues, ReviewIssue{Code: "source_metadata_confirmation", SourcePage: 2, Blocking: true})
	}
	if verifiedArt {
		artIssues = nil
	}
	body.fixed = append(body.fixed, artSlots...)
	issues = append(issues, artIssues...)
	header, err := bodyHeaderComponent(body.header, canonical.Metadata, len(allowed.Pages), body.components[0].ID)
	if err != nil {
		return unsupported(err)
	}
	// The reader excludes the production band; keep article titles/speakers above.
	if composed {
		for _, issue := range header.issues {
			if issue.Code == "canonical_source_mismatch" {
				issue.SourcePage = 2
				issues = append(issues, issue)
			}
		}
		// Current reader keeps the omitted production band's whitespace and divider.
		if !currentRenderer || canonical.Metadata.IssueNumber < 1739 {
			artBottom, first := 0.0, 1.0
			for _, slot := range artSlots {
				if slot.Element == "title" || slot.Element == "subtitle" {
					artBottom = math.Max(artBottom, slot.Box.Y+slot.Box.Height)
				}
			}
			for _, slot := range body.slots {
				if slot.sourcePage == 2 {
					first = math.Min(first, slot.Box.Y)
				}
			}
			shift := 0.0
			if artBottom > 0 {
				shift = math.Max(0, first-artBottom-16/allowed.Pages[1].Height)
			}
			for i := range body.slots {
				if body.slots[i].sourcePage == 2 {
					body.slots[i].Box.Y -= shift
				}
			}
			fixed := body.fixed[:0]
			for _, slot := range body.fixed {
				if slot.sourcePage == 2 && slot.Element == "topRule" {
					continue
				}
				if slot.sourcePage == 2 && slot.Element == "speakerSeparator" {
					slot.Box.Y -= shift
				}
				fixed = append(fixed, slot)
			}
			body.fixed = fixed
		}
	} else {
		issues = append(issues, header.issues...)
		if header.header != nil {
			body.components[0].BodySection.Header = header.header
			body.slots = append(header.slots, body.slots...)
			body.fixed = append(body.fixed, header.fixed...)
		}
	}
	stage, sourcePage = "hymn", len(allowed.Pages)-1
	hymns, hymnSlots, hymnFixed, hymnIssues, err := hymnComponent(hymnPage)
	if err != nil {
		return unsupported(err)
	}
	issues = append(issues, hymnIssues...)
	if bodyTail != nil {
		kept := hymnFixed[:0]
		for _, slot := range hymnFixed {
			if slot.Element != "pageNumber" {
				kept = append(kept, slot)
			}
		}
		hymnFixed = kept
	}
	stage, sourcePage = "back", len(allowed.Pages)
	back, backSlots, backFixed, backIssues, err := backComponents(allowed.Pages[len(allowed.Pages)-1])
	if err != nil {
		return unsupported(err)
	}
	issues = append(issues, backIssues...)
	sidebarSlots, sidebarIssues := backSidebarSlots(allowed.Pages[len(allowed.Pages)-1])
	backFixed = append(backFixed, sidebarSlots...)
	issues = append(issues, sidebarIssues...)
	frameSlots, frameIssues := backFrameSlots(allowed.Pages[len(allowed.Pages)-1])
	if currentProfile {
		frameSlots, frameIssues = currentBackFrameSlots(allowed.Pages[len(allowed.Pages)-1], backSlots, backFixed)
	}
	backFixed = append(backFixed, frameSlots...)
	issues = append(issues, frameIssues...)
	document := Document{IssueID: canonical.IssueID, Series: canonical.Series, ContentLocale: canonical.ContentLocale, SchemaVersion: "1", TemplateVersion: "v1", SourceAssetChecksum: allowed.SourceSHA256, SourcePageCount: len(allowed.Pages)}
	if snapshot != nil {
		frozen := *snapshot
		document.TemplateSnapshot = &frozen
	}
	document.LayoutManifest = LayoutManifest{TemplateVersion: "v1", RendererVersion: manifest.RendererVersion, RendererArtifactSha256: manifest.RendererArtifactSha256, Assets: append([]Asset(nil), manifest.Assets...)}
	for _, page := range allowed.Pages {
		id := "page-" + strconv.Itoa(page.Number)
		document.Pages = append(document.Pages, Page{ID: id, Width: page.Width, Height: page.Height})
		document.LayoutManifest.Pages = append(document.LayoutManifest.Pages, LayoutPage{PageID: id, Slots: []Slot{}})
		for _, image := range page.Images {
			if page.Number == 2 && verifiedArt {
				continue
			}
			knownHistorical := false
			if page.Number == 1 && historical != nil {
				for _, recognized := range historical.recognizedImages {
					knownHistorical = knownHistorical || image == recognized
				}
			}
			if page.Number != 1 || (!knownHistorical && coverGraphicElement(image) == "" && !isCoverMastheadAnchor(image) && !(manifest.RendererVersion != "v1" && isExcludedCoverQR(image))) {
				issues = append(issues, ReviewIssue{Code: "unknown_source_image", SourcePage: page.Number, Blocking: true})
			}
		}
	}
	document.Components = append([]Component{cover}, body.components...)
	document.Components = append(document.Components, hymns)
	document.Components = append(document.Components, back...)
	if composed {
		blocks := map[string]*Block{}
		for i := range document.Components {
			for _, block := range componentBlocks(&document.Components[i]) {
				blocks[block.ID] = block
			}
		}
		kept := issues[:0]
		for _, issue := range issues {
			if block := blocks[issue.BlockID]; issue.Code == "non_uniform_source_tracking" && block != nil {
				// Use normal spacing; the compositor must still prove fit on the same page.
				block.Style.LetterSpacing = 0
				continue
			}
			kept = append(kept, issue)
		}
		issues = kept
	}
	for _, slots := range [][]Slot{coverSlots, body.slots, hymnSlots, backSlots} {
		for _, slot := range slots {
			if slot.sourcePage < 1 || slot.sourcePage > len(document.Pages) {
				return DocumentCandidate{}, nil, ErrInvalidGeometry
			}
			layout := &document.LayoutManifest.Pages[slot.sourcePage-1]
			layout.Slots = append(layout.Slots, slot)
		}
	}
	document.LayoutManifest.Pages[0].FixedSlots = append(coverFixed, metadataSlots...)
	document.LayoutManifest.Pages[0].FixedSlots = append(document.LayoutManifest.Pages[0].FixedSlots, churchSlots...)
	document.LayoutManifest.Pages[0].FixedSlots = append(document.LayoutManifest.Pages[0].FixedSlots, footerSlots...)
	if mastheadSlot != nil {
		document.LayoutManifest.Pages[0].FixedSlots = append(document.LayoutManifest.Pages[0].FixedSlots, *mastheadSlot)
	}
	document.LayoutManifest.Pages[len(allowed.Pages)-2].FixedSlots = hymnFixed
	document.LayoutManifest.Pages[len(allowed.Pages)-1].FixedSlots = backFixed
	for _, slot := range body.fixed {
		if slot.sourcePage < 2 || slot.sourcePage > len(allowed.Pages)-1 {
			return DocumentCandidate{}, nil, ErrInvalidGeometry
		}
		layout := &document.LayoutManifest.Pages[slot.sourcePage-1]
		layout.FixedSlots = append(layout.FixedSlots, slot)
	}
	if currentProfile {
		issues = append(issues, currentFixedTemplateIssues(document)...)
	} else {
		// Legacy profiles retain their independent visual reconstruction gate.
		issues = append(issues, ReviewIssue{Code: "unmapped_fixed_template", Blocking: true})
	}
	validationIssues, err := ValidateDocument(document)
	if err != nil {
		stage, sourcePage = "source", 0
		return unsupported(ErrUnsupportedSource)
	}
	issues = append(issues, validationIssues...)
	seen := map[ReviewIssue]bool{}
	distinct := []ReviewIssue{}
	for _, issue := range issues {
		if (manifest.RendererVersion == "v6" || composed) && issue.ComponentID == "cover" && issue.SourcePage == 0 {
			issue.SourcePage = 1
		}
		if !seen[issue] {
			seen[issue] = true
			distinct = append(distinct, issue)
		}
	}
	candidate.Document = &document
	return candidate, distinct, nil
}
