package onlinebulletins

import (
	"encoding/json"
	"errors"
	"unicode/utf8"
)

var ErrComponentEdit = errors.New("invalid online component edit")

// EditComponents retains server-owned source evidence and layout geometry.
// The result is only a draft seed: it must be composed/measured again before
// confirmation or publication. Callers cannot submit coordinates or proof.
func EditComponents(current Document, components []Component) (Document, error) {
	if _, err := ValidateDocument(current); err != nil {
		return Document{}, ErrComponentEdit
	}
	raw, err := json.Marshal(current)
	if err != nil {
		return Document{}, ErrComponentEdit
	}
	var draft Document
	if json.Unmarshal(raw, &draft) != nil {
		return Document{}, ErrComponentEdit
	}
	sources := map[string]*SourceLocation{}
	illustrations := map[string][]Illustration{}
	for i := range draft.Components {
		illustrations[draft.Components[i].ID] = draft.Components[i].Illustrations
		for _, block := range componentBlocks(&draft.Components[i]) {
			for _, sentence := range block.Sentences {
				sources[sentence.ID] = sentence.Source
			}
		}
	}
	raw, err = json.Marshal(components)
	// Decode into fresh values: Unmarshal into an existing slice retains absent
	// pointer fields, including the old source coordinates we explicitly reject.
	draft.Components = nil
	if err != nil || len(raw) > MaxDocumentBytes || json.Unmarshal(raw, &draft.Components) != nil {
		return Document{}, ErrComponentEdit
	}
	lengths := map[string]int{}
	for i := range draft.Components {
		if len(draft.Components[i].Illustrations) != 0 {
			return Document{}, ErrComponentEdit
		}
		draft.Components[i].Illustrations = illustrations[draft.Components[i].ID]
		for _, block := range componentBlocks(&draft.Components[i]) {
			for j := range block.Sentences {
				sentence := &block.Sentences[j]
				if sentence.Source != nil {
					return Document{}, ErrComponentEdit
				}
				if source := sources[sentence.ID]; source != nil {
					copy := *source
					sentence.Source = &copy
				}
				for _, span := range sentence.Spans {
					lengths[sentence.ID] += utf8.RuneCountInString(span.Text)
				}
			}
		}
	}
	if err := seedAddedContent(current, &draft, lengths); err != nil {
		return Document{}, err
	}
	ends := map[string]int{}
	for _, page := range draft.LayoutManifest.Pages {
		for _, slot := range page.Slots {
			for _, fragment := range slot.Fragments {
				ends[fragment.SentenceID] = fragment.End
			}
		}
	}
	previous := map[string]string{}
	for i := range draft.LayoutManifest.Pages {
		page := &draft.LayoutManifest.Pages[i]
		kept := make([]Slot, 0, len(page.Slots))
		for _, slot := range page.Slots {
			fragments := make([]Fragment, 0, len(slot.Fragments))
			for _, fragment := range slot.Fragments {
				length := lengths[fragment.SentenceID]
				if fragment.Start >= length {
					continue
				}
				if fragment.End == ends[fragment.SentenceID] {
					fragment.End = length
				} else {
					fragment.End = min(fragment.End, length)
				}
				fragments = append(fragments, fragment)
			}
			if len(fragments) == 0 {
				continue
			}
			slot.Fragments = fragments
			if slot.ContinuationOf != "" {
				slot.ContinuationOf = previous[slot.BlockID]
			}
			previous[slot.BlockID] = slot.ID
			kept = append(kept, slot)
		}
		page.Slots = kept
	}
	draft.LayoutManifest.ContentHash, draft.LayoutManifest.LayoutValidationHash = "", ""
	if _, err = ValidateDocument(draft); err != nil {
		return Document{}, ErrComponentEdit
	}
	return draft, nil
}

// Pointers let editing and anchor reconciliation walk every typed text field,
// including titles and contributor names, without reflection or JSON paths.
func componentBlocks(component *Component) []*Block {
	var blocks []*Block
	add := func(values []Block) {
		for i := range values {
			blocks = append(blocks, &values[i])
		}
	}
	items := func(values []Item) {
		for i := range values {
			if values[i].Title != nil {
				blocks = append(blocks, values[i].Title)
			}
			add(values[i].Blocks)
		}
	}
	if cover := component.Cover; cover != nil {
		add(cover.Welcome)
		items(cover.Worship)
		items(cover.Work)
		items(cover.WordQuestions)
		add(cover.WeeklyVerses)
	}
	if body := component.BodySection; body != nil {
		if header := body.Header; header != nil {
			blocks = append(blocks, &header.LectureDate)
			for i := range header.Contributors {
				blocks = append(blocks, &header.Contributors[i].Name)
			}
		}
		blocks = append(blocks, &body.Title)
		if body.Subtitle != nil {
			blocks = append(blocks, body.Subtitle)
		}
		for i := range body.Contributors {
			blocks = append(blocks, &body.Contributors[i].Name)
		}
		add(body.Blocks)
	}
	if lyrics := component.HymnLyrics; lyrics != nil {
		for i := range lyrics.Hymns {
			hymn := &lyrics.Hymns[i]
			for _, block := range []*Block{hymn.Number, &hymn.Title, hymn.SourceLabel} {
				if block != nil {
					blocks = append(blocks, block)
				}
			}
			for j := range hymn.Sections {
				add(hymn.Sections[j].Lines)
			}
		}
	}
	items(component.Items)
	return blocks
}
