package onlinebulletins

import (
	"strconv"
	"strings"
	"time"
)

// ReaderAnchor contains no source/PDF coordinates or private extraction evidence.
type ReaderAnchor struct{ ID, Text, ComponentID, PageID string }

func ReaderAnchors(document Document, metadata CanonicalMetadata) []ReaderAnchor {
	pages := map[string]string{}
	fixed := map[string]string{}
	for _, page := range document.LayoutManifest.Pages {
		for _, slot := range page.Slots {
			for _, fragment := range slot.Fragments {
				if pages[fragment.SentenceID] == "" {
					pages[fragment.SentenceID] = page.PageID
				}
			}
		}
		for _, slot := range page.FixedSlots {
			if fixed[slot.Element] == "" {
				fixed[slot.Element] = page.PageID
			}
		}
	}
	anchors := []ReaderAnchor{}
	date := metadata.Date
	if parsed, err := time.Parse("2006-01-02", date); err == nil {
		date = parsed.Format("Jan.2.2006")
	}
	for _, field := range []struct{ id, text string }{{"title", metadata.Title}, {"subtitle", metadata.Subtitle}, {"issueNumber", "第" + strconv.Itoa(metadata.IssueNumber) + "期"}, {"date", date}} {
		if page := fixed[field.id]; page != "" {
			anchors = append(anchors, ReaderAnchor{ID: "canonical-" + field.id, Text: field.text, ComponentID: "canonical", PageID: page})
		}
	}
	for i := range document.Components {
		component := &document.Components[i]
		for _, block := range componentBlocks(component) {
			for _, sentence := range block.Sentences {
				var text strings.Builder
				for _, span := range sentence.Spans {
					text.WriteString(span.Text)
				}
				anchors = append(anchors, ReaderAnchor{ID: sentence.ID, Text: text.String(), ComponentID: component.ID, PageID: pages[sentence.ID]})
			}
		}
	}
	return anchors
}
