package onlinebulletins

import "slices"

// Seeds are provisional, server-owned geometry. Only the isolated compositor
// can make them publishable; text is never clipped to fit a borrowed box.
func seedAddedContent(current Document, draft *Document, lengths map[string]int) error {
	type placement struct {
		page int
		slot Slot
	}
	positions := map[string]placement{}
	componentPositions := map[string]placement{}
	bodyBoxes := map[string]Box{}
	oldSentences := map[string][]string{}
	oldOrder := map[string][]string{}
	for _, component := range current.Components {
		for _, block := range componentBlocks(&component) {
			oldOrder[component.ID] = append(oldOrder[component.ID], block.ID)
			for _, sentence := range block.Sentences {
				oldSentences[block.ID] = append(oldSentences[block.ID], sentence.ID)
			}
		}
	}
	for i, page := range current.LayoutManifest.Pages {
		for _, slot := range page.Slots {
			if _, found := positions[slot.BlockID]; !found {
				positions[slot.BlockID] = placement{i, slot}
			}
			if _, found := componentPositions[slot.ComponentID]; !found {
				componentPositions[slot.ComponentID] = placement{i, slot}
			}
		}
	}
	for _, component := range current.Components {
		if component.BodySection == nil {
			continue
		}
		for _, block := range component.BodySection.Blocks {
			if position, exists := positions[block.ID]; exists {
				bodyBoxes[component.ID] = position.slot.Box
				break
			}
		}
	}
	// Empty back-page lists have no text slot. Their church-owned label (or
	// neighboring back list) supplies the initial region for the first item.
	for _, component := range draft.Components {
		if _, exists := componentPositions[component.ID]; exists {
			continue
		}
		label := map[string]string{"backSummary": "summaryLabel", "announcements": "announcementsLabel", "victoriesAndPrayers": "prayersLabel"}[component.Type]
		if label == "" {
			continue
		}
		for _, old := range current.Components {
			if old.Type != "backSummary" && old.Type != "announcements" && old.Type != "victoriesAndPrayers" {
				continue
			}
			if position, exists := componentPositions[old.ID]; exists {
				position.slot.Box.Y = min(.85, position.slot.Box.Y+.004)
				for _, fixed := range current.LayoutManifest.Pages[position.page].FixedSlots {
					if fixed.Element == label {
						position.slot.Box.Y = min(.85, fixed.Box.Y+fixed.Box.Height+.004)
					}
				}
				componentPositions[component.ID] = position
				break
			}
		}
	}
	rebuilt := map[string]bool{}
	additions := map[int][]Slot{}
	for _, component := range draft.Components {
		blocks := componentBlocks(&component)
		bodyBlocks := map[string]bool{}
		if component.BodySection != nil {
			for _, block := range component.BodySection.Blocks {
				bodyBlocks[block.ID] = true
			}
		}
		currentIDs := map[string]bool{}
		newOrder := []string{}
		for _, block := range blocks {
			currentIDs[block.ID] = true
			if _, exists := positions[block.ID]; exists {
				newOrder = append(newOrder, block.ID)
			}
		}
		sharedOrder := []string{}
		pool := []placement{}
		for _, id := range oldOrder[component.ID] {
			if currentIDs[id] {
				sharedOrder = append(sharedOrder, id)
				pool = append(pool, positions[id])
			}
		}
		moved := map[string]bool{}
		if len(sharedOrder) == len(newOrder) && !slices.Equal(sharedOrder, newOrder) {
			for i, id := range newOrder {
				if sharedOrder[i] != id {
					positions[id] = pool[i]
					moved[id] = true
				}
			}
		}
		for i, block := range blocks {
			ids := []string{}
			for _, sentence := range block.Sentences {
				ids = append(ids, sentence.ID)
			}
			position, exists := positions[block.ID]
			if exists && position.slot.ComponentID != component.ID {
				exists = false
			}
			if exists && !moved[block.ID] && slices.Equal(ids, oldSentences[block.ID]) {
				continue
			}
			if !exists {
				var found bool
				// Borrow a neighboring paragraph from this component, not an
				// unrelated page or a client-provided rectangle.
				for j := i - 1; j >= 0; j-- {
					if p, ok := positions[blocks[j].ID]; ok {
						position = p
						position.slot.Box.Y += .004
						found = true
						break
					}
				}
				if !found {
					for j := i + 1; j < len(blocks); j++ {
						if p, ok := positions[blocks[j].ID]; ok {
							position = p
							position.slot.Box.Y = max(0, position.slot.Box.Y-.004)
							found = true
							break
						}
					}
				}
				if !found {
					position, found = componentPositions[component.ID]
				}
				if !found {
					return ErrComponentEdit
				}
				position.slot.ID = templateID("edit-slot-", block.ID)
				if box, exists := bodyBoxes[component.ID]; exists && bodyBlocks[block.ID] {
					position.slot.Box.X, position.slot.Box.Width = box.X, box.Width
				}
				position.slot.Box.Height = min(position.slot.Box.Height, .1)
				position.slot.Box.Y = min(position.slot.Box.Y, 1-position.slot.Box.Height)
			}
			position.slot.ComponentID, position.slot.BlockID = component.ID, block.ID
			position.slot.ContinuationOf = ""
			position.slot.Fragments = []Fragment{}
			for _, sentence := range block.Sentences {
				position.slot.Fragments = append(position.slot.Fragments, Fragment{SentenceID: sentence.ID, End: lengths[sentence.ID]})
			}
			positions[block.ID] = position
			rebuilt[block.ID] = true
			additions[position.page] = append(additions[position.page], position.slot)
		}
	}
	for i := range draft.LayoutManifest.Pages {
		page := &draft.LayoutManifest.Pages[i]
		kept := make([]Slot, 0, len(page.Slots)+len(additions[i]))
		for _, slot := range page.Slots {
			if !rebuilt[slot.BlockID] {
				kept = append(kept, slot)
			}
		}
		page.Slots = append(kept, additions[i]...)
	}
	return nil
}
